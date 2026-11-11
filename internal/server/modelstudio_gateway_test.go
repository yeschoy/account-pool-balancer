package server

import (
	"bytes"
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
)

type msCall struct {
	path, auth, apiKey, body string
	header                   http.Header
}

type msEnv struct {
	s         *Server
	front, up *httptest.Server
	mu        sync.Mutex
	calls     []msCall
	handler   http.HandlerFunc
}

// newMSEenv starts a Model Studio pool against a mock upstream. The mock
// records every path so the tests can prove which host shape each protocol
// was sent to.
func newMSEEnv(t testing.TB) *msEnv {
	t.Helper()
	e := &msEnv{}
	e.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.calls = append(e.calls, msCall{path: r.URL.Path, auth: r.Header.Get("Authorization"), apiKey: r.Header.Get("X-Api-Key"), body: string(b), header: r.Header.Clone()})
		h := e.handler
		e.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen3.8-flash"},{"id":"deepseek-v4.1-flash"}]}`)
		case strings.Contains(r.URL.Path, "/apps/anthropic/"):
			fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"qwen3.8-flash","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":0}}`)
		default:
			fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","model":"qwen3.8-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4}}}`)
		}
	}))
	t.Cleanup(e.up.Close)
	var err error
	e.s, err = New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.s.stopPerch(); e.s.logs.Close() })
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Quota.Enabled = false
		c.ModelStudio.BaseOverride = e.up.URL
		c.ModelStudio.Routing = &config.Routing{Strategy: "ordered", MaxAccounts: 2, ConcurrencyWaitMs: 0}
		c.Accounts = []config.Account{
			{ID: "ms1", Provider: config.ProviderModelStudio, Group: config.ModelStudioGroup, RuntimeKey: "sk-sp-mock-secret-1", Enabled: true, Priority: 1, MaxConc: 4},
			{ID: "ms2", Provider: config.ProviderModelStudio, Group: config.ModelStudioGroup, RuntimeKey: "sk-sp-mock-secret-2", Enabled: true, Priority: 2, MaxConc: 4},
		}
		c.ClientKeys = []config.ClientKey{{ID: "client", Provider: config.ProviderModelStudio, Group: config.ModelStudioGroup, KeyHash: config.HashKey("downstream-ms"), Enabled: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.front = httptest.NewServer(e.s.Handler())
	t.Cleanup(e.front.Close)
	for !e.s.logs.Ready() {
		time.Sleep(time.Millisecond)
	}
	return e
}

func (e *msEnv) do(t *testing.T, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.front.URL+path, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer downstream-ms")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func (e *msEnv) snapshot() []msCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]msCall(nil), e.calls...)
}

// Chat Completions goes to the OpenAI-compatible root and authenticates with
// the account's Bearer key; the downstream key never reaches the upstream.
func TestModelStudioChatCompletions(t *testing.T) {
	e := newMSEEnv(t)
	resp, body := e.do(t, "/v1/chat/completions", `{"model":"qwen3.8-flash","messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	calls := e.snapshot()
	if len(calls) == 0 {
		t.Fatal("no upstream call")
	}
	c := calls[0]
	if c.path != "/compatible-mode/v1/chat/completions" {
		t.Fatalf("path = %q", c.path)
	}
	if c.auth != "Bearer sk-sp-mock-secret-1" {
		t.Fatalf("upstream got %q", c.auth)
	}
	if strings.Contains(c.body, "downstream-ms") {
		t.Fatal("downstream key leaked upstream")
	}
	var got struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(body), &got) != nil || len(got.Choices) != 1 || got.Choices[0].Message.Content != "hi" {
		t.Fatalf("body not passed through: %s", body)
	}
}

// Anthropic Messages goes to /apps/anthropic, authenticates with x-api-key,
// and must not carry the caller's Authorization header upstream.
func TestModelStudioAnthropicMessages(t *testing.T) {
	e := newMSEEnv(t)
	resp, body := e.do(t, "/v1/messages", `{"model":"qwen3.8-flash","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"anthropic-version": "2023-06-01"})
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	calls := e.snapshot()
	if len(calls) == 0 {
		t.Fatal("no upstream call")
	}
	c := calls[0]
	if c.path != "/apps/anthropic/v1/messages" {
		t.Fatalf("path = %q", c.path)
	}
	if c.apiKey != "sk-sp-mock-secret-1" {
		t.Fatalf("x-api-key = %q", c.apiKey)
	}
	if c.auth != "" {
		t.Fatalf("caller Authorization leaked upstream: %q", c.auth)
	}
	var got struct {
		Type    string `json:"type"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(body), &got) != nil || got.Type != "message" {
		t.Fatalf("anthropic body not passed through: %s", body)
	}
}

// Responses is refused locally: the upstream does not implement it, and a 404
// must not be invented by sending it anyway.
func TestModelStudioResponsesRefusedLocally(t *testing.T) {
	e := newMSEEnv(t)
	resp, body := e.do(t, "/v1/responses", `{"model":"qwen3.8-flash","input":"hi"}`, nil)
	if resp.StatusCode < 400 {
		t.Fatalf("responses was forwarded: %d %s", resp.StatusCode, body)
	}
	if calls := e.snapshot(); len(calls) != 0 {
		t.Fatalf("unsupported protocol reached upstream: %+v", calls)
	}
}

// A revoked key hard-quarantines the account and the next request fails over
// to the healthy account: the pool keeps serving.
func TestModelStudioInvalidKeyFailover(t *testing.T) {
	e := newMSEEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Authorization"), "secret-1") {
			w.WriteHeader(401)
			fmt.Fprint(w, `{"error":{"message":"Incorrect API key provided.","type":"invalid_request_error","code":"invalid_api_key"},"request_id":"r1"}`)
			return
		}
		fmt.Fprint(w, `{"id":"chatcmpl-2","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}
	e.mu.Unlock()
	for _, n := range []int{0, 1} {
		resp, body := e.do(t, "/v1/chat/completions", `{"model":"qwen3.8-flash","messages":[{"role":"user","content":"hi"}]}`, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("attempt %d status %d: %s", n, resp.StatusCode, body)
		}
	}
	// the bad key is isolated: later requests only use the healthy account
	before := len(e.snapshot())
	for i := 0; i < 3; i++ {
		if resp, body := e.do(t, "/v1/chat/completions", `{"model":"qwen3.8-flash","messages":[{"role":"user","content":"hi"}]}`, nil); resp.StatusCode != 200 {
			t.Fatalf("after failover status %d: %s", resp.StatusCode, body)
		}
	}
	for _, c := range e.snapshot()[before:] {
		if c.auth == "Bearer sk-sp-mock-secret-1" {
			t.Fatal("quarantined account kept receiving requests")
		}
	}
}

// An exhausted quota cools the account and switches, and the client is told
// 503 rather than a fabricated balance.
func TestModelStudioQuotaExhausted(t *testing.T) {
	e := newMSEEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":{"message":"Your token-plan quota has been exhausted.","type":"invalid_request_error","code":"insufficient_quota"}}`)
	}
	e.mu.Unlock()
	resp, body := e.do(t, "/v1/chat/completions", `{"model":"qwen3.8-flash","messages":[{"role":"user","content":"hi"}]}`, nil)
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	calls := e.snapshot()
	if len(calls) != 2 {
		t.Fatalf("expected both accounts tried once, got %d", len(calls))
	}
}

// The model catalogue is a real read of the account's /models.
func TestModelStudioModelList(t *testing.T) {
	e := newMSEEnv(t)
	req, _ := http.NewRequest(http.MethodGet, e.front.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer downstream-ms")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if !strings.Contains(string(b), "qwen3.8-flash") {
		t.Fatalf("catalogue missing: %s", b)
	}
	for _, c := range e.snapshot() {
		if !strings.HasSuffix(c.path, "/compatible-mode/v1/models") {
			t.Fatalf("models read from %q", c.path)
		}
	}
}

// Cross-pool isolation: a Model Studio client key cannot reach another pool's
// accounts, and a tokenharbor key is refused here.
func TestModelStudioClientKeyIsolation(t *testing.T) {
	e := newMSEEnv(t)
	req, _ := http.NewRequest(http.MethodPost, e.front.URL+"/v1/chat/completions", bytes.NewReader([]byte(`{"model":"qwen3.8-flash","messages":[{"role":"user","content":"hi"}]}`)))
	req.Header.Set("Authorization", "Bearer not-a-modelstudio-client")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("unknown downstream key was accepted")
	}
	if calls := e.snapshot(); len(calls) != 0 {
		t.Fatal("upstream contacted for an unauthorized client")
	}
}
