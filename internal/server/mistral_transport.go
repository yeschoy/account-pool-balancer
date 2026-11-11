package server

import (
	"net/http"
	"strconv"
	"strings"

	"account-pool-balancer/internal/config"
)

const mistralTransportPrefix = "\x00mistral:"

func mistralRouting(c *config.Config) config.Routing {
	if c.Mistral.Routing != nil {
		return *c.Mistral.Routing
	}
	return c.Routing
}
func (s *Server) mistralHTTPClient(cfg *config.Config, proxy string) *http.Client {
	r := mistralRouting(cfg)
	key := mistralTransportPrefix + strconv.Itoa(r.DirectMaxConns) + ":" + strconv.Itoa(r.ProxyMaxConns) + ":" + proxy
	return &http.Client{Transport: s.transportFor(proxy, r, key)}
}
func (s *Server) pruneMistralTransports(old, next *config.Config) {
	a, b := mistralRouting(old), mistralRouting(next)
	if a.DirectMaxConns == b.DirectMaxConns && a.ProxyMaxConns == b.ProxyMaxConns {
		return
	}
	s.tmu.Lock()
	defer s.tmu.Unlock()
	s.transports.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), mistralTransportPrefix) {
			value.(*http.Transport).CloseIdleConnections()
			s.transports.Delete(key)
		}
		return true
	})
}
