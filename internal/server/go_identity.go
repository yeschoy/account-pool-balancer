package server

import (
	"net/http"
	"strings"

	"account-pool-balancer/internal/config"
)

// goIdentity keeps conversation identity separate from model-scoped cache
// preference. The returned hashes are internal; only source enums are logged.
func (s *Server) goIdentity(cfg *config.Config, scope string, h http.Header, body map[string]any) (Identity, string, string) {
	clean := make(map[string]any, len(body))
	for k, v := range body {
		if k != "prompt_cache_key" {
			clean[k] = v
		}
	}
	hint, source := "", "none"
	if v, ok := validValue(lookup(body, "prompt_cache_key")); ok {
		hint, source = v, "body.prompt_cache_key"
	}
	if hint == "" {
		for _, name := range []string{"X-Prompt-Cache-Key", "Prompt-Cache-Key"} {
			if v, ok := validValue(headerValue(h, name)); ok {
				hint, source = v, "header.prompt_cache_key"
				break
			}
		}
	}
	cache := ""
	if hint != "" {
		cache = s.fingerprint(scope, "go-cache:"+lookup(body, "model"), hint)
	}
	ident := s.identity(cfg, scope, h, clean)
	return ident, cache, source
}

func goClientFamily(h http.Header, body map[string]any) string {
	ua := strings.ToLower(h.Get("User-Agent"))
	switch {
	case strings.Contains(ua, "codex"):
		return "codex"
	case strings.Contains(ua, "claude"):
		return "claude-code"
	case strings.Contains(ua, "opencode"):
		return "opencode"
	case strings.Contains(ua, "pi/") || strings.Contains(ua, "pi-agent"):
		return "pi"
	}
	if strings.Contains(strings.ToLower(h.Get("Originator")), "codex") {
		return "codex"
	}
	if headerValue(h, "X-Claude-Code-Session-Id") != "" || claudeUserSession(lookup(body, "metadata.user_id")) != "" {
		return "claude-code"
	}
	return "other"
}

func goUserAgent(ua string) string {
	lower := strings.ToLower(strings.TrimSpace(ua))
	for _, product := range []string{"codex", "claude", "opencode", "pi-agent"} {
		if strings.Contains(lower, product) {
			return ua
		}
	}
	for _, generic := range []string{"go-http-client", "curl/", "python-requests", "python-httpx", "openai/", "anthropic/", "node-fetch", "undici"} {
		if strings.HasPrefix(lower, generic) {
			return "account-pool-balancer/go-gateway"
		}
	}
	if lower == "" {
		return "account-pool-balancer/go-gateway"
	}
	return ua
}

// A response chain reuses its original opaque session across continuation and
// checkpoint restoration. Native client/cache fields in the body remain intact.
func (s *Server) goUpstreamSession(cfg *config.Config, scope string, h http.Header, body map[string]any, ident Identity) string {
	if ident.KeyType == "previous_response_id" {
		if session := s.pool.UpstreamSession(ident.Fingerprint); session != "" {
			return session
		}
		clean := make(map[string]any, len(body))
		for k, v := range body {
			if k != "previous_response_id" && k != "prompt_cache_key" {
				clean[k] = v
			}
		}
		ident = s.identity(cfg, scope, h, clean)
	}
	if ident.Fingerprint == "" {
		return ""
	}
	return "apb-" + ident.Fingerprint
}
