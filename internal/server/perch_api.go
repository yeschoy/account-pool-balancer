package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perchimport"
	"account-pool-balancer/internal/perchstore"
	"account-pool-balancer/internal/quota"
)

// registerPerch wires the Perch pool's admin API. Everything here is
// adminOnly (registered by the caller), and no response carries a token.
func (s *Server) registerPerch(mux *http.ServeMux, a func(string, http.HandlerFunc)) {
	a("GET /api/perch/accounts", s.apiPerchAccounts)
	a("GET /api/perch/accounts/{id}", s.apiPerchAccount)
	a("POST /api/perch/accounts/{id}/check", s.apiPerchCheck)
	a("POST /api/perch/accounts/{id}/refresh", s.apiPerchRefresh)
	a("POST /api/perch/accounts/{id}/quota", s.apiPerchSyncQuota)
	a("PUT /api/perch/accounts/{id}/session", s.apiPerchUpdateSession)
	a("PUT /api/perch/accounts/{id}", s.apiPerchUpdateAccount)

	a("POST /api/perch/import/preview", s.apiPerchImportPreview)
	a("POST /api/perch/import", s.apiPerchImport)
	a("POST /api/perch/registrations", s.apiPerchRegister)
	a("GET /api/perch/registrations", s.apiPerchRegistrations)
	a("GET /api/perch/registrations/{id}", s.apiPerchRegistration)
	a("PUT /api/perch/registrations/{id}/proxy", s.apiPerchRegistrationProxy)
	a("POST /api/perch/registrations/{id}/import", s.apiPerchRegistrationImport)
	a("GET /api/perch/autosignup", s.apiPerchAuto)
	a("PUT /api/perch/autosignup/settings", s.apiPerchAutoSettings)
	a("POST /api/perch/autosignup/candidates", s.apiPerchAutoCandidates)
	a("POST /api/perch/autosignup/proxies", s.apiPerchAutoProxies)
	a("DELETE /api/perch/autosignup/proxies/{id}", s.apiPerchAutoDeleteProxy)
	a("DELETE /api/perch/autosignup/candidates/{id}", s.apiPerchAutoDeleteCandidate)
	a("POST /api/perch/autosignup/candidates/{id}/import", s.apiPerchAutoImportCandidate)
	a("POST /api/perch/autosignup/candidates/{id}/retry-save", s.apiPerchAutoRetrySave)
	a("POST /api/perch/autosignup/candidates/{id}/activate", s.apiPerchAutoActivateCandidate)
	a("POST /api/perch/autosignup/run", s.apiPerchAutoRun)

	a("POST /api/perch/batch", s.apiPerchBatch)

	a("GET /api/perch/overview", s.apiPerchOverview)
	a("GET /api/perch/groups", s.apiPerchGroups)
	a("GET /api/perch/available", s.apiPerchAvailable)

	a("GET /api/perch/settings", s.apiPerchSettings)
	a("PUT /api/perch/settings", s.apiPerchSaveSettings)
	a("GET /api/perch/fingerprint", s.apiPerchFingerprint)

	a("GET /api/perch/export", s.apiPerchExport)
	a("POST /api/perch/backup", s.apiPerchBackup)
	a("POST /api/perch/restore", s.apiPerchRestore)
	a("DELETE /api/perch/logs", s.apiPerchClearLogs)
}

// ---- views ---------------------------------------------------------------------

// perchAccountView is one account as the console shows it: identity, status,
// quota, tags — never a token.
type perchAccountView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	UserID    string    `json:"userId"`
	Group     string    `json:"group"`
	Enabled   bool      `json:"enabled"`
	Tags      []string  `json:"tags,omitempty"`
	Note      string    `json:"note,omitempty"`
	Weight    int       `json:"weight"`
	Priority  int       `json:"priority"`
	MaxConc   int       `json:"maxConcurrent"`
	MaxRPM    int       `json:"maxRpm"`
	CreatedAt time.Time `json:"createdAt"`
	HasProxy  bool      `json:"hasProxy"`
	ProxyURL  string    `json:"proxyUrl"`

	// Session is the redacted credential summary.
	Session *perchstore.Summary `json:"session,omitempty"`
	// Status is the console's one-word verdict.
	Status string `json:"status"`
	// StatusDetail explains it (an error text, safe to show).
	StatusDetail string    `json:"statusDetail,omitempty"`
	LastChecked  time.Time `json:"lastCheckedAt,omitzero"`
	// RefreshFailed / LastError come from the account's stored verdict.
	RefreshFailed bool      `json:"refreshFailed"`
	LastError     string    `json:"lastError,omitempty"`
	LastErrorAt   time.Time `json:"lastErrorAt,omitzero"`

	Quota      *quota.PerchMeter `json:"quota,omitempty"`
	QuotaAt    time.Time         `json:"quotaAt,omitzero"`
	QuotaError string            `json:"quotaError,omitempty"`
	QuotaFresh bool              `json:"quotaFresh"`
	// RemainingPT is unknown (and RemainingKnown false) when Perch states no
	// cap — the console prints "未知" rather than a made-up number.
	RemainingPT    int64  `json:"remainingPt"`
	RemainingKnown bool   `json:"remainingKnown"`
	Plan           string `json:"plan,omitempty"`
	// PlanCode is the raw plan code; AllowanceSource says where the cap came
	// from ("entitlement" = Perch stated it, "plan_fallback" = Perch's own
	// plan default, "none" = genuinely unknown).
	PlanCode        string `json:"planCode,omitempty"`
	AllowanceSource string `json:"allowanceSource,omitempty"`
	CreditBalancePT int64  `json:"creditBalancePt,omitempty"`
	RoostUnlimited  bool   `json:"roostUnlimited,omitempty"`
	Unlimited       bool   `json:"unlimited,omitempty"`
	// TierSelectionRequired: the account has not picked a plan, so Perch
	// states no allowance and serves no models. The console prompts for it.
	TierSelectionRequired bool `json:"tierSelectionRequired,omitempty"`

	Runtime runtimeStatus `json:"runtime"`
}

type runtimeStatus struct {
	Status string `json:"status"`
	Active int    `json:"active"`
	RPM    int    `json:"rpm"`
	// Health is the 24 h success rate (nil until there are samples).
	Health *float64 `json:"health"`
}

