package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

func TestOpenCodeExplicitProtocolBaseDoesNotMoveCatalogOrUsage(t *testing.T) {
	e := newOCEnv(t, false)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.OpenCode.GoProtocolBases = map[string]string{"messages": e.up.URL + "/inference/go/anthropic/v1"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.s.cfg.Get().Account("go1")
	if err := e.s.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/inference/go/anthropic/v1/messages" || r.Header.Get("X-Api-Key") != "opaque-go-secret-key" || r.URL.Query().Get("beta") != "true" {
			t.Errorf("incorrect native target/auth: %s", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ocReply(opencode.Messages))
	}
	status, b, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/messages", ocRequest(opencode.Messages, "messages", false), nil)
	if status != 200 {
		t.Fatalf("protocol base %d %s", status, b)
	}
	if e.s.cfg.Get().RuntimeBase(a) != e.up.URL+"/zen/go/v1" {
		t.Fatal("inference override moved usage/catalog endpoint")
	}
}
