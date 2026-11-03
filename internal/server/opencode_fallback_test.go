package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

func TestOpenCodeExplicitCrossTierFallbackReencodesAndKeepsAffinity(t *testing.T) {
	e := newOCEnv(t, false)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.OpenCode.ZenFallbackGroup = config.ProviderOpenCodeGo; return nil })
	if err != nil {
		t.Fatal(err)
	}
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/zen/v1/") {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"invalid key"}}`)
			return
		}
		if r.URL.Path != "/zen/go/v1/messages" || r.Header.Get("X-Api-Key") != "opaque-go-secret-key" {
			t.Errorf("cross-tier protocol/auth not adapted: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ocReply(opencode.Messages))
	}
	for i := 0; i < 2; i++ {
		status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/chat/completions", ocRequest(opencode.Chat, "same", false), map[string]string{"X-Session-Id": "shared-session"})
		if status != 200 || !strings.Contains(string(b), "tool-original") {
			t.Fatalf("fallback %d %s", status, b)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.keys) != 3 || e.keys[0] != "sk-zen-secret-key" || e.keys[1] != "opaque-go-secret-key" || e.keys[2] != e.keys[1] {
		t.Fatalf("fallback lost affinity: %v", e.keys)
	}
}
func TestOpenCodeStatefulFallbackContinuationNeverReturnsToPrimary(t *testing.T) {
	e := newOCEnv(t, false)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.OpenCode.ZenFallbackGroup = config.ProviderOpenCodeGo; return nil })
	if err != nil {
		t.Fatal(err)
	}
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/zen/v1/") {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"invalid key"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ocReply(opencode.Responses))
	}
	status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/responses", ocRequest(opencode.Responses, "responses", false), nil)
	if status != 200 {
		t.Fatalf("%d %s", status, b)
	}
	status, b, _ = e.post(t, config.ProviderOpenCodeZen, "/v1/responses", `{"model":"responses","previous_response_id":"reply-1","input":"next"}`, nil)
	if status != 200 {
		t.Fatalf("stateful fallback %d %s", status, b)
	}
	if e.count() != 3 {
		t.Fatal("unexpected stateful retry")
	}
}