// perchProxyLabel exposes only the endpoint, not credentials or URL data.
func perchProxyLabel(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[代理已配置]"
	}
	label := u.Scheme + "://"
	if u.User != nil {
		label += "***@"
	}
	return label + u.Host
}

// perchStatusOf names an account's state in the one vocabulary the whole page
// uses: normal, pending (never checked / no session), login-expired, refresh-
// failed, quota-exhausted, rate-limited, service-down, disabled.
func perchStatusOf(a config.Account, sess *perchstore.Summary, met *quota.PerchMeter, metFresh bool, check *perchCheck, runtime string, now time.Time) (string, string) {
	if !a.Enabled {
		return "disabled", "已手动禁用"
	}
	if sess == nil || !sess.HasRefresh {
		return "pending", "没有可续期的 Session，需重新导入或登录"
	}
	if a.PerchRefreshFailed {
		return "login-expired", a.PerchLastError
	}
	if check != nil {
		switch check.Status {
		case "need-login":
			return "login-expired", check.Detail
		case "error":
			if check.Reachable {
				return "refresh-failed", check.Detail
			}
			return "service-down", "网络或 Perch 服务暂时不可用：" + check.Detail
		}
	}
	switch runtime {
	case "quarantined":
		// A quarantine is either a refused session (needs signing in) or a
		// rule the plan tripped (needs an operator's decision). Only the
		// refresh-failure flag can tell them apart.
		if a.PerchRefreshFailed {
			return "login-expired", a.PerchLastError
		}
		return "restricted", a.PerchLastError
	case "disabled":
		return "disabled", "已手动禁用"
	case "no-key":
		return "pending", "没有可续期的 Session"
	case "cooldown":
		return "rate-limited", "处于冷却中"
	case "quota-exhausted":
		return "quota-exhausted", "额度已用尽"
	}
	if metFresh && met != nil && met.Exhausted(now) {
		return "quota-exhausted", "本月额度已用尽"
	}
	// Perch can report tier selection required even when the independent usage
	// RPC has recorded calls; do not infer the account's actual usability.
	if metFresh && met != nil && met.TierSelectionRequired {
		return "tier-pending", "Perch 返回待选套餐且未提供额度；请在 chat.perchai.app 检查该账号的套餐状态"
	}
	if sess.ExpiresAt.IsZero() {
		return "pending", "Access Token 到期时间未知，等待续期"
	}
	if !now.Before(sess.ExpiresAt) {
		return "pending", "Access Token 已过期，等待自动续期"
	}
	return "normal", ""
}

// apiPerchAccounts lists and filters accounts. Every filter is applied here
// (the account pool is small enough for one pass) and pagination is on top, so
// a thousand accounts stay responsive without a second round trip.
func (s *Server) apiPerchAccounts(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	views := s.perchAccountViews(cfg)
	q := r.URL.Query()

	if term := strings.ToLower(strings.TrimSpace(q.Get("q"))); term != "" {
		kept := views[:0]
		for _, v := range views {
			if strings.Contains(strings.ToLower(v.Email), term) ||
				strings.Contains(strings.ToLower(v.UserID), term) ||
				strings.Contains(strings.ToLower(v.Name), term) {
				kept = append(kept, v)
			}
		}
		views = kept
	}
	if st := q.Get("status"); st != "" && st != "all" {
		want := map[string]bool{}
		for _, x := range strings.Split(st, ",") {
			if x = strings.TrimSpace(x); x != "" {
				want[x] = true
			}
		}
		kept := views[:0]
		for _, v := range views {
			if want[v.Status] {
				kept = append(kept, v)
			}
		}
		views = kept
	}
	if g := q.Get("group"); g != "" {
		kept := views[:0]
		for _, v := range views {
			if v.Group == g {
				kept = append(kept, v)
			}
		}
		views = kept
	}
	if tag := q.Get("tag"); tag != "" {
		kept := views[:0]
		for _, v := range views {
			for _, t := range v.Tags {
				if t == tag {
					kept = append(kept, v)
					break
				}
			}
		}
		views = kept
	}
	if plan := q.Get("plan"); plan != "" {
		kept := views[:0]
		for _, v := range views {
			p := v.Plan
			if p == "" {
				p = "unknown"
			}
			if p == plan {
				kept = append(kept, v)
			}
		}
		views = kept
	}
	// quota filters: remaining headroom, as a percentage of the cap when one
	// is known. "unknown" selects accounts Perch states no cap for.
	if rem := q.Get("remaining"); rem != "" {
		kept := views[:0]
		for _, v := range views {
			switch {
			case rem == "unknown":
				if !v.RemainingKnown {
					kept = append(kept, v)
				}
			case rem == "exhausted":
				if v.RemainingKnown && v.RemainingPT == 0 {
					kept = append(kept, v)
				}
			case strings.HasSuffix(rem, "%"):
				n, err := strconv.ParseInt(strings.TrimSuffix(rem, "%"), 10, 64)
				if err != nil {
					continue
				}
				if !v.RemainingKnown {
					continue
				}
				cap := v.Quota.AllowancePT
				if cap > 0 && v.RemainingPT*100/cap >= n {
					kept = append(kept, v)
				}
			}
		}
		views = kept
	}
	if en := q.Get("enabled"); en != "" {
		want := en == "true" || en == "1"
		kept := views[:0]
		for _, v := range views {
			if v.Enabled == want {
				kept = append(kept, v)
			}
		}
		views = kept
	}

	sortPerchViews(views, q.Get("sort"), q.Get("dir"))

	total := len(views)
	page, size := 1, 50
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 0 {
		page = v
	}
	if v, err := strconv.Atoi(q.Get("size")); err == nil && v > 0 {
		size = min(v, 500)
	}
	start := min((page-1)*size, total)
	end := min(start+size, total)
	writeJSON(w, http.StatusOK, map[string]any{
		"revision": cfg.Revision, "total": total, "page": page, "size": size,
		"accounts": views[start:end],
	})
}

