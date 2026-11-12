package server

import (
	"net/http"
	"strconv"
	"strings"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

type deployOverview struct {
	QuotaState     string `json:"quotaState"`
	QuotaReason    string `json:"quotaReason"`
	CatalogFresh   int    `json:"catalogFresh"`
	CatalogUnknown int    `json:"catalogUnknown"`
}

func deployOverviewFor(views []accountView) *deployOverview {
	var out *deployOverview
	for _, v := range views {
		if v.Provider != config.ProviderDeploy {
			continue
		}
		if out == nil {
			out = &deployOverview{QuotaState: "not-connected", QuotaReason: quota.DeployQuotaReason}
		}
		if v.Deploy != nil && v.Deploy.CatalogFresh {
			out.CatalogFresh++
		} else {
			out.CatalogUnknown++
		}
	}
	return out
}

const deployTransportPrefix = "\x00deploy:"

// Use the existing transport owner/cache, but with Deploy's connection caps.
func (s *Server) deployHTTPClient(cfg *config.Config, proxy string) *http.Client {
	r := cfg.Deploy.EffectiveRouting()
	key := deployTransportPrefix + strconv.Itoa(r.DirectMaxConns) + ":" + strconv.Itoa(r.ProxyMaxConns) + ":" + proxy
	return &http.Client{Transport: s.transportFor(proxy, r, key), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (s *Server) pruneDeployTransports(old, next *config.Config) {
	a, b := old.Deploy.EffectiveRouting(), next.Deploy.EffectiveRouting()
	if a.DirectMaxConns == b.DirectMaxConns && a.ProxyMaxConns == b.ProxyMaxConns {
		return
	}
	s.tmu.Lock()
	defer s.tmu.Unlock()
	s.transports.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), deployTransportPrefix) {
			value.(*http.Transport).CloseIdleConnections()
			s.transports.Delete(key)
		}
		return true
	})
}
