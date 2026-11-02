package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perch"
	"account-pool-balancer/internal/perchstore"
	"account-pool-balancer/internal/pool"
)

func TestPerchStickyHeadersEndToEnd(t *testing.T) {
	headers := []string{
		"X-Claude-Code-Session-Id", "Session_id", "Session-Id", "X-Session-Id",
		"X-Parent-Session-Id", "X-Parent-Session-Affinity", "X-Codex-Parent-Thread-Id", "X-Claude-Code-Parent-Agent-Id",
	}
	for _, surface := range []string{"cli", "web"} {
		for _, header := range headers {
			for _, sticky := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/sticky=%t", surface, header, sticky), func(t *testing.T) {
					var mu sync.Mutex
					var tokens, ids []string
					threads := 0
					up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						mu.Lock()
						defer mu.Unlock()
						if r.URL.Path == perch.WebThreadsPath {
							threads++
							fmt.Fprintf(w, `{"ok":true,"thread":{"id":"thread-%d"}}`, threads)
							return
						}
						if r.URL.Path != perch.ModelCallPath && r.URL.Path != perch.WebChatPath {
							http.NotFound(w, r)
							return
						}
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Errorf("decode envelope: %v", err)
							return
						}
						tokens = append(tokens, r.Header.Get("Authorization"))
						id, _ := body["runId"].(string)
						if surface == "web" {
							id, _ = body["threadId"].(string)
							if id != fmt.Sprintf("thread-%d", threads) {
								t.Errorf("web thread = %q, latest created = thread-%d", id, threads)
							}
						}
						ids = append(ids, id)
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"type\":\"done\",\"ok\":true,\"finishReason\":\"stop\"}\n\n")
					}))
					defer up.Close()
					var accounts []config.Account
					for _, id := range []string{"p1", "p2"} {
						accounts = append(accounts, config.Account{ID: id, Provider: config.ProviderPerch, Name: id, Group: "perch", PerchUserID: id, RuntimeKey: config.PerchRuntimeKey(id)})
					}
					e := newEnv(t, accounts, func(c *config.Config) {
						c.Upstream.PerchBase, c.Upstream.PerchWebBase = up.URL, up.URL
						c.Perch.Surface = surface
						c.Perch.GatePrefix = perch.GatePrefixAnchor + " - " + strings.Repeat("gate ", 60)
						c.Routing.Sticky = sticky
						c.Routing.FallbackHash = false
						c.ClientKeys[0].Group = "perch"
					})
					for _, id := range []string{"p1", "p2"} {
						_, err := e.srv.perch.keeper.Put(perchstore.Secrets{UserID: id, AccessToken: "access-" + id, RefreshToken: "refresh-" + id, ExpiresAt: time.Now().Add(2 * time.Hour).Unix()}, perchstore.PutOptions{BindAs: id, FetchedAt: time.Now().UTC(), AllowCreate: true})
						if err != nil {
							t.Fatal(err)
						}
					}
					for i, session := range []string{"session-a", "session-b", "session-b"} {
						resp := e.post("/v1/chat/completions", map[string]any{"model": "auto", "messages": []any{map[string]any{"role": "user", "content": fmt.Sprintf("message %d", i)}}}, map[string]string{header: session})
						body := readAll(resp)
						if resp.StatusCode != http.StatusOK {
							t.Fatalf("request %d: status %d: %s", i, resp.StatusCode, body)
						}
					}
					cfg := e.srv.cfg.Get()
					ident := e.srv.identity(cfg, "perch", http.Header{http.CanonicalHeaderKey(header): []string{"session-b"}}, nil)
					lease, err := e.srv.pool.Acquire(context.Background(), cfg, pool.Request{Group: "perch", Model: "auto", Affinity: ident.Fingerprint, Explicit: true})
					if err != nil {
						t.Fatal(err)
					}
					decision := lease.Decision
					lease.Release()
					wantBinding := "not-applicable"
					if sticky {
						wantBinding = "hit"
					}
					if decision.BindingResult != wantBinding {
						t.Fatalf("sticky=%t, binding result = %q, want %q", sticky, decision.BindingResult, wantBinding)
					}
					mu.Lock()
					defer mu.Unlock()
					if len(tokens) != 3 || (sticky && tokens[1] != tokens[2]) {
						t.Fatalf("sticky=%t, account sequence = %v", sticky, tokens)
					}
					if len(ids) != 3 || ids[0] == "" || ids[1] == "" || ids[2] == "" || ids[0] == ids[1] || ids[0] == ids[2] || ids[1] == ids[2] {
						t.Fatalf("expected independent upstream run/thread IDs, got %v", ids)
					}
					if surface == "web" && threads != 3 {
						t.Fatalf("web thread creations = %d, want 3", threads)
					}
				})
			}
		}
	}
}
