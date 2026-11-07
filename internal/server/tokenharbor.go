package server

import (
	"net/http"

	"account-pool-balancer/internal/config"
)

// Only inference settings are exposed; registration secrets remain write-only.
type tokenHarborSettings struct {
	BaseURL        string `json:"baseUrl"`
	ModelSyncSec   int    `json:"modelSyncSec"`
	AccountSyncSec *int   `json:"accountSyncSec,omitempty"`
	DetailLog      *bool  `json:"detailLog,omitempty"`
}

// Session identity wins over a reusable cache prefix (which may be shared by
// unrelated conversations). Cache marks themselves are not session IDs.
func (s *Server) tokenHarborIdentity(cfg *config.Config, scope string, h http.Header, body map[string]any) Identity {
	local := *cfg
	local.Routing.FallbackHash = false
	sessionBody := make(map[string]any, len(body))
	for k, v := range body {
		if k != "prompt_cache_key" {
			sessionBody[k] = v
		}
	}
	id := s.identity(&local, scope, h, sessionBody)
	if id.Fingerprint != "" {
		return id
	}
	for _, name := range []string{"X-Prompt-Cache-Key", "Prompt-Cache-Key", "X-Cache-Key"} {
		if v, ok := validValue(headerValue(h, name)); ok {
			return Identity{Source: "header", KeyType: "prompt_cache_key", Confidence: "explicit", Fingerprint: s.fingerprint(scope, "session", v)}
		}
	}
	return s.identity(cfg, scope, h, body)
}
