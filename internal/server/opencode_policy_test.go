package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/rules"
)

func TestOpenCodePolicyRefusalNeverWalksKeysOrFallbackEgress(t *testing.T) {
	for _, reason := range []string{"FreeTierError", "free tier can only be used from within OpenCode", "abusive traffic from this IP address"} {
		t.Run(reason, func(t *testing.T) {
			e := newOCEnv(t, true)
			_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				c.OpenCode.ZenFallbackGroup = config.ProviderOpenCodeGo
				c.OpenCode.ZenErrorRules = []rules.ErrorRule{{ID: "custom-forbidden", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, When: rules.Match{Statuses: []int{403}}, Reset: &rules.Reset{Fallback: "1s"}}}
				c.OpenCode.ZenRetryRules = []rules.RetryRule{}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(403)
				fmt.Fprintf(w, `{"error":{"message":%q}}`, reason)
			}
			status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), nil)
			if status != 403 || e.count() != 1 || !strings.Contains(string(b), "no account or egress replay") {
				t.Fatalf("policy refusal replayed: %d %s calls=%d", status, b, e.count())
			}
			v := e.s.pool.Views(e.s.cfg.Get())["zen1"]
			if v.Samples != 0 || v.State != nil && v.State.HardQuarantined {
				t.Fatal("policy refusal quarantined account")
			}
		})
	}
}
