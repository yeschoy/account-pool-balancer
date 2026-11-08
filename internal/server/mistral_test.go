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
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/pool"
)

type mistralCall struct {
	key, path, body string
	header          http.Header
}
type mistralEnv struct {
	s         *Server
	up, front *httptest.Server
	mu        sync.Mutex
	calls     []mistralCall
	handler   http.HandlerFunc
}

func newMistralEnv(t *testing.T) *mistralEnv {
	t.Helper()
	e := &mistralEnv{}
	e.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"m","base_model_id":"ancestor-not-an-alias"}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.calls = append(e.calls, mistralCall{r.Header.Get("Authorization"), r.URL.Path, string(b), r.Header.Clone()})
		h := e.handler
		e.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"reply","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":3}}}`)
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
		c.Mistral.BaseURL = e.up.URL + "/v1"
		c.Routing.Strategy, c.Routing.ConcurrencyWaitMs = "ordered", 0
		c.Accounts = []config.Account{{ID: "m1", RuntimeKey: "mstrl_mock_secret_1", Enabled: true, MaxConc: 1, Priority: 1}, {ID: "m2", RuntimeKey: "mstrl_mock_secret_2", Enabled: true, MaxConc: 1, Priority: 2}}
		c.ClientKeys = []config.ClientKey{{ID: "client1", Group: config.MistralGroup, Provider: config.ProviderMistral, KeyHash: config.HashKey("mistral-client"), Enabled: true}, {ID: "client2", Group: config.MistralGroup, Provider: config.ProviderMistral, KeyHash: config.HashKey("mistral-other-client"), Enabled: true}}
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
func (e *mistralEnv) post(t *testing.T, path, body string, headers map[string]string) (int, string) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, e.front.URL+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer mistral-client")
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, err := e.front.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
func (e *mistralEnv) count() int                    { e.mu.Lock(); defer e.mu.Unlock(); return len(e.calls) }
func (e *mistralEnv) call(i int) mistralCall        { e.mu.Lock(); defer e.mu.Unlock(); return e.calls[i] }
func (e *mistralEnv) setHandler(h http.HandlerFunc) { e.mu.Lock(); e.handler = h; e.mu.Unlock() }

func TestMistralAccountTestModel(t *testing.T) {
	for _, tc := range []struct {
		name, model, wantModel string
		inference              bool
	}{
		{name: "default without cached catalog", inference: true, wantModel: "deepseek-v4-1-flash-1017"},
		{name: "explicit model preserved", model: "custom-mistral-model", inference: true, wantModel: "custom-mistral-model"},
		{name: "free check does not infer", inference: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newMistralEnv(t)
			body, _ := json.Marshal(map[string]any{"inference": tc.inference, "model": tc.model})
			r := httptest.NewRequest(http.MethodPost, "/api/accounts/m1/test", strings.NewReader(string(body)))
			r.SetPathValue("id", "m1")
			w := httptest.NewRecorder()
			e.s.apiTestAccount(w, r)
			var out struct {
				Runtime   struct{ OK bool } `json:"runtime"`
				Inference *struct {
					OK    bool   `json:"ok"`
					Model string `json:"model"`
				} `json:"inference"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil || !out.Runtime.OK {
				t.Fatalf("account test failed: %d %s", w.Code, w.Body.String())
			}
			if !tc.inference {
				if out.Inference != nil || e.count() != 0 {
					t.Fatal("free check triggered inference")
				}
				return
			}
			if out.Inference == nil || !out.Inference.OK || out.Inference.Model != tc.wantModel || e.count() != 1 {
				t.Fatalf("wrong inference model or count: %s; calls=%d", w.Body.String(), e.count())
			}
			var sent struct {
				Model     string `json:"model"`
				MaxTokens int    `json:"max_tokens"`
			}
			call := e.call(0)
			if json.Unmarshal([]byte(call.body), &sent) != nil || sent.Model != tc.wantModel || sent.MaxTokens != 1 || call.path != "/v1/chat/completions" {
				t.Fatalf("wrong upstream test request: %s %s", call.path, call.body)
			}
		})
	}
}

