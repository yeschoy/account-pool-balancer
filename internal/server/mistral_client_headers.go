package server

import (
	"net/http"
	"sort"
	"strings"

	"account-pool-balancer/internal/config"
)

// This is the client -> Balancer boundary, before outbound header rewriting.
// Summary diagnostics remain available when body/detail logging is disabled.
func mistralClientRequestHeaders(cfg *config.Config, h http.Header) (map[string][]string, map[string]string, bool) {
	candidates := map[string]bool{}
	for _, name := range cfg.Routing.SessionHeaders {
		candidates[strings.ToLower(name)] = true
	}
	for _, name := range parentHeaders {
		candidates[strings.ToLower(name)] = true
	}
	for _, header := range sessionHeaders {
		candidates[strings.ToLower(header.name)] = true
	}
	for _, name := range []string{"X-Prompt-Cache-Key", "Prompt-Cache-Key"} {
		candidates[strings.ToLower(name)] = true
	}
	private := maskHeaders(cfg, thPrivateHeaders(h))
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	out, states := map[string][]string{}, map[string]string{}
	remaining, truncated := 8192, false
	for _, name := range names {
		if len(out) >= 64 || len(name) > remaining {
			truncated = true
			break
		}
		remaining -= len(name)
		values := private[name]
		if candidates[strings.ToLower(name)] {
			state := "invalid"
			value := headerValue(h, name)
			if strings.TrimSpace(value) == "" {
				state = "empty"
			} else if _, ok := validValue(value); ok {
				state = "valid"
			}
			states[name] = state
			values = []string{"[REDACTED]"} // Includes configured and parent-agent headers.
		}
		out[name] = []string{}
		for i, value := range values {
			if i >= 4 || remaining == 0 {
				truncated = true
				break
			}
			bounded := clip(value, min(512, remaining))
			truncated = truncated || len(bounded) < len(value)
			remaining -= len(bounded)
			out[name] = append(out[name], bounded)
		}
	}
	return out, states, truncated
}
