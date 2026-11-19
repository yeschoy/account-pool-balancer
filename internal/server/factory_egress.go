package server

import (
	"hash/fnv"
	"net/http"
	"strings"

	"account-pool-balancer/internal/config"
)

// factoryUsedEgress counts the proxies and UAs already assigned to other
// enabled Factory accounts. Assignment is deterministic and prefers an unused
// pool entry, so a pool at least as large as the account set yields distinct
// device egress/IP and distinct inference UAs.
func factoryUsedEgress(cfg *config.Config, exclude string) (map[string]bool, map[string]bool) {
	proxies, uas := map[string]bool{}, map[string]bool{}
	for _, a := range cfg.Accounts {
		if a.ID == exclude || a.Provider != config.ProviderFactory || !a.Enabled {
			continue
		}
		if a.ProxyURL != "" {
			proxies[a.ProxyURL] = true
		}
		if a.UserAgent != "" {
			uas[a.UserAgent] = true
		}
	}
	return proxies, uas
}

func pickFactoryPool(pool []string, used map[string]bool, seed string) string {
	if len(pool) == 0 {
		return ""
	}
	for _, v := range pool {
		if !used[v] {
			return v
		}
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(seed))
	return pool[int(h.Sum32())%len(pool)]
}

func pickFactoryPoolAt(pool []string, used map[string]bool, seed string, start int) string {
	if len(pool) == 0 {
		return ""
	}
	for i := 0; i < len(pool); i++ {
		v := pool[(start+i)%len(pool)]
		if !used[v] {
			return v
		}
	}
	return pickFactoryPool(pool, used, seed)
}

// factoryEgressFor returns the proxy and inference UA a new account should get.
// The per-server sequence makes two concurrent device logins start at
// different pool positions even before either account exists.
func (s *Server) factoryEgressFor(cfg *config.Config, id string) (proxy, ua string) {
	proxies, uas := factoryUsedEgress(cfg, id)
	n := int(s.factoryEgressSeq.Add(1))
	return pickFactoryPoolAt(cfg.Factory.ProxyPool, proxies, id, n), pickFactoryPoolAt(cfg.Factory.UserAgents, uas, id, n)
}

// assignFactoryEgress fills only missing fields; an explicit per-account proxy
// or UA is never overwritten.
func assignFactoryEgress(cfg *config.Config, a *config.Account) bool {
	if a.Provider != config.ProviderFactory {
		return false
	}
	changed := false
	proxies, uas := factoryUsedEgress(cfg, a.ID)
	if strings.TrimSpace(a.ProxyURL) == "" {
		if p := pickFactoryPool(cfg.Factory.ProxyPool, proxies, a.ID); p != "" {
			a.ProxyURL, changed = p, true
		}
	}
	if strings.TrimSpace(a.UserAgent) == "" {
		if ua := pickFactoryPool(cfg.Factory.UserAgents, uas, a.ID); ua != "" {
			a.UserAgent, changed = ua, true
		}
	}
	return changed
}

// apiFactoryAssignEgress fills missing proxies/UAs for existing Factory
// accounts. It is explicit rather than a side effect of reloading settings.
func (s *Server) apiFactoryAssignEgress(w http.ResponseWriter, r *http.Request) {
	var assignedProxy, assignedUA int
	_, err := s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			a := &c.Accounts[i]
			if a.Provider != config.ProviderFactory || !a.Enabled {
				continue
			}
			beforeProxy, beforeUA := a.ProxyURL, a.UserAgent
			assignFactoryEgress(c, a)
			if a.ProxyURL != beforeProxy {
				assignedProxy++
			}
			if a.UserAgent != beforeUA {
				assignedUA++
			}
		}
		return nil
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"proxyAssigned": assignedProxy, "uaAssigned": assignedUA})
}
