package server

import (
	"net/http"
	"testing"

	"account-pool-balancer/internal/config"
)

// Chat and Responses requests from one conversation must resolve to the SAME
// conversation fingerprint, so both land on the same Perch run id and share
// one delta cache entry.
func TestPerchConversationFingerprintUnifiesProtocols(t *testing.T) {
	e := withPerchPool(t, newPerchModelFake(t))
	cfg := e.srv.cfg.Get()
	acct := cfg.Accounts[0]

	cases := []struct {
		name string
		hdr  map[string]string
		body map[string]any
		same bool
	}{
		{
			name: "chat with session header",
			hdr:  map[string]string{"X-Session-Id": "conv-42"},
			body: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		},
		{
			name: "responses with same session header",
			hdr:  map[string]string{"X-Session-Id": "conv-42"},
			body: map[string]any{"input": "hi"},
		},
		{
			name: "chat with prompt_cache_key",
			hdr:  map[string]string{},
			body: map[string]any{"prompt_cache_key": "conv-42"},
		},
		{
			name: "a different conversation",
			hdr:  map[string]string{"X-Session-Id": "conv-99"},
			body: map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}},
		},
	}

	var first string
	for i, tc := range cases {
		r, _ := http.NewRequest("POST", "/v1/chat/completions", nil)
		for k, v := range tc.hdr {
			r.Header.Set(k, v)
		}
		got := e.srv.perchConversationFingerprint(cfg, acct, r, tc.body)
		if got == "" {
			t.Fatalf("%s: empty fingerprint", tc.name)
		}
		if i == 0 {
			first = got
			continue
		}
		if tc.name == "a different conversation" {
			if got == first {
				t.Fatalf("%s must not share the fingerprint", tc.name)
			}
			continue
		}
		if got != first {
			t.Fatalf("%s: fingerprint %s != %s — the delta cache will not hit across protocols", tc.name, got[:12], first[:12])
		}
	}
}

var _ = config.ProviderPerch