// sortPerchViews orders the list. Default: most recently failing first, then
// by email, so the accounts that need attention are on the first page.
func sortPerchViews(views []perchAccountView, field, dir string) {
	if field == "" {
		field = "status"
	}
	less := func(i, j int) bool { return strings.ToLower(views[i].Email) < strings.ToLower(views[j].Email) }
	switch field {
	case "email":
		// default order
	case "status":
		rank := map[string]int{"login-expired": 0, "refresh-failed": 1, "restricted": 2, "service-down": 3,
			"quota-exhausted": 4, "rate-limited": 5, "pending": 6, "normal": 7, "disabled": 8}
		less = func(i, j int) bool {
			ri, rj := rank[views[i].Status], rank[views[j].Status]
			if ri != rj {
				return ri < rj
			}
			return strings.ToLower(views[i].Email) < strings.ToLower(views[j].Email)
		}
	case "expires":
		less = func(i, j int) bool {
			var ei, ej time.Time
			if views[i].Session != nil {
				ei = views[i].Session.ExpiresAt
			}
			if views[j].Session != nil {
				ej = views[j].Session.ExpiresAt
			}
			return ei.Before(ej)
		}
	case "checked":
		less = func(i, j int) bool { return views[i].LastChecked.Before(views[j].LastChecked) }
	case "quota":
		less = func(i, j int) bool {
			// unknown headroom sorts last either way: it is neither full
			// nor empty, and pretending otherwise would mislead
			if views[i].RemainingKnown != views[j].RemainingKnown {
				return views[i].RemainingKnown
			}
			return views[i].RemainingPT < views[j].RemainingPT
		}
	case "created":
		less = func(i, j int) bool { return views[i].CreatedAt.Before(views[j].CreatedAt) }
	case "plan":
		less = func(i, j int) bool { return views[i].Plan < views[j].Plan }
	}
	desc := dir == "desc"
	if desc {
		asc := less
		less = func(i, j int) bool { return asc(j, i) }
	}
	sort.SliceStable(views, less)
}

// perchAccountViews builds the console's list for every Perch account.
func (s *Server) perchAccountViews(cfg *config.Config) []perchAccountView {
	views := s.pool.Views(cfg)
	snaps := s.quota.All()
	now := time.Now()
	stale := time.Duration(cfg.Quota.StaleSec) * time.Second
	out := make([]perchAccountView, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		if a.Provider != config.ProviderPerch {
			continue
		}
		v := perchAccountView{
			ID: a.ID, Name: a.Name, Email: a.Email, UserID: a.PerchUserID, Group: a.Group,
			Enabled: a.Enabled, Note: a.Note, Weight: a.Weight, Priority: a.Priority,
			MaxConc: a.MaxConc, MaxRPM: a.MaxRPM, CreatedAt: a.CreatedAt,
			HasProxy: a.ProxyURL != "", ProxyURL: perchProxyLabel(a.ProxyURL),
			Tags:          append([]string(nil), a.PerchTags...),
			RefreshFailed: a.PerchRefreshFailed, LastError: a.PerchLastError, LastErrorAt: a.PerchLastErrorAt,
		}
		var summ *perchstore.Summary
		if s.perch != nil {
			if sec, ok := s.perch.keeper.Get(a.ID); ok {
				sm := perchstore.Summarize(sec)
				summ = &sm
				v.Session = summ
			}
		}
		var met *quota.PerchMeter
		var metFresh bool
		var quotaAt time.Time
		var quotaErr string
		if snap := snaps[a.ID]; snap != nil {
			met = snap.Perch
			quotaAt = snap.PerchAt
			quotaErr = snap.PerchError
			metFresh = snap.FreshPerch(now, stale)
			if met != nil {
				v.Quota = met
				v.Plan = met.Plan
				v.PlanCode, v.AllowanceSource = met.PlanCode, met.AllowanceSource
				v.CreditBalancePT, v.RoostUnlimited, v.Unlimited = met.CreditBalancePT, met.RoostUnlimited, met.Unlimited
				v.TierSelectionRequired = met.TierSelectionRequired
				if rem, known := met.RemainingPT(); known {
					v.RemainingPT, v.RemainingKnown = rem, true
				}
				if v.Plan == "" {
					v.Plan = a.PerchPlan
				}
				if !v.RemainingKnown && a.PerchQuotaPT > 0 {
					v.RemainingPT, v.RemainingKnown = a.PerchQuotaPT, true
				}
			}
		}
		v.QuotaAt, v.QuotaError, v.QuotaFresh = quotaAt, quotaErr, metFresh
		var check *perchCheck
		if s.perch != nil {
			s.perch.mu.Lock()
			if c, ok := s.perch.checks[a.ID]; ok {
				cc := c
				check = &cc
			}
			s.perch.mu.Unlock()
		}
		if check != nil {
			v.LastChecked = check.At
		}
		runtime := ""
		if rv, ok := views[a.ID]; ok {
			runtime = rv.Status
			v.Runtime = runtimeStatus{Status: rv.Status, Active: rv.Active, RPM: rv.RPM, Health: rv.Health}
			// the pool's own verdict is the most recent evidence there is
			if check == nil && rv.State != nil {
				check = &perchCheck{At: rv.State.UpdatedAt, Status: "runtime", Detail: rv.State.Reason, Reachable: true}
			}
			if rv.State != nil && rv.State.HardQuarantined && check != nil && check.Status == "runtime" {
				// the quarantine rule recorded it; the reason is the verdict
				check.Status = "quarantined"
			}
		}
		v.Status, v.StatusDetail = perchStatusOf(a, summ, met, metFresh, check, runtime, now)
		v.StatusDetail = scrub(v.StatusDetail)
		out = append(out, v)
	}
	return out
}