func TestMistralNativeBytesHeadersAndAccounting(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/responses/input_tokens", "/v1/fim/completions", "/v1/embeddings"} {
		t.Run(path, func(t *testing.T) {
			e := newMistralEnv(t)
			body := `{ "model":"m", "store":false, "input":"test", "messages":[{"role":"user","content":"hello"}], "tools":[{"type":"function","function":{"name":"test"}}], "cache_control":{"type":"ephemeral"} }`
			status, reply := e.post(t, path, body, map[string]string{"Session_id": "private-codex-session", "X-Api-Key": "must-not-leak", "X-Prompt-Cache-Key": "private-cache", "User-Agent": "pi/test"})
			if status != 200 {
				t.Fatalf("%d %s", status, reply)
			}
			c := e.call(0)
			if path == "/v1/chat/completions" || path == "/v1/responses" {
				var got, original map[string]json.RawMessage
				_ = json.Unmarshal([]byte(c.body), &got)
				_ = json.Unmarshal([]byte(body), &original)
				var cache string
				_ = json.Unmarshal(got["prompt_cache_key"], &cache)
				if !strings.HasPrefix(cache, "apb-mistral-") || len(got) != len(original)+1 {
					t.Fatal("missing private session cache key")
				}
				for key, value := range original {
					if string(got[key]) != string(value) {
						t.Fatalf("unrelated native field changed: %s", key)
					}
				}
			} else if c.body != body {
				t.Fatal("non-generation body changed")
			}
			if c.path != path || c.key != "Bearer mstrl_mock_secret_1" || c.header.Get("X-Api-Key") != "" || c.header.Get("User-Agent") != "pi/test" || headerValue(c.header, "Session_id") != "private-codex-session" || c.header.Get("X-Prompt-Cache-Key") != "private-cache" {
				t.Fatal("native bytes or headers changed")
			}
			logs, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
			if len(logs.Items) != 1 {
				t.Fatal("missing pool-scoped request log")
			}
			var rec requestLog
			_ = json.Unmarshal(logs.Items[0], &rec)
			if rec.InputTokens == nil || *rec.InputTokens != 10 || rec.CachedTokens == nil || *rec.CachedTokens != 3 || rec.AffinityConfidence != "explicit" {
				t.Fatal("lost accounting or identity")
			}
			for _, secret := range []string{"private-codex-session", "private-cache", "mstrl_mock_secret", "mistral-client"} {
				if strings.Contains(string(logs.Items[0]), secret) {
					t.Fatal("ordinary log leaked identity or key")
				}
			}
		})
	}
}

func TestMistralSessionAffinity(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		extra   string
	}{
		{"codex", map[string]string{"Session_id": "session-private"}, ""},
		{"codex-thread", map[string]string{"X-Codex-Thread-Id": "session-private"}, ""},
		{"claude", map[string]string{"X-Claude-Code-Session-Id": "session-private"}, ""},
		{"pi", map[string]string{"X-Pi-Session-Id": "session-private"}, ""},
		{"pi-standard", map[string]string{"X-Session-Affinity": "session-private", "User-Agent": "pi/test"}, ""},
		{"parent", map[string]string{"X-Codex-Parent-Thread-Id": "session-private"}, ""},
		{"custom", map[string]string{"X-Custom-Conversation": "session-private"}, ""},
		{"claude-metadata", nil, `,"metadata":{"user_id":"{\"session_id\":\"session-private\"}"}`},
		{"body-session", nil, `,"session_id":"session-private"`},
		{"cache", nil, `,"prompt_cache_key":"session-private"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newMistralEnv(t)
			_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				c.Routing.SessionHeaders = []string{"X-Custom-Conversation"}
				c.Routing.Strategy = "least-selections"
				for i := range c.Accounts {
					c.Accounts[i].Priority = 50
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				status, b := e.post(t, "/v1/chat/completions", fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"turn-%d"}]%s}`, i, tc.extra), tc.headers)
				if status != 200 {
					t.Fatalf("%d %s", status, b)
				}
				if e.call(i).key != e.call(0).key {
					t.Fatal("session moved between accounts")
				}
			}
		})
	}
	e := newMistralEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Routing.Strategy = "least-selections"
		for i := range c.Accounts {
			c.Accounts[i].Priority = 50
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, auth := range []string{"mistral-client", "mistral-other-client"} {
		if st, b := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, map[string]string{"Authorization": "Bearer " + auth, "X-Pi-Session-Id": "same-session"}); st != 200 {
			t.Fatalf("%d %s", st, b)
		}
	}
	if e.call(0).key == e.call(1).key {
		t.Fatal("downstream keys shared a session binding")
	}
}

