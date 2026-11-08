package server

import (
	"net/http"

	"account-pool-balancer/internal/config"
)

// Mistral reports its trace in Mistral-Correlation-Id/X-Kong-Request-Id,
// not necessarily X-Request-Id. Keep the native header map unchanged: details
// must still distinguish provider headers from this downstream projection.
func upstreamRequestID(upstream http.Header, provider string) string {
	if provider != config.ProviderMistral {
		return upstream.Get("X-Request-Id")
	}
	for _, name := range []string{"X-Request-Id", "Mistral-Correlation-Id", "X-Kong-Request-Id"} {
		if id, ok := validValue(upstream.Get(name)); ok {
			return id
		}
	}
	return ""
}

// Expose only the delivered attempt's provider trace, never an internal req_*.
func setUpstreamRequestID(dst, upstream http.Header, provider string) {
	if id := upstreamRequestID(upstream, provider); id != "" {
		dst.Set("X-Upstream-Request-Id", id)
	}
}