// apiPerchAccount returns one account and its recent events.
func (s *Server) apiPerchAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := s.cfg.Get()
	a, ok := cfg.Account(id)
	if !ok || a.Provider != config.ProviderPerch {
		apiError(w, http.StatusNotFound, errors.New("perch account not found"))
		return
	}
	var view *perchAccountView
	for _, v := range s.perchAccountViews(cfg) {
		if v.ID == id {
			vv := v
			view = &vv
			break
		}
	}
	out := map[string]any{"account": view}
	if s.logs != nil {
		if page, err := s.logs.Query("perch", map[string]string{"accountId": id}, time.Time{}, time.Time{}, 50, 0); err == nil {
			out["events"] = page
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// apiPerchUpdateAccount edits the fields an operator may change.
func (s *Server) apiPerchUpdateAccount(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Name     *string   `json:"name"`
		Group    *string   `json:"group"`
		Enabled  *bool     `json:"enabled"`
		Tags     *[]string `json:"tags"`
		Note     *string   `json:"note"`
		Weight   *int      `json:"weight"`
		Priority *int      `json:"priority"`
		ProxyURL *string   `json:"proxyUrl"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	// Omitted or null keeps the current proxy; an empty string selects direct.
	if in.ProxyURL != nil {
		proxy := strings.TrimSpace(*in.ProxyURL)
		if err := config.ValidateProxyURL(proxy); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		in.ProxyURL = &proxy
	}
	cfg, err := s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			a := &c.Accounts[i]
			if a.ID != id || a.Provider != config.ProviderPerch {
				continue
			}
			if in.Name != nil {
				a.Name = strings.TrimSpace(*in.Name)
			}
			if in.Group != nil {
				g := strings.TrimSpace(*in.Group)
				if g == "" {
					g = config.GroupForProvider(config.ProviderPerch)
				}
				a.Group = g
			}
			if in.Enabled != nil {
				a.Enabled = *in.Enabled
			}
			if in.Tags != nil {
				a.PerchTags = append([]string(nil), (*in.Tags)...)
			}
			if in.Note != nil {
				a.Note = strings.TrimSpace(*in.Note)
			}
			if in.Weight != nil {
				a.Weight = *in.Weight
			}
			if in.Priority != nil {
				a.Priority = *in.Priority
			}
			if in.ProxyURL != nil {
				a.ProxyURL = *in.ProxyURL
			}
			return nil
		}
		return errors.New("perch account not found")
	})
	if err != nil {
		s.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": cfg.Revision})
}

// ---- session actions ---------------------------------------------------------------

// apiPerchCheck checks one account now.
func (s *Server) apiPerchCheck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, ok := s.cfg.Get().Account(id)
	if !ok || a.Provider != config.ProviderPerch {
		apiError(w, http.StatusNotFound, errors.New("perch account not found"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	c := s.checkPerchAccount(ctx, a)
	s.recordCheck(id, c)
	s.applyCheck(a, c)
	s.logPerchOutcome(id, "check", c.Status, "", c.Detail)
	writeJSON(w, http.StatusOK, c)
}

// apiPerchRefresh renews one account's session now.
func (s *Server) apiPerchRefresh(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.cfg.Get().Account(id); !ok {
		apiError(w, http.StatusNotFound, errors.New("perch account not found"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	res := s.perch.keeper.Refresh(ctx, id, true)
	s.onPerchRefresh(id, res)
	s.logPerchOutcome(id, "refresh", refreshOutcome(res), "", errText(res.Err))
	writeJSON(w, http.StatusOK, map[string]any{
		"renewed": res.Renewed, "skipped": res.Skipped, "needLogin": res.NeedLogin,
		"error": errText(res.Err), "session": perchstore.Summarize(res.Secrets),
	})
}

func refreshOutcome(res perchstore.RefreshResult) string {
	switch {
	case res.NeedLogin:
		return "need-login"
	case res.Err != nil:
		return "failed"
	case res.Skipped:
		return "skipped"
	default:
		return "renewed"
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// apiPerchUpdateSession replaces an account's session with pasted tokens (the
// manual recovery path when automatic renewal has run out).
func (s *Server) apiPerchUpdateSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, ok := s.cfg.Get().Account(id)
	if !ok || a.Provider != config.ProviderPerch {
		apiError(w, http.StatusNotFound, errors.New("perch account not found"))
		return
	}
	var in struct {
		// AccessToken and RefreshToken are the pair; a JSON export may be
		// pasted whole instead (JSON below).
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
		// JSON is a complete export document (the import format).
		JSON string `json:"json"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	sec := perchstore.Secrets{UserID: a.PerchUserID, Email: a.Email, SupabaseURL: "", FetchedAt: time.Now().UTC()}
	if strings.TrimSpace(in.JSON) != "" {
		doc, err := perchimport.ParseJSON([]byte(in.JSON))
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		if doc.UserID != a.PerchUserID {
			apiError(w, http.StatusBadRequest, fmt.Errorf("这份 JSON 属于 Supabase 用户 %s，不是本账号的 %s", doc.UserID, a.PerchUserID))
			return
		}
		sec = doc.Secrets()
		sec.FetchedAt = time.Now().UTC()
	} else {
		sec.AccessToken = strings.TrimSpace(in.AccessToken)
		sec.RefreshToken = strings.TrimSpace(in.RefreshToken)
		sec.ExpiresAt = in.ExpiresAt
	}
	if strings.TrimSpace(sec.AccessToken) == "" || strings.TrimSpace(sec.RefreshToken) == "" {
		apiError(w, http.StatusBadRequest, errors.New("需要 accessToken + refreshToken，或一份完整的 JSON"))
		return
	}
	res, err := s.perch.keeper.Put(sec, perchstore.PutOptions{
		BindAs: id, FetchedAt: time.Now().UTC(), Force: true,
	})
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, perchstore.ErrSessionIdentity) {
			status = http.StatusBadRequest
		}
		apiError(w, status, err)
		return
	}
	s.afterPerchSession(id)
	s.logPerchOutcome(id, "session-updated", "updated", "", "管理员手动更新了 Session")
	writeJSON(w, http.StatusOK, map[string]any{"session": perchstore.Summarize(res.Secrets)})
}

