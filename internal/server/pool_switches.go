package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"account-pool-balancer/internal/config"
)

func (s *Server) poolWorkContext(provider string) context.Context {
	s.poolWorkMu.Lock()
	defer s.poolWorkMu.Unlock()
	if s.poolWork == nil {
		s.poolWork = map[string]context.Context{}
		s.poolWorkCancel = map[string]context.CancelFunc{}
	}
	root := s.requestContext
	if root == nil {
		root = context.Background()
	} // lightweight fixtures without New
	if !s.cfg.Get().PoolEnabled(provider) {
		ctx, cancel := context.WithCancel(root)
		cancel()
		return ctx
	}
	if ctx := s.poolWork[provider]; ctx != nil {
		return ctx
	}
	ctx, cancel := context.WithCancel(root)
	s.poolWork[provider], s.poolWorkCancel[provider] = ctx, cancel
	return ctx
}
func (s *Server) reconcilePoolWork(cfg *config.Config) {
	s.poolWorkMu.Lock()
	defer s.poolWorkMu.Unlock()
	for p, cancel := range s.poolWorkCancel {
		if !cfg.PoolEnabled(p) {
			cancel()
			delete(s.poolWorkCancel, p)
			delete(s.poolWork, p)
		}
	}
}
func (s *Server) poolJobContext(parent context.Context, p string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	root := s.poolWorkContext(p)
	stop := context.AfterFunc(root, cancel)
	if root.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}
func (s *Server) apiPools(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	pools := []map[string]any{}
	for _, p := range config.ManagedPools() {
		n := 0
		for _, a := range c.Accounts {
			if a.Provider == p {
				n++
			}
		}
		pools = append(pools, map[string]any{"provider": p, "enabled": c.PoolEnabled(p), "accounts": n})
	}
	writeJSON(w, 200, map[string]any{"revision": c.Revision, "pools": pools})
}
func (s *Server) apiSetPool(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("provider")
	if !config.KnownManagedPool(p) {
		apiError(w, 400, errors.New("unknown pool"))
		return
	}
	var in struct {
		Revision int64 `json:"revision"`
		Enabled  *bool `json:"enabled"`
	}
	if e := readBody(r, &in); e != nil || in.Enabled == nil {
		apiError(w, 400, errors.New("enabled is required"))
		return
	}
	c, e := s.cfg.Update(in.Revision, func(c *config.Config) error {
		if c.PoolSwitches == nil {
			c.PoolSwitches = map[string]bool{}
		}
		c.PoolSwitches[p] = *in.Enabled
		return nil
	})
	if e != nil {
		s.configError(w, e)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "revision": c.Revision, "enabled": c.PoolEnabled(p)})
}
func (s *Server) adminPool(r *http.Request) string {
	path := r.URL.Path
	for _, p := range config.ManagedPools() {
		if strings.HasPrefix(path, "/api/"+p+"/") {
			return p
		}
	}
	if strings.HasPrefix(path, "/api/accounts/") {
		if a, ok := s.cfg.Get().Account(r.PathValue("id")); ok {
			return a.Provider
		}
	}
	if strings.HasPrefix(path, "/api/device-auth") {
		return config.ProviderOpenDesign
	}
	return ""
}

// Disabled pools allow only local maintenance; unclassified actions fail shut
// rather than accidentally allowing a new upstream endpoint added later.
func (s *Server) poolAdminAllowed(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	p := s.adminPool(r)
	if p == "" || s.cfg.Get().PoolEnabled(p) {
		return true
	}
	path := r.URL.Path
	local := r.Method == http.MethodDelete
	for _, suffix := range []string{"/settings", "/cancel", "/recover", "/reset-health", "/note", "/assign-proxy", "/assign-proxies", "/clear-finished"} {
		if strings.HasSuffix(path, suffix) {
			local = true
		}
	}
	if (r.Method == http.MethodPut || r.Method == http.MethodPatch) && strings.HasPrefix(path, "/api/accounts/") {
		local = true
	}
	if r.Method == http.MethodPost && (strings.HasSuffix(path, "/candidates") || strings.HasSuffix(path, "/candidates/bulk")) {
		local = true
	}
	if local {
		return true
	}
	writeJSON(w, 409, map[string]string{"error": "号池已关闭；请在号池管理中开启后再执行上游操作"})
	return false
}
