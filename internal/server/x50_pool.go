package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/x50signupstore"
)

type x50Settings struct {
	BaseURL      string `json:"baseUrl"`
	ModelSyncSec int    `json:"modelSyncSec"`
	DetailLog    *bool  `json:"detailLog,omitempty"`
	RequestLog   *bool  `json:"requestLog,omitempty"`
	ErrorLog     *bool  `json:"errorLog,omitempty"`
}

func (s *Server) x50HTTPClient(c *config.Config, proxy string) *http.Client {
	r := c.X50.EffectiveRouting()
	key := "\x00x50:" + strconv.Itoa(r.DirectMaxConns) + ":" + strconv.Itoa(r.ProxyMaxConns) + ":" + proxy
	return &http.Client{Transport: s.transportFor(proxy, r, key), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func x50AccountFor(c *config.Config, key string) string {
	for _, a := range c.Accounts {
		if a.Provider == config.ProviderX50 && a.RuntimeKey == key {
			return a.ID
		}
	}
	return ""
}

// A candidate's imported state is a projection of the durable config, not a
// second flag that can survive deletion or get ahead of a failed disk write.
func (s *Server) apiX50PoolImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []string `json:"ids"`
	}
	if e := readBody(r, &in); e != nil {
		apiError(w, 400, e)
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > x50MaxBatch {
		apiError(w, 400, errors.New("请选择1–500个候选"))
		return
	}
	st, e := s.x50Store()
	if e != nil {
		apiError(w, 500, e)
		return
	}
	type item struct {
		ID        string `json:"id"`
		AccountID string `json:"accountId,omitempty"`
		Already   bool   `json:"already,omitempty"`
		Error     string `json:"error,omitempty"`
	}
	results := []item{}
	rows := []x50signupstore.Row{}
	seen := map[string]bool{}
	for _, id := range in.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		row, e := st.Get(id)
		if e != nil || row.Status != x50signupstore.Keyed || !config.ValidRuntimeKeyFor(config.ProviderX50, row.APIKey) {
			results = append(results, item{ID: id, Error: "候选不存在、未完成或Key无效"})
			continue
		}
		rows = append(rows, row)
	}
	added := 0
	cfg, e := s.cfg.Update(-1, func(c *config.Config) error {
		// The callback is serialized by Store.Update. Nothing is reported as
		// imported until Normalize + atomic disk replacement have succeeded.
		for _, row := range rows {
			if id := x50AccountFor(c, row.APIKey); id != "" {
				results = append(results, item{ID: row.ID, AccountID: id, Already: true})
				continue
			}
			conflict := false
			for _, a := range c.Accounts {
				if a.RuntimeKey == row.APIKey {
					conflict = true
					break
				}
			}
			if conflict {
				results = append(results, item{ID: row.ID, Error: "此Key已属于其他池，拒绝跨池"})
				continue
			}
			a := config.Account{ID: config.NewID("acc_"), Provider: config.ProviderX50, Group: config.ProviderX50, Name: row.Email, Email: row.Email, RuntimeKey: row.APIKey, ControlKey: row.SessionToken, ProxyURL: row.ProxyURL, Enabled: true, Weight: 1}
			probe := c.Clone()
			probe.Accounts = append(probe.Accounts, a)
			if err := probe.Normalize(); err != nil {
				results = append(results, item{ID: row.ID, Error: "候选配置无效（代理/会话/名称等），请检查注册记录"})
				continue
			}
			c.Accounts = append(c.Accounts, a)
			added++
			results = append(results, item{ID: row.ID, AccountID: a.ID})
		}
		return nil
	})
	if e != nil {
		s.configError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"added": added, "results": results, "revision": cfg.Revision})
}
func (s *Server) apiX50PoolProxy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Scope    string   `json:"scope"`
		IDs      []string `json:"ids"`
		ProxyURL *string  `json:"proxyUrl"`
		Confirm  string   `json:"confirm"`
	}
	if e := readBody(r, &in); e != nil {
		apiError(w, 400, e)
		return
	}
	if in.Confirm != "x50" || in.ProxyURL == nil || (in.Scope != "all" && in.Scope != "selected") || in.Scope == "selected" && len(in.IDs) == 0 || len(in.IDs) > 3000 || in.Scope == "all" && len(in.IDs) > 0 {
		apiError(w, 400, errors.New("必须明确确认x50及all/selected作用域与proxyUrl（空字符串=直连）"))
		return
	}
	ids := map[string]bool{}
	for _, id := range in.IDs {
		ids[id] = true
	}
	changed := 0
	_, e := s.cfg.Update(-1, func(c *config.Config) error {
		for id := range ids {
			a, ok := c.Account(id)
			if !ok || a.Provider != config.ProviderX50 {
				return fmt.Errorf("所选账号不存在或不属于50X")
			}
		}
		for i := range c.Accounts {
			a := &c.Accounts[i]
			if a.Provider == config.ProviderX50 && (in.Scope == "all" || ids[a.ID]) {
				a.ProxyURL = *in.ProxyURL
				changed++
			}
		}
		return nil
	})
	if e != nil {
		s.configError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "scope": in.Scope, "updated": changed, "direct": *in.ProxyURL == ""})
}