func TestMistralBusyPolicyMatchesExistingScheduler(t *testing.T) {
	for _, policy := range []string{"reject", "overflow", "wait-overflow"} {
		t.Run(policy, func(t *testing.T) {
			e := newMistralEnv(t)
			_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				r := c.Routing
				r.OnBindingBusy = policy
				c.Mistral.Routing = &r
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			headers := map[string]string{"X-Pi-Session-Id": "busy-session"}
			body := `{"model":"m","messages":[]}`
			if st, b := e.post(t, "/v1/chat/completions", body, headers); st != 200 {
				t.Fatalf("%d %s", st, b)
			}
			cfg := e.s.cfg.Get().ForProvider(config.ProviderMistral)
			id := e.s.identity(cfg, config.MistralGroup+"\x00client1", http.Header{"X-Pi-Session-Id": []string{"busy-session"}}, nil)
			lease, err := e.s.pool.Acquire(context.Background(), cfg, pool.Request{Group: config.MistralGroup, Model: "m", Affinity: id.Fingerprint, Explicit: true})
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			st, b := e.post(t, "/v1/chat/completions", body, headers)
			if policy == "reject" {
				if st != 429 || e.count() != 1 {
					t.Fatalf("busy binding overflowed: %d %s", st, b)
				}
			} else {
				if st != 200 || e.call(1).key == e.call(0).key {
					t.Fatalf("configured overflow failed: %d %s", st, b)
				}
			}
			if owner, ok := e.s.pool.BoundAccount(id.Fingerprint); !ok || owner != "m1" {
				t.Fatal("temporary overflow changed binding")
			}
		})
	}
}

func TestMistralErrorsRetryAndStatelessBoundary(t *testing.T) {
	for _, code := range []int{400, 401, 402, 403, 422, 429, 503} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "Bearer mstrl_mock_secret_1" {
					w.Header().Set("Retry-After", "12345")
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(code)
					fmt.Fprint(w, `{"error":"mock failure"}`)
					return
				}
				fmt.Fprint(w, `{"choices":[]}`)
			})
			st, b := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil)
			switch code {
			case 401, 402, 429, 503:
				if st != 200 || e.count() != 2 {
					t.Fatalf("failover: %d %s calls=%d", st, b, e.count())
				}
			default:
				want := code
				if code == 403 {
					want = 502
				} // existing downstream account-auth mapping
				if st != want || e.count() != 1 {
					t.Fatalf("deterministic error replayed: %d %s", st, b)
				}
			}
			if code == 429 {
				state, _ := e.s.pool.ExportState()
				if time.Until(state.States["m1"].CooldownUntil) < 12340*time.Second {
					t.Fatal("Retry-After truncated")
				}
			}
		})
	}
	e := newMistralEnv(t)
	for _, body := range []string{`{"model":"m","previous_response_id":"resp-missing","input":"next"}`, `{"model":"m","store":true,"input":"next"}`, `{"model":"m","model":"other"}`, `{"Model":"m"}`, `{"model":"m",}`, `{"input":"no model"}`} {
		if st, b := e.post(t, "/v1/responses", body, nil); st != 400 {
			t.Fatalf("unsafe request accepted: %d %s", st, b)
		}
	}
	for _, path := range []string{"/v1/messages", "/v1/files", "/v1/responses/some-id"} {
		if st, _ := e.post(t, path, `{"model":"m"}`, nil); st != 404 {
			t.Fatal("unknown endpoint allowed")
		}
	}
	if e.count() != 0 {
		t.Fatal("local rejection contacted upstream")
	}
}

