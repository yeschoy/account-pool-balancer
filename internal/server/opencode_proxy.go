package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/egressid"
	"account-pool-balancer/internal/opencode"
)

type proxyHealthView struct {
	Status        string    `json:"status"`
	Failures      int       `json:"failures"`
	CooldownUntil time.Time `json:"cooldownUntil,omitzero"`
	LastFailureAt time.Time `json:"lastFailureAt,omitzero"`
	LastOKAt      time.Time `json:"lastOkAt,omitzero"`
	Reason        string    `json:"reason,omitempty"`
}

func (s *Server) openCodeProxyView(proxy string) proxyHealthView {
	if proxy == "" {
		return proxyHealthView{Status: "direct"}
	}
	s.proxyHealth.Lock()
	v, ok := s.proxyHealth.states[config.HashKey(proxy)]
	s.proxyHealth.Unlock()
	if !ok {
		return proxyHealthView{Status: "unknown"}
	}
	if v.Status == "unhealthy" && !time.Now().Before(v.CooldownUntil) {
		v.Status = "probe-due"
	}
	return v
}
func (s *Server) observeOpenCodeProxy(proxy string, failed bool) {
	if proxy == "" {
		return
	}
	key := config.HashKey(proxy)
	now := time.Now()
	s.proxyHealth.Lock()
	defer s.proxyHealth.Unlock()
	if s.proxyHealth.states == nil {
		s.proxyHealth.states = map[string]proxyHealthView{}
	}
	v := s.proxyHealth.states[key]
	if failed {
		v.Status = "unhealthy"
		v.Failures++
		v.LastFailureAt = now
		v.CooldownUntil = now.Add(30 * time.Second)
		v.Reason = "fixed proxy connection failed"
	} else {
		v.Status = "healthy"
		v.LastOKAt = now
		v.CooldownUntil = time.Time{}
		v.Reason = ""
		v.Failures = 0
	}
	s.proxyHealth.states[key] = v
	if len(s.proxyHealth.states) > 1024 {
		for k, st := range s.proxyHealth.states {
			if k != key && now.Sub(maxTime(st.LastOKAt, st.LastFailureAt)) > time.Hour {
				delete(s.proxyHealth.states, k)
			}
		}
	}
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// Connectivity diagnostics are explicit admin operations, with no production
// lease, RPM debit, health score or inference-token cost. Never dial direct.
func (s *Server) apiOpenCodeProxyTest(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	a, ok := cfg.Account(r.PathValue("id"))
	if !ok || !config.IsOpenCode(a.Provider) {
		apiError(w, 404, errors.New("OpenCode account not found"))
		return
	}
	if !cfg.OpenCode.Enabled {
		apiError(w, http.StatusConflict, errors.New("OpenCode is disabled; no upstream was contacted"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if a.Provider == config.ProviderOpenCodeGo {
		if err := s.ensureGoEgress(ctx, cfg, a, true); err != nil {
			writeJSON(w, 200, map[string]any{"ok": false, "error": err.Error(), "egress": s.goEgressView(cfg, a), "proxyHealth": s.openCodeProxyView(a.ProxyURL)})
			return
		}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.RuntimeBase(a)+"/models", nil)
	egressid.Apply(req.Header, egressid.For(a.ID))
	req.Header.Set("Authorization", "Bearer "+a.RuntimeKey)
	resp, err := opencode.NoRedirect(s.httpClient(a.ProxyURL)).Do(req)
	if err != nil {
		if ctx.Err() == nil && isProxyError(err, a.ProxyURL) {
			s.observeOpenCodeProxy(a.ProxyURL, true)
		}
		writeJSON(w, 200, map[string]any{"ok": false, "error": "fixed egress connection failed; no direct fallback", "proxyHealth": s.openCodeProxyView(a.ProxyURL)})
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	s.observeOpenCodeProxy(a.ProxyURL, resp.StatusCode == http.StatusProxyAuthRequired)
	message := ""
	if resp.StatusCode == http.StatusProxyAuthRequired {
		message = "fixed proxy authentication required"
	}
	writeJSON(w, 200, map[string]any{"ok": resp.StatusCode != http.StatusProxyAuthRequired, "error": message, "upstreamStatus": resp.StatusCode, "note": "reachable egress is not proof of key authorization or static IP; optional pin checks are bounded by freshness", "egress": s.goEgressView(cfg, a), "proxyHealth": s.openCodeProxyView(a.ProxyURL)})
}