// apiPerchSyncQuota reads one account's usage meter now.
func (s *Server) apiPerchSyncQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, ok := s.cfg.Get().Account(id)
	if !ok || a.Provider != config.ProviderPerch {
		apiError(w, http.StatusNotFound, errors.New("perch account not found"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	err := s.quota.RefreshNow(ctx, a)
	s.logPerchOutcome(id, "quota", outcomeOf(err), "", errText(err))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": scrubbedErr(err)})
		return
	}
	var met *quota.PerchMeter
	var at time.Time
	var qerr string
	for _, v := range s.perchAccountViews(s.cfg.Get()) {
		if v.ID == id {
			met, at, qerr = v.Quota, v.QuotaAt, v.QuotaError
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "quota": met, "quotaAt": at, "quotaError": qerr})
}

func outcomeOf(err error) string {
	if err != nil {
		return "failed"
	}
	return "ok"
}

func scrubbedErr(err error) string {
	if err == nil {
		return ""
	}
	return scrub(err.Error())
}

// ---- import ------------------------------------------------------------------------

// readPerchUpload reads the multipart body: any number of files under any
// field name, each kept under its own name. A drag-and-drop posts one, a
// picker several, and a ZIP arrives as one file among them.
func readPerchUpload(w http.ResponseWriter, r *http.Request) (map[string][]byte, error) {
	const maxTotal = 64 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxTotal)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		return nil, fmt.Errorf("读取上传失败：%v", err)
	}
	files := map[string][]byte{}
	for _, fhs := range r.MultipartForm.File {
		for _, fh := range fhs {
			name := saneUploadName(fh)
			body, err := readUploaded(fh)
			if err != nil {
				return nil, fmt.Errorf("%s: %v", name, err)
			}
			// two files with one name must not silently replace each other
			key, n := name, 2
			for {
				if _, taken := files[key]; !taken {
					break
				}
				key = fmt.Sprintf("%s (%d)", name, n)
				n++
			}
			files[key] = body
		}
	}
	if len(files) == 0 {
		return nil, errors.New("没有收到文件")
	}
	return files, nil
}

func saneUploadName(fh *multipart.FileHeader) string {
	name := strings.TrimSpace(fh.Filename)
	if name == "" {
		return "upload.json"
	}
	name = strings.ReplaceAll(name, "\\", "/")
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	if len([]rune(name)) > 200 {
		name = string([]rune(name)[:200])
	}
	return name
}

func readUploaded(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 32<<20))
}

// apiPerchImportPreview validates an upload and reports what importing it
// would do, without writing anything.
func (s *Server) apiPerchImportPreview(w http.ResponseWriter, r *http.Request) {
	files, err := readPerchUpload(w, r)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	res := perchimport.ReadFiles(files)
	stored := func(uid string) bool {
		if s.perch == nil {
			return false
		}
		_, ok := s.perch.keeper.ByUserID(uid)
		return ok
	}
	items := perchimport.Preview(res, stored)
	inPool := map[string]string{}
	for _, a := range s.cfg.Get().Accounts {
		if a.PerchUserID != "" {
			inPool[a.PerchUserID] = a.ID
		}
	}
	for i := range items {
		if id, ok := inPool[items[i].UserID]; ok {
			items[i].AccountID = id
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"files": res.Files, "parsed": res.Parsed, "failed": len(res.Failed), "items": items,
	})
}

// apiPerchImport applies an upload. ?preview=1 only reports.
func (s *Server) apiPerchImport(w http.ResponseWriter, r *http.Request) {
	files, err := readPerchUpload(w, r)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	group := strings.TrimSpace(r.URL.Query().Get("group"))
	if group == "" {
		group = config.GroupForProvider(config.ProviderPerch)
	}
	res := perchimport.ReadFiles(files)
	sum, _ := s.importPerch(res, group, nil)
	batch := config.NewID("imp_")
	s.logPerchBatch(batch, sum, fmt.Sprintf("%d 个文件，创建 %d、更新 %d、重复 %d、跳过 %d、失败 %d",
		res.Files, sum.Created, sum.Updated, sum.Duplicate, sum.Skipped, sum.Failed))
	writeJSON(w, http.StatusOK, map[string]any{
		"batch": batch, "files": res.Files + len(res.Failed), "summary": sum,
		"parseFailed": res.Failed,
	})
}

// ---- batch ---------------------------------------------------------------------------

// apiPerchBatch runs one bulk action. Each account is handled on its own, so
// a failure is recorded for that account and the rest continue.
func (s *Server) apiPerchBatch(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string   `json:"action"`
		IDs    []string `json:"ids"`
		// Filter selects by status instead of naming ids ("abnormal" =
		// everything that is not normal/disabled).
		Filter string   `json:"filter"`
		Group  *string  `json:"group"`
		Tags   []string `json:"tags"`
		// Session is used by update-session.
		Session *struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"session"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	cfg := s.cfg.Get()
	views := s.perchAccountViews(cfg)
	byID := map[string]perchAccountView{}
	for _, v := range views {
		byID[v.ID] = v
	}
	targets := in.IDs
	if in.Filter != "" {
		targets = nil
		for _, v := range views {
			if batchFilterMatch(v, in.Filter) {
				targets = append(targets, v.ID)
			}
		}
	}
	if len(targets) == 0 {
		apiError(w, http.StatusBadRequest, errors.New("没有匹配的账号"))
		return
	}
	results := make([]perchBatchResult, 0, len(targets))
	switch in.Action {
	case "enable", "disable":
		s.batchSetEnabled(targets, in.Action == "enable", &results)
	case "delete":
		s.batchDeletePerch(targets, &results)
	case "check":
		s.batchCheck(targets, r, &results)
	case "refresh":
		s.batchRefresh(targets, r, &results)
	case "quota":
		s.batchQuota(targets, r, &results)
	case "group":
		if in.Group == nil {
			apiError(w, http.StatusBadRequest, errors.New("group is required"))
			return
		}
		s.batchSetGroup(targets, strings.TrimSpace(*in.Group), &results)
	case "tags":
		s.batchSetTags(targets, in.Tags, &results)
	case "session":
		s.batchSetSession(targets, in.Session, &results)
	default:
		apiError(w, http.StatusBadRequest, fmt.Errorf("unknown action %q", in.Action))
		return
	}
	okN, failN := 0, 0
	for _, res := range results {
		if res.OK {
			okN++
		} else {
			failN++
		}
	}
	s.logPerchOutcome("", "batch", fmt.Sprintf("ok=%d fail=%d", okN, failN), in.Action,
		fmt.Sprintf("批量 %s：成功 %d，失败 %d", in.Action, okN, failN))
	writeJSON(w, http.StatusOK, map[string]any{"action": in.Action, "total": len(results),
		"ok": okN, "failed": failN, "results": results})
}

