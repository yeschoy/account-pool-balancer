package server

import (
	"account-pool-balancer/internal/config"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFactoryStatefulOwnerAndSource(t *testing.T) {
	e := newFactoryEnv(t)
	rsp, out := e.do(t, "/v1/responses", `{"model":"gpt-5.4","input":"hello","store":true}`, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(out)
	}
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].Priority = 99; c.Accounts[1].Priority = 1; return nil })
	if err != nil {
		t.Fatal(err)
	}
	rsp, out = e.do(t, "/v1/responses", `{"model":"gpt-5.4","input":"next","previous_response_id":"resp_factory_1"}`, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(out)
	}
	calls := e.snapshot()
	if len(calls) != 2 || calls[1].auth != "Bearer fk-mock_factory_key_1" {
		t.Fatal("continuation changed owner")
	}
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"gpt-5.4","previous_response_id":"resp_factory_1","input":"foreign client"}`))
	req.Header.Set("Authorization", "Bearer other-client")
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error {
		c.ClientKeys = append(c.ClientKeys, config.ClientKey{ID: "other", Provider: config.ProviderFactory, Group: "factory", KeyHash: config.HashKey("other-client"), Enabled: true})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, req)
	if w.Code != 409 || len(e.snapshot()) != 2 {
		t.Fatal("foreign client continuation leaked", w.Code)
	}
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].RuntimeKey = "fk-mock_factory_new_key"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	rsp, _ = e.do(t, "/v1/responses", `{"model":"gpt-5.4","input":"next","previous_response_id":"resp_factory_1"}`, nil)
	if rsp.StatusCode != 409 || len(e.snapshot()) != 2 {
		t.Fatal("old source continuation rebound")
	}
}
func TestFactorySSEStopsAndDoesNotReplay(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			e := newFactoryEnv(t)
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"}}]}\n\n")
				if complete {
					fmt.Fprint(w, "data: [DONE]\n\n")
				}
			}
			e.mu.Unlock()
			rsp, out := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","stream":true,"messages":[]}`, nil)
			if rsp.StatusCode != 200 || len(e.snapshot()) != 1 || strings.Contains(out, "[DONE]") != complete {
				t.Fatal("stream mutated/replayed", rsp.StatusCode, out)
			}
		})
	}
}
func TestFactoryUnknownNetworkOutcomeNotReplayed(t *testing.T) {
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		h := w.(http.Hijacker)
		c, _, err := h.Hijack()
		if err == nil {
			c.Close()
		}
	}
	e.mu.Unlock()
	rsp, _ := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[]}`, nil)
	if rsp.StatusCode != 502 || len(e.snapshot()) != 1 {
		t.Fatal("unknown outcome replayed", rsp.StatusCode, len(e.snapshot()))
	}
}
func TestFactoryCredentialRedirectForbidden(t *testing.T) {
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; fmt.Fprint(w, `{"choices":[{}]}`) }))
	defer target.Close()
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }
	e.mu.Unlock()
	rsp, _ := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash"}`, nil)
	if rsp.StatusCode == 200 || hits != 0 {
		t.Fatal("credential followed redirect")
	}
}
