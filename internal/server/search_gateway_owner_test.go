package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/pool"
	"account-pool-balancer/internal/searchgateway"
	"account-pool-balancer/internal/websearch"
)

type ownerSearchFunc func(context.Context, websearch.Request) (websearch.Result, error)

func (f ownerSearchFunc) Search(c context.Context, r websearch.Request) (websearch.Result, error) {
	return f(c, r)
}
func TestSearchGatewayThroughRealPoolAdmission(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(fmt.Sprintf("busy=%t", busy), func(t *testing.T) {
			e := newMistralEnv(t)
			_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				c.ClientKeys = append(c.ClientKeys, config.ClientKey{ID: "gateway-client", Group: config.MistralGroup, Provider: config.ProviderMistral, KeyHash: config.HashKey("gateway-pool-test-client"), Enabled: true})
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(requestOwnerHeader) != "" {
					t.Error("token leaked to official backend")
				}
				e.mu.Lock()
				n := len(e.calls)
				e.mu.Unlock()
				if n == 1 {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"r1","status":"completed","output":[{"id":"fc1","type":"function_call","call_id":"call1","name":"apb_web_search","arguments":"{\"query\":\"public docs\"}","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
				} else {
					ownerReply(w)
				}
			})
			c, err := searchgateway.ReadConfig(strings.NewReader(fmt.Sprintf(`{"requestTimeoutSec":180,"routes":[{"id":"pool","tokenEnv":"ROUTE","upstreamBaseUrl":%q,"upstreamKeyEnv":"POOL","mode":"compat","requirePoolOwner":true,"allowedModels":["m"]}],"search":{"endpoint":"https://search.example/search","apiKeyEnv":"SEARCH"}}`, e.front.URL+"/v1")), func(k string) string {
				switch k {
				case "ROUTE":
					return "gateway-route-test-credential"
				case "POOL":
					return "gateway-pool-test-client"
				default:
					return "gateway-search-test-credential"
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			var held *pool.Lease
			defer func() {
				if held != nil {
					held.Release()
				}
			}()
			backend := ownerSearchFunc(func(ctx context.Context, r websearch.Request) (websearch.Result, error) {
				if busy {
					// Hold the exact first selected account while search is executing. Other
					// accounts remain free; the following model round must fail, never borrow.
					e.mu.Lock()
					firstKey := strings.TrimPrefix(e.calls[0].key, "Bearer ")
					e.mu.Unlock()
					cfg := e.s.cfg.Get()
					selected := ""
					for _, a := range cfg.Accounts {
						if a.RuntimeKey == firstKey {
							selected = a.ID
						}
					}
					var err error
					held, err = e.s.pool.Acquire(ctx, cfg.ForProvider(config.ProviderMistral), pool.Request{PinnedAccountID: selected, Group: config.MistralGroup, Model: "m", RequestID: "hold-during-search"})
					if err != nil {
						t.Error(err)
					}
				}
				return websearch.Result{Sources: []websearch.Source{{Title: "Docs", URL: "https://example.com", Content: "Synthetic public content"}}}, nil
			})
			g, err := searchgateway.New(c, searchgateway.Options{Search: backend})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("POST", "/r/pool/v1/responses", strings.NewReader(`{"model":"m","input":"synthetic QA","stream":true,"tools":[{"type":"web_search"}]}`))
			req.Header.Set("Authorization", "Bearer gateway-route-test-credential")
			req.Header.Set("Session-Id", "gateway-isolated-qa")
			req.Header.Set(requestOwnerHeader, "caller-injected-must-not-forward")
			w := httptest.NewRecorder()
			g.ServeHTTP(w, req)
			if busy {
				if !strings.Contains(w.Body.String(), "response.failed") || e.count() != 1 {
					t.Fatalf("busy owner borrowed another account: %d %s", e.count(), w.Body.String())
				}
			} else {
				if !strings.Contains(w.Body.String(), "response.completed") || e.count() != 2 || e.call(0).key != e.call(1).key {
					t.Fatalf("pool round trip failed: %d %s", e.count(), w.Body.String())
				}
				if !strings.Contains(w.Body.String(), `"input_tokens":20`) || !strings.Contains(w.Body.String(), `"output_tokens":4`) {
					t.Fatal("usage not aggregated")
				}
			}
			if w.Header().Get(requestOwnerHeader) != "" || strings.Contains(w.Body.String(), "ow1.") {
				t.Fatal("internal token leaked to gateway caller")
			}
			page, _ := e.s.logs.Query("requests", nil, time.Time{}, time.Time{}, 100, 0)
			pinned := 0
			for _, raw := range page.Items {
				var row struct {
					Owner string `json:"requestOwner"`
				}
				_ = json.Unmarshal(raw, &row)
				if row.Owner == "pinned" {
					pinned++
				}
			}
			if pinned != 1 {
				t.Fatal("pool did not record pinned continuation")
			}
			// Endpoints in this test exercise the actual HTTP server, account pool and
			// logs; only the external model and search backend are synthetic.
		})
	}
}
