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

// deployWithCachePreference adds Deploy's node-selection knob when the caller
// did not choose one. The field is spliced in after the last member, so every
// byte the client sent is forwarded unchanged. body must be validated JSON.
func deployWithCachePreference(body []byte, pref int) []byte {
	if pref <= 0 {
		return body
	}
	o := scanMistralObject(body)
	if o == nil || o.get("cache_preference") != nil {
		return body
	}
	at, field := o.end, `"cache_preference":`+strconv.Itoa(pref)
	if n := len(o.fields); n > 0 {
		at, field = o.fields[n-1].end, ","+field
	}
	out := make([]byte, 0, len(body)+len(field))
	out = append(out, body[:at]...)
	out = append(out, field...)
	return append(out, body[at:]...)
}

const deployTransportPrefix = "\x00deploy:"

// Use the existing transport owner/cache, but with Deploy's connection caps.
func (s *Server) deployHTTPClient(cfg *config.Config, proxy string) *http.Client {
	r := cfg.Deploy.EffectiveRouting()
	key := deployTransportPrefix + strconv.Itoa(r.DirectMaxConns) + ":" + strconv.Itoa(r.ProxyMaxConns) + ":" + proxy
	return &http.Client{Transport: s.transportFor(proxy, r, key), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (s *Server) pruneNativeChatTransports(old, next *config.Config) {
	s.pruneProviderTransports(old.Factory.EffectiveRouting(), next.Factory.EffectiveRouting(), factoryTransportPrefix)
	s.pruneProviderTransports(old.X50.EffectiveRouting(), next.X50.EffectiveRouting(), "\x00x50:")
	s.pruneProviderTransports(old.Deploy.EffectiveRouting(), next.Deploy.EffectiveRouting(), deployTransportPrefix)
}
func (s *Server) pruneProviderTransports(a, b config.Routing, prefix string) {
	if a.DirectMaxConns == b.DirectMaxConns && a.ProxyMaxConns == b.ProxyMaxConns {
		return
	}
	s.tmu.Lock()
	defer s.tmu.Unlock()
	s.transports.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), prefix) {
			value.(*http.Transport).CloseIdleConnections()
			s.transports.Delete(key)
		}
		return true
	})
}