type perchBatchResult struct {
	ID     string `json:"id"`
	Email  string `json:"email,omitempty"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// batchFilterMatch selects accounts by the console's status words.
func batchFilterMatch(v perchAccountView, filter string) bool {
	switch filter {
	case "all":
		return true
	case "abnormal":
		return v.Status != "normal" && v.Status != "disabled"
	case "due":
		return v.Session != nil && !v.Session.ExpiresAt.IsZero() && time.Now().After(v.Session.ExpiresAt.Add(-10*time.Minute))
	case "login-expired":
		return v.Status == "login-expired"
	case "restricted":
		return v.Status == "restricted"
	case "refresh-failed":
		return v.Status == "refresh-failed" || v.RefreshFailed
	case "quota-exhausted":
		return v.Status == "quota-exhausted"
	case "rate-limited":
		return v.Status == "rate-limited"
	case "service-down":
		return v.Status == "service-down"
	case "pending":
		return v.Status == "pending"
	case "disabled":
		return v.Status == "disabled"
	case "normal":
		return v.Status == "normal"
	}
	return false
}

func (s *Server) batchSetEnabled(ids []string, enabled bool, out *[]perchBatchResult) {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	_, err := s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			a := &c.Accounts[i]
			if a.Provider == config.ProviderPerch && set[a.ID] {
				a.Enabled = enabled
			}
		}
		return nil
	})
	for _, id := range ids {
		r := perchBatchResult{ID: id, OK: err == nil, Detail: errText(err)}
		if v, ok := s.perchViewOf(id); ok {
			r.Email = v.Email
		}
		// a batch task never fails as a whole because one account did
		*out = append(*out, r)
	}
	if err != nil {
		return
	}
	for _, id := range ids {
		if enabled {
			s.pool.Recover(id)
		} else {
			s.pool.Invalidate(id, false)
		}
	}
}

func (s *Server) perchViewOf(id string) (perchAccountView, bool) {
	for _, v := range s.perchAccountViews(s.cfg.Get()) {
		if v.ID == id {
			return v, true
		}
	}
	return perchAccountView{}, false
}

func (s *Server) batchDeletePerch(ids []string, out *[]perchBatchResult) {
	for _, id := range ids {
		email := ""
		if v, ok := s.perchViewOf(id); ok {
			email = v.Email
		}
		_, err := s.cfg.Update(-1, func(c *config.Config) error {
			for i := range c.Accounts {
				a := c.Accounts[i]
				if a.ID != id || a.Provider != config.ProviderPerch {
					continue
				}
				c.Accounts = append(c.Accounts[:i], c.Accounts[i+1:]...)
				return nil
			}
			return errors.New("account not found")
		})
		if err != nil {
			*out = append(*out, perchBatchResult{ID: id, Email: email, Detail: err.Error()})
			continue
		}
		s.pool.Invalidate(id, true)
		s.quota.Forget(id)
		s.stats.Forget(id)
		if s.perch != nil {
			s.perch.keeper.Forget(id)
		}
		if s.logs != nil {
			// drop the cached verdict with the account
			s.logs.Append("perch", time.Now(), &perchLogRecord{TS: time.Now(), Provider: config.ProviderPerch,
				AccountID: id, Email: email, Event: "deleted", Detail: "账号已删除"})
		}
		if s.perch != nil {
			s.perch.mu.Lock()
			delete(s.perch.checks, id)
			s.perch.mu.Unlock()
		}
		*out = append(*out, perchBatchResult{ID: id, Email: email, OK: true})
	}
}

func (s *Server) batchCheck(ids []string, r *http.Request, out *[]perchBatchResult) {
	cfg := s.cfg.Get()
	var accts []config.Account
	for _, id := range ids {
		if a, ok := cfg.Account(id); ok && a.Provider == config.ProviderPerch {
			accts = append(accts, a)
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	byID := map[string]config.Account{}
	for _, a := range accts {
		byID[a.ID] = a
	}
	s.checkPerchBatch(ctx, accts, func(id string, c perchCheck) {
		*out = append(*out, perchBatchResult{ID: id, Email: byID[id].Email, OK: c.Status == "ok",
			Detail: c.Status + "：" + c.Detail})
		s.logPerchOutcome(id, "check", c.Status, "", c.Detail)
	})
}

func (s *Server) batchRefresh(ids []string, r *http.Request, out *[]perchBatchResult) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	byID := map[string]config.Account{}
	for _, a := range s.cfg.Get().Accounts {
		byID[a.ID] = a
	}
	var wg sync.WaitGroup
	limit := max(s.cfg.Get().Perch.RefreshConcurrency, 1)
	sem := make(chan struct{}, limit)
	var mu sync.Mutex
	for _, id := range ids {
		id := id
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res := s.perch.keeper.Refresh(ctx, id, false)
			s.onPerchRefresh(id, res)
			s.logPerchOutcome(id, "refresh", refreshOutcome(res), "", errText(res.Err))
			mu.Lock()
			*out = append(*out, perchBatchResult{ID: id, Email: byID[id].Email,
				OK: res.Err == nil && !res.NeedLogin, Detail: refreshOutcome(res) + "：" + errText(res.Err)})
			mu.Unlock()
		}()
	}
	wg.Wait()
}

func (s *Server) batchQuota(ids []string, r *http.Request, out *[]perchBatchResult) {
	cfg := s.cfg.Get()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	byID := map[string]config.Account{}
	for _, a := range cfg.Accounts {
		byID[a.ID] = a
	}
	limit := max(cfg.Perch.CheckConcurrency, 1)
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for _, id := range ids {
		a, ok := byID[id]
		if !ok || a.Provider != config.ProviderPerch {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			qerr := s.quota.RefreshNow(ctx, a)
			s.logPerchOutcome(id, "quota", outcomeOf(qerr), "", errText(qerr))
			mu.Lock()
			*out = append(*out, perchBatchResult{ID: id, Email: a.Email, OK: qerr == nil, Detail: scrubbedErr(qerr)})
			mu.Unlock()
		}()
	}
	wg.Wait()
}

func (s *Server) batchSetGroup(ids []string, group string, out *[]perchBatchResult) {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	_, err := s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			a := &c.Accounts[i]
			if a.Provider == config.ProviderPerch && set[a.ID] {
				a.Group = group
			}
		}
		return nil
	})
	for _, id := range ids {
		r := perchBatchResult{ID: id, OK: err == nil, Detail: errText(err)}
		if v, ok := s.perchViewOf(id); ok {
			r.Email = v.Email
		}
		*out = append(*out, r)
	}
}

func (s *Server) batchSetTags(ids []string, tags []string, out *[]perchBatchResult) {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	clean := make([]string, 0, len(tags))
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" {
			clean = append(clean, t)
		}
	}
	_, err := s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			a := &c.Accounts[i]
			if a.Provider == config.ProviderPerch && set[a.ID] {
				a.PerchTags = append([]string(nil), clean...)
			}
		}
		return nil
	})
	for _, id := range ids {
		r := perchBatchResult{ID: id, OK: err == nil, Detail: errText(err)}
		if v, ok := s.perchViewOf(id); ok {
			r.Email = v.Email
		}
		*out = append(*out, r)
	}
}

func (s *Server) batchSetSession(ids []string, sess *struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}, out *[]perchBatchResult) {
	if sess == nil || strings.TrimSpace(sess.AccessToken) == "" || strings.TrimSpace(sess.RefreshToken) == "" {
		for _, id := range ids {
			*out = append(*out, perchBatchResult{ID: id, Detail: "accessToken and refreshToken are required"})
		}
		return
	}
	unique := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		unique[id] = struct{}{}
	}
	if len(unique) > 1 {
		for _, id := range ids {
			*out = append(*out, perchBatchResult{ID: id, Detail: "session update requires a single account"})
		}
		return
	}
	for _, id := range ids {
		a, ok := s.cfg.Get().Account(id)
		if !ok || a.Provider != config.ProviderPerch {
			*out = append(*out, perchBatchResult{ID: id, Detail: "account not found"})
			continue
		}
		_, err := s.perch.keeper.Put(perchstore.Secrets{
			UserID: a.PerchUserID, Email: a.Email, AccessToken: strings.TrimSpace(sess.AccessToken),
			RefreshToken: strings.TrimSpace(sess.RefreshToken), FetchedAt: time.Now().UTC(),
		}, perchstore.PutOptions{BindAs: id, Force: true, FetchedAt: time.Now().UTC()})
		if err != nil {
			*out = append(*out, perchBatchResult{ID: id, Email: a.Email, Detail: err.Error()})
			continue
		}
		s.afterPerchSession(id)
		*out = append(*out, perchBatchResult{ID: id, Email: a.Email, OK: true})
	}
}

// ---- overview / groups / available ------------------------------------------------------

// apiPerchOverview is the dashboard.
func (s *Server) apiPerchOverview(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	views := s.perchAccountViews(cfg)
	counts := map[string]int{}
	plans := map[string]int{}
	groups := map[string]int{}
	tags := map[string]int{}
	var totalQuota, totalRemaining int64
	remainingKnown := 0
	recentImport := 0
	cut := time.Now().Add(-24 * time.Hour)
	for _, v := range views {
		counts[v.Status]++
		plan := v.Plan
		if plan == "" {
			plan = "未知"
		}
		plans[plan]++
		groups[v.Group]++
		for _, t := range v.Tags {
			tags[t]++
		}
		if v.CreatedAt.After(cut) {
			recentImport++
		}
		if v.RemainingKnown {
			remainingKnown++
			totalRemaining += v.RemainingPT
			if v.Quota != nil {
				totalQuota += v.Quota.AllowancePT
			}
		}
	}
	stats := perchstore.Stats{}
	if s.perch != nil {
		stats = s.perch.keeper.Stats()
	}
	// the renewal success rate over the recent events
	var okN, failN int
	renewRate, rateSamples := 0.0, 0
	if s.logs != nil {
		if page, err := s.logs.Query("perch", map[string]string{"event": "refresh"}, time.Now().Add(-7*24*time.Hour), time.Time{}, 200, 0); err == nil {
			for _, raw := range page.Items {
				var m map[string]any
				if json.Unmarshal(raw, &m) != nil {
					continue
				}
				switch m["outcome"] {
				case "renewed":
					okN++
				case "failed", "need-login":
					failN++
				}
			}
			if okN+failN > 0 {
				renewRate = float64(okN) * 100 / float64(okN+failN)
				rateSamples = okN + failN
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": len(views), "counts": counts,
		"abnormal": len(views) - counts["normal"] - counts["disabled"],
		"plans":    plans, "groups": groups, "tags": tags,
		"recentImports": recentImport, "recentSession": stats,
		"quota":       map[string]any{"totalPt": totalQuota, "remainingPt": totalRemaining, "known": remainingKnown},
		"refreshRate": map[string]any{"percent": renewRate, "samples": rateSamples},
		"trend":       s.perchTrend(),
		"alerting":    cfg.Perch.AlertOnFailure && (counts["login-expired"]+counts["refresh-failed"]) > 0,
	})
}

// perchTrend is the last days' status mix, oldest first.
func (s *Server) perchTrend() []perchTrend {
	if s.perch == nil {
		return nil
	}
	s.perch.mu.Lock()
	defer s.perch.mu.Unlock()
	out := make([]perchTrend, len(s.perch.history))
	copy(out, s.perch.history)
	return out
}

// apiPerchGroups summarises the groups.
func (s *Server) apiPerchGroups(w http.ResponseWriter, r *http.Request) {
	views := s.perchAccountViews(s.cfg.Get())
	type groupInfo struct {
		Name    string `json:"name"`
		Total   int    `json:"total"`
		Normal  int    `json:"normal"`
		Enabled int    `json:"enabled"`
	}
	byName := map[string]*groupInfo{}
	order := []string{}
	for _, v := range views {
		g := byName[v.Group]
		if g == nil {
			g = &groupInfo{Name: v.Group}
			byName[v.Group] = g
			order = append(order, v.Group)
		}
		g.Total++
		if v.Status == "normal" {
			g.Normal++
		}
		if v.Enabled {
			g.Enabled++
		}
	}
	sort.Strings(order)
	out := make([]groupInfo, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}

// apiPerchAvailable lists the accounts a caller could legitimately use: the
// ones that are enabled, not quarantined, and whose session is alive. It is a
// picker for an authorised caller — a manual selection, never a rotation to
// dodge a plan's limits.
func (s *Server) apiPerchAvailable(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	views := s.perchAccountViews(cfg)
	q := r.URL.Query()
	group := q.Get("group")
	plan := q.Get("plan")
	minRemain := q.Get("minRemaining")
	var minPT int64
	if minRemain != "" {
		minPT, _ = strconv.ParseInt(minRemain, 10, 64)
	}
	type avail struct {
		ID             string `json:"id"`
		Email          string `json:"email"`
		UserID         string `json:"userId"`
		Group          string `json:"group"`
		Plan           string `json:"plan,omitempty"`
		Status         string `json:"status"`
		RemainingPT    int64  `json:"remainingPt"`
		RemainingKnown bool   `json:"remainingKnown"`
	}
	out := []avail{}
	for _, v := range views {
		// the only accounts offered are the ones genuinely usable: a
		// disabled or failed account is never silently included
		if v.Status != "normal" && v.Status != "quota-exhausted" {
			continue
		}
		if v.Status != "normal" {
			continue
		}
		if group != "" && v.Group != group {
			continue
		}
		if plan != "" && v.Plan != plan {
			continue
		}
		if minPT > 0 && (!v.RemainingKnown || v.RemainingPT < minPT) {
			continue
		}
		out = append(out, avail{ID: v.ID, Email: v.Email, UserID: v.UserID, Group: v.Group, Plan: v.Plan,
			Status: v.Status, RemainingPT: v.RemainingPT, RemainingKnown: v.RemainingKnown})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

// ---- settings -------------------------------------------------------------------------

func (s *Server) apiPerchSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	p := cfg.Perch
	writeJSON(w, http.StatusOK, map[string]any{
		"perch":      p,
		"hasKey":     strings.TrimSpace(p.SupabaseKey) != "",
		"maintained": s.perch != nil,
		"store":      s.perchStats(),
	})
}

func (s *Server) perchStats() perchstore.Stats {
	if s.perch == nil {
		return perchstore.Stats{}
	}
	return s.perch.keeper.Stats()
}

func (s *Server) apiPerchSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Perch            *config.Perch `json:"perch"`
		ExpectedRevision *int64        `json:"expectedRevision"`
		ClearSupabaseKey bool          `json:"clearSupabaseKey"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if in.Perch == nil {
		apiError(w, http.StatusBadRequest, errors.New("perch is required"))
		return
	}
	next := *in.Perch
	rev := int64(-1)
	if in.ExpectedRevision != nil {
		rev = *in.ExpectedRevision
	}
	cfg, err := s.cfg.Update(rev, func(c *config.Config) error {
		if next.SupabaseKey == "" && !in.ClearSupabaseKey {
			next.SupabaseKey = c.Perch.SupabaseKey
		}
		c.Perch = next
		return nil
	})
	if err != nil {
		s.configError(w, err)
		return
	}
	if s.perch != nil {
		s.perch.sched.Concurrency = cfg.Perch.RefreshConcurrency
		s.perch.sched.Tick = time.Duration(cfg.Perch.RefreshTickSec) * time.Second
		s.perch.sched.Wake()
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": cfg.Revision, "perch": cfg.Perch})
}

// ---- export / backup / restore ------------------------------------------------------------

// apiPerchExport exports account metadata with every secret removed: ids,
// emails, groups, tags and quota — the shape a spreadsheet or a handover
// wants, and useless as a credential.
func (s *Server) apiPerchExport(w http.ResponseWriter, r *http.Request) {
	views := s.perchAccountViews(s.cfg.Get())
	out := make([]map[string]any, 0, len(views))
	for _, v := range views {
		row := map[string]any{"userId": v.UserID, "email": v.Email, "name": v.Name,
			"group": v.Group, "enabled": v.Enabled, "tags": v.Tags, "status": v.Status,
			"createdAt": v.CreatedAt, "plan": v.Plan}
		if v.Session != nil {
			row["expiresAt"] = v.Session.ExpiresAt
			row["lastFetchedAt"] = v.Session.FetchedAt
		}
		if v.RemainingKnown {
			row["remainingPt"] = v.RemainingPT
		}
		if v.Quota != nil {
			row["usedPt"] = v.Quota.UsedPT
			row["monthlyCalls"] = v.Quota.MonthlyCalls
			row["monthlyTokens"] = v.Quota.MonthlyTokens
		}
		out = append(out, row)
	}
	body := map[string]any{"schema_version": "1.0", "provider": "perchai", "exported_at": time.Now(),
		"note": "脱敏导出：不含任何 Token、Refresh Token 或 Supabase Key", "accounts": out}
	w.Header().Set("Content-Disposition", `attachment; filename="perch-accounts-redacted.json"`)
	writeJSON(w, http.StatusOK, body)
}

// apiPerchBackup writes an encrypted backup of the sessions. The passphrase
// the caller supplies is the only way back in: the keeper's own key is not in
// the file.
func (s *Server) apiPerchBackup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Passphrase string `json:"passphrase"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if len(in.Passphrase) < 8 {
		apiError(w, http.StatusBadRequest, errors.New("备份口令至少 8 位"))
		return
	}
	blob, err := s.backupPerch(in.Passphrase)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	s.logPerch("", "backup", fmt.Sprintf("导出了 %d 个账号的加密备份", s.perchStats().Stored))
	w.Header().Set("Content-Disposition", `attachment; filename="perch-backup.json"`)
	writeJSON(w, http.StatusOK, blob)
}

// apiPerchRestore restores an encrypted backup.
func (s *Server) apiPerchRestore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Passphrase string          `json:"passphrase"`
		Backup     json.RawMessage `json:"backup"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if len(in.Passphrase) < 8 || len(in.Backup) == 0 {
		apiError(w, http.StatusBadRequest, errors.New("需要备份文件和口令"))
		return
	}
	created, updated, skipped, errs, err := s.restorePerch(in.Passphrase, in.Backup)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	s.logPerch("", "restore", fmt.Sprintf("恢复：新建 %d、更新 %d、跳过 %d、失败 %d", created, updated, skipped, len(errs)))
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "updated": updated,
		"skipped": skipped, "failed": len(errs), "errors": errs})
}

// apiPerchClearLogs clears the Perch event log, keeping the retention window
// in mind: "before" is an optional cutoff.
func (s *Server) apiPerchClearLogs(w http.ResponseWriter, r *http.Request) {
	before := r.URL.Query().Get("before")
	if before == "" {
		if err := s.logs.Clear("perch"); err != nil {
			apiError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cleared": "all"})
		return
	}
	n, err := s.prunePerchLogs(before)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": "before", "at": before, "removed": n})
}
