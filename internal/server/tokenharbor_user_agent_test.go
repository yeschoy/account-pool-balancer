package server

import (
	"testing"

	"account-pool-balancer/internal/opencode"
)

func TestTokenHarborUsesHonestGatewayUserAgent(t *testing.T) {
	for _, protocol := range []opencode.Protocol{opencode.Chat, opencode.Responses, opencode.Messages} {
		for _, ua := range []string{"", "Go-http-client/1.1", "python-httpx/0.28", "codex_cli_rs/test", "claude-code/test", "pi/1.0", "custom-client/2.0"} {
			t.Run(string(protocol)+"/"+ua, func(t *testing.T) {
				e := newTHEnv(t)
				status, _, _ := e.post(t, "/v1"+opencode.Path(protocol), ocRequest(protocol, "m", false), map[string]string{"User-Agent": ua})
				if status != 200 {
					t.Fatal("mock inference failed", status)
				}
				want := ua
				if ua == "" || ua == "Go-http-client/1.1" || ua == "python-httpx/0.28" {
					want = "account-pool-balancer/tokenharbor-gateway"
				}
				if got := e.call(0).header.Get("User-Agent"); got != want {
					t.Fatalf("User-Agent = %q, want %q", got, want)
				}
			})
		}
	}
}