func TestMistralStreamAndNoUncertainReplay(t *testing.T) {
	t.Run("native-stream", func(t *testing.T) {
		e := newMistralEnv(t)
		wire := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n"
		e.setHandler(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, wire)
		})
		st, b := e.post(t, "/v1/chat/completions", `{"model":"m","stream":true,"messages":[]}`, nil)
		if st != 200 || b != wire || e.count() != 1 {
			t.Fatalf("stream rewritten: %d %q", st, b)
		}
	})
	t.Run("connection-closed-after-send", func(t *testing.T) {
		e := newMistralEnv(t)
		e.setHandler(func(w http.ResponseWriter, r *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		})
		if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 502 || e.count() != 1 {
			t.Fatal("uncertain sent request replayed")
		}
	})
	t.Run("redirect", func(t *testing.T) {
		e := newMistralEnv(t)
		var leaked atomic.Bool
		sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
		defer sink.Close()
		e.setHandler(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, sink.URL+"/v1/chat/completions", 307)
		})
		e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil)
		if leaked.Load() || e.count() != 1 {
			t.Fatal("credential redirect followed")
		}
	})
}

func TestMistralPoolIsolationAndUnknownUsage(t *testing.T) {
	e := newMistralEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Upstream.RuntimeBase = e.up.URL + "/v1"
		c.Accounts = append(c.Accounts, config.Account{ID: "foreign", RuntimeKey: "sk-amr-foreign-key", Group: config.DefaultGroup, Enabled: true})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[]}`)
	})
	if st, b := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	logs, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
	var rec requestLog
	if len(logs.Items) != 1 {
		t.Fatal("missing request log")
	}
	_ = json.Unmarshal(logs.Items[0], &rec)
	if rec.InputTokens != nil || rec.OutputTokens != nil || rec.CachedTokens != nil {
		t.Fatal("missing usage fabricated as zero")
	}
	e.s.pool.Quarantine("m1", 401, "test", "test")
	e.s.pool.Quarantine("m2", 401, "test", "test")
	if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 503 || e.count() != 1 {
		t.Fatal("Mistral borrowed an OpenDesign account")
	}
}

func TestMistralResponsesSSE(t *testing.T) {
	e := newMistralEnv(t)
	wire := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_mistral\",\"usage\":{\"input_tokens\":4,\"output_tokens\":1}}}\n\n"
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, wire)
	})
	if st, b := e.post(t, "/v1/responses", `{"model":"m","stream":true,"store":false,"input":"hi"}`, nil); st != 200 || b != wire {
		t.Fatal("Responses stream rewritten")
	}
	logs, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
	var rec requestLog
	if len(logs.Items) != 1 {
		t.Fatal("missing Responses log")
	}
	_ = json.Unmarshal(logs.Items[0], &rec)
	if rec.InputTokens == nil || *rec.InputTokens != 4 || rec.OutputTokens == nil || *rec.OutputTokens != 1 {
		t.Fatal("Responses SSE usage lost")
	}
	state, _ := e.s.pool.ExportState()
	if len(state.Bindings) != 0 {
		t.Fatal("stateless Responses bindings persisted")
	}
}

func TestMistralModelsNativeAndSourceInvalidation(t *testing.T) {
	e := newMistralEnv(t)
	cfg := e.s.cfg.Get()
	a := cfg.Accounts[0]
	if err := e.s.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	q := e.s.quota.Get(a.ID)
	if !q.Catalog["m"] || len(q.Aliases) != 0 || !q.PlanAt.IsZero() || q.WalletUSD != "" {
		t.Fatal("catalog invented alias or funding")
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer mistral-client")
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"m"`) || strings.Contains(w.Body.String(), "ancestor-not-an-alias") {
		t.Fatalf("catalog model renamed: %s", w.Body)
	}
	if class := e.s.pool.Class(cfg, "m"); class != "unknown" {
		t.Fatal("Mistral polluted OpenDesign funding classes")
	}
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Mistral.BaseURL = "http://127.0.0.1:1/v1"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if e.s.quota.View(a.ID) != nil {
		t.Fatal("old catalog survived source change")
	}
	if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 502 || e.count() != 0 {
		t.Fatal("failed Mistral route escaped to old base or another provider")
	}
}

