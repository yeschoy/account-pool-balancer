package server

import (
	"context"
	"net/http"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestGoRecentRequestsStrategyKeepsSessionAndCacheAheadOfBalance(t *testing.T) {
	e := newGoGatewayEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.OpenCode.GoRouting.Strategy = "least-requests-1h"
		for i := range c.Accounts {
			if c.Accounts[i].Provider == config.ProviderOpenCodeGo {
				c.Accounts[i].Priority = 50
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := e.s.pool.Views(e.s.cfg.Get())
	if before["go1"].RecentRequests1H != 0 || before["go2"].RecentRequests1H != 0 {
		t.Fatal("metadata reads counted as inference")
	}
	body := `{"model":"chat","prompt_cache_key":"warm-prefix","messages":[{"role":"user","content":"local mock"}]}`
	ownerHeaders := map[string]string{"Session_id": "owner"}
	for range 3 {
		if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, ownerHeaders); st != 200 {
			t.Fatal(st)
		}
	}
	if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, map[string]string{"Session_id": "cache-preferred"}); st != 200 {
		t.Fatal(st)
	}
	plain := `{"model":"chat","messages":[{"role":"user","content":"independent new conversation"}]}`
	if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", plain, map[string]string{"Session_id": "balanced"}); st != 200 {
		t.Fatal(st)
	}
	if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, ownerHeaders); st != 200 {
		t.Fatal(st)
	}
	e.mu.Lock()
	keys := append([]string(nil), e.keys...)
	e.mu.Unlock()
	if len(keys) != 6 || keys[0] != keys[1] || keys[0] != keys[2] || keys[0] != keys[3] || keys[0] == keys[4] || keys[0] != keys[5] {
		t.Fatal("session/cache precedence or unbound recent balancing failed")
	}
	views := e.s.pool.Views(e.s.cfg.Get())
	var total int64
	for _, id := range []string{"go1", "go2"} {
		total += views[id].RecentRequests1H
		if views[id].PendingRequests != 0 {
			t.Fatal("pending lease leaked")
		}
	}
	if total != 6 {
		t.Fatalf("sent attempts counted %d, want 6", total)
	}
	for _, a := range e.s.cfg.Get().Accounts {
		if a.Provider == config.ProviderOpenCodeGo {
			if err = e.s.quota.RefreshNow(context.Background(), a); err != nil {
				t.Fatal(err)
			}
		}
	}
	after := e.s.pool.Views(e.s.cfg.Get())
	if after["go1"].RecentRequests1H+after["go2"].RecentRequests1H != total {
		t.Fatal("quota refresh spent hourly request budget")
	}
	rows := goLogRows(t, e.s, "requests", map[string]string{"provider": config.ProviderOpenCodeGo}, 6)
	found := false
	for _, r := range rows {
		if r["selector"] != "least-requests-1h" {
			t.Fatal("log selector is not the effective Go strategy")
		}
		if r["selectionReason"] == "strategy:least-requests-1h" {
			found = true
		}
	}
	if !found {
		t.Fatal("new selection reason missing")
	}
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"mock upstream rejected request"}}`))
	}
	e.mu.Unlock()
	if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", plain, map[string]string{"Session_id": "sent-failure"}); st != 400 {
		t.Fatal(st)
	}
	after = e.s.pool.Views(e.s.cfg.Get())
	if after["go1"].RecentRequests1H+after["go2"].RecentRequests1H != total+1 {
		t.Fatal("failed-but-sent upstream attempt was not counted")
	}
	if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", "not-json", nil); st != 400 {
		t.Fatal(st)
	}
	after = e.s.pool.Views(e.s.cfg.Get())
	if after["go1"].RecentRequests1H+after["go2"].RecentRequests1H != total+1 {
		t.Fatal("local rejection counted as sent inference")
	}
}