func TestMistralAdminImportSettingsAndPrivacy(t *testing.T) {
	e := newMistralEnv(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/import", strings.NewReader(`{"provider":"mistral","text":"opaque-mistral-key,new\nopaque-mistral-key,duplicate"}`))
	e.s.apiImportAccounts(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"added":1`) || !strings.Contains(w.Body.String(), `"unchanged":1`) {
		t.Fatalf("import: %d %s", w.Code, w.Body)
	}
	ik := config.ImportKey{ID: "ik", Provider: config.ProviderMistral, Group: config.MistralGroup}
	res := e.s.importOne(ik, importItem{RuntimeKey: "mstrl_open_import_key", Name: "open"}, time.Now())
	if res.Status != "added" {
		t.Fatalf("open import: %+v", res)
	}
	wrong := e.s.importOne(ik, importItem{Provider: config.ProviderOpenDesign, RuntimeKey: "sk-amr-foreign"}, time.Now())
	if wrong.Status != "error" {
		t.Fatal("Mistral import key crossed provider")
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/accounts/m1/keys", nil)
	r.SetPathValue("id", "m1")
	e.s.apiAccountKeys(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "mstrl_mock_secret_1") {
		t.Fatal("administrator cannot copy saved key")
	}
	old := e.s.cfg.Get()
	doc := settingsDoc{Revision: old.Revision, Upstream: old.Upstream, Routing: old.Routing, Quota: old.Quota, Logs: old.Logs, Pricing: old.Pricing, Mistral: &config.Mistral{BaseURL: e.up.URL + "/v1"}, ErrorRules: old.MistralErrorRules, RetryRules: old.MistralRetryRules}
	doc.Routing.Strategy = "ordered"
	body, _ := json.Marshal(doc)
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, "/api/settings?provider=mistral", strings.NewReader(string(body)))
	e.s.apiSaveSettings(w, r)
	if w.Code != 200 || e.s.cfg.Get().Routing.Strategy != old.Routing.Strategy || e.s.cfg.Get().ForProvider(config.ProviderMistral).Routing.Strategy != "ordered" {
		t.Fatalf("settings isolation: %d %s", w.Code, w.Body)
	}
	before := e.s.cfg.Get()
	doc.Revision = before.Revision
	doc.Mistral.BaseURL = "https://secret:pass@host/v1"
	body, _ = json.Marshal(doc)
	w = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, "/api/settings?provider=mistral", strings.NewReader(string(body)))
	e.s.apiSaveSettings(w, r)
	if w.Code != 400 || e.s.cfg.Get() != before {
		t.Fatal("invalid settings published")
	}
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Logs.Detail = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if st, b := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[],"metadata":{"session_id":"private-session-value"}}`, map[string]string{"X-Pi-Session-Id": "private-session-value"}); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	for _, kind := range []string{"requests", "details"} {
		logs, _ := e.s.logs.Query(kind, map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 20, 0)
		if len(logs.Items) == 0 {
			t.Fatal("missing logs")
		}
		for _, b := range logs.Items {
			for _, secret := range []string{"mstrl_mock_secret_1", "private-session-value", "mistral-client"} {
				if strings.Contains(string(b), secret) {
					t.Fatalf("%s leaked private identity", kind)
				}
			}
		}
	}
	if got := redactSecrets(config.Default(), "mstrl_unconfigured_secret_123"); strings.Contains(got, "unconfigured") {
		t.Fatal("Mistral prefix redaction missing")
	}
}
