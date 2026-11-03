package server

import (
	"bytes"
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
	"account-pool-balancer/internal/opencode"
)

type ocEnv struct {
	s           *Server
	front, up   *httptest.Server
	mu          sync.Mutex
	keys        []string
	paths       []string
	bodies      [][]byte
	lastReply   string
	handler     http.HandlerFunc
	usageFailed atomic.Bool
	upCalls     atomic.Int32
}

func newOCEnv(t *testing.T, second bool) *ocEnv {
	t.Helper()
	e := &ocEnv{}
	e.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.upCalls.Add(1)
		switch {
		case r.URL.Path == "/capabilities":
			fmt.Fprint(w, `{"opencode":{"npm":"@ai-sdk/openai-compatible","models":{"chat":{},"responses":{"provider":{"npm":"@ai-sdk/openai"}},"messages":{"provider":{"npm":"@ai-sdk/anthropic"}},"same":{}}},"opencode-go":{"npm":"@ai-sdk/anthropic","models":{"chat":{"provider":{"npm":"@ai-sdk/openai-compatible"}},"responses":{"provider":{"npm":"@ai-sdk/openai"}},"messages":{},"same":{}}}}`)
			return
		case strings.HasSuffix(r.URL.Path, "/models"):
			fmt.Fprint(w, `{"data":[{"id":"chat"},{"id":"responses"},{"id":"messages"},{"id":"same"}]}`)
			return
		case strings.HasSuffix(r.URL.Path, "/usage"):
			if e.usageFailed.Load() {
				w.WriteHeader(503)
				fmt.Fprint(w, `{"error":"unavailable"}`)
				return
			}
			fmt.Fprint(w, `{"usage":{"rolling":{"status":"ok","percent":10,"resetsAt":"2099-01-01T00:00:00Z"},"weekly":{"status":"ok","percent":30,"resetsAt":"2099-02-01T00:00:00Z"},"monthly":{"status":"ok","percent":20,"resetsAt":"2099-03-01T00:00:00Z"}}}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if key == "" {
			key = r.Header.Get("X-Api-Key")
		}
		e.mu.Lock()
		e.keys = append(e.keys, key)
		e.paths = append(e.paths, r.URL.Path)
		e.bodies = append(e.bodies, b)
		h := e.handler
		e.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		p := opencode.FromPath("/v1" + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/zen/go/v1"), "/zen/v1"))
		if p == opencode.Messages {
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-Api-Key") == "" || r.Header.Get("Anthropic-Version") == "" {
				t.Error("wrong Messages auth")
			}
			if strings.HasPrefix(r.URL.Path, "/zen/go/") && r.URL.Query().Get("beta") != "true" {
				t.Error("Go Messages beta query missing")
			}
		} else if r.Header.Get("X-Api-Key") != "" || r.Header.Get("Authorization") == "" {
			t.Error("wrong OpenAI auth")
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		w.Header().Set("Content-Type", "application/json")
		reply := ocReply(p)
		if m["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			reply = ocStream(p)
		}
		e.mu.Lock()
		e.lastReply = reply
		e.mu.Unlock()
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(e.up.Close)
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	t.Cleanup(func() { s.stopPerch(); s.logs.Close() })
	_, err = s.cfg.Update(-1, func(c *config.Config) error {
		c.Quota.Enabled = false
		c.OpenCode.Enabled = true
		c.OpenCode.Compatibility = true
		c.OpenCode.ZenBase = e.up.URL + "/zen/v1"
		c.OpenCode.GoBase = e.up.URL + "/zen/go/v1"
		c.OpenCode.CapabilitiesURL = e.up.URL + "/capabilities"
		c.Routing.Strategy = "ordered"
		c.Routing.ConcurrencyWaitMs = 0
		c.Accounts = []config.Account{{ID: "zen1", Provider: config.ProviderOpenCodeZen, RuntimeKey: "sk-zen-secret-key", Enabled: true, MaxConc: 2, Priority: 1}, {ID: "go1", Provider: config.ProviderOpenCodeGo, RuntimeKey: "opaque-go-secret-key", Enabled: true, MaxConc: 2}}
		if second {
			c.Accounts = append(c.Accounts, config.Account{ID: "zen2", Provider: config.ProviderOpenCodeZen, RuntimeKey: "second-zen-secret-key", Enabled: true, MaxConc: 2, Priority: 2})
		}
		for _, p := range []string{config.ProviderOpenCodeZen, config.ProviderOpenCodeGo} {
			c.ClientKeys = append(c.ClientKeys, config.ClientKey{ID: p, Provider: p, Group: p, KeyHash: config.HashKey("downstream-" + p), Enabled: true})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range s.cfg.Get().Accounts {
		if err := s.quota.RefreshNow(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	e.front = httptest.NewServer(s.Handler())
	t.Cleanup(e.front.Close)
	return e
}
func ocReply(p opencode.Protocol) string {
	switch p {
	case opencode.Messages:
		return `{"id":"reply-1","type":"message","model":"m","role":"assistant","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"tool-original","name":"lookup","input":{"q":1}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":5}}`
	case opencode.Responses:
		return `{"id":"reply-1","object":"response","model":"m","status":"completed","output":[{"type":"message","id":"message-1","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}]},{"type":"function_call","id":"fc-1","call_id":"tool-original","name":"lookup","arguments":"{\"q\":1}"}],"usage":{"input_tokens":15,"output_tokens":2,"input_tokens_details":{"cached_tokens":5}}}`
	default:
		return `{"id":"reply-1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hello","tool_calls":[{"id":"tool-original","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":15,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":5}}}`
	}
}
func ocStream(p opencode.Protocol) string {
	// Decode and re-emit exercises the same wire contract, while native byte
	// preservation is independently asserted against this exact payload.
	r, err := opencode.DecodeReply(p, []byte(ocReply(p)))
	if err != nil {
		panic(err)
	}
	b, err := r.Stream(p)
	if err != nil {
		panic(err)
	}
	return string(b)
}
func (e *ocEnv) post(t *testing.T, provider, path, body string, headers map[string]string) (int, []byte, http.Header) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, e.front.URL+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer downstream-"+provider)
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
	return resp.StatusCode, b, resp.Header
}
func (e *ocEnv) count() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.keys) }
func ocRequest(p opencode.Protocol, model string, stream bool) string {
	if p == opencode.Responses {
		return fmt.Sprintf(`{"model":%q,"input":"hello","max_output_tokens":100,"stream":%v}`, model, stream)
	}
	return fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"max_tokens":100,"stream":%v}`, model, stream)
}
func TestOpenCodeNativeAndCompatibilityMatrix(t *testing.T) {
	e := newOCEnv(t, false)
	for _, provider := range []string{config.ProviderOpenCodeZen, config.ProviderOpenCodeGo} {
		for _, from := range []opencode.Protocol{opencode.Chat, opencode.Responses, opencode.Messages} {
			for _, native := range []opencode.Protocol{opencode.Chat, opencode.Responses, opencode.Messages} {
				for _, stream := range []bool{false, true} {
					name := fmt.Sprintf("%s/%s-%s/%v", provider, from, native, stream)
					t.Run(name, func(t *testing.T) {
						body := ocRequest(from, string(native), stream)
						status, b, h := e.post(t, provider, "/v1"+opencode.Path(from), body, nil)
						if status != 200 || !bytes.Contains(b, []byte("tool-original")) {
							t.Fatalf("%d %s", status, b)
						}
						if from == native {
							if h.Get("X-Compatibility-Mode") != "" {
								t.Fatal("native converted")
							}
							e.mu.Lock()
							want := e.lastReply
							e.mu.Unlock()
							if string(b) != want {
								t.Fatal("native response changed")
							}
							e.mu.Lock()
							sent := string(e.bodies[len(e.bodies)-1])
							e.mu.Unlock()
							if sent != body {
								t.Fatalf("native request changed: %s", sent)
							}
						} else if h.Get("X-Compatibility-Mode") != "buffered" {
							t.Fatal("conversion not disclosed")
						}
					})
				}
			}
		}
	}
}
func TestOpenCodeAccountFailuresAndNoRequestQuarantine(t *testing.T) {
	for _, code := range []int{401, 402, 403, 429, 500, 400, 422} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			e := newOCEnv(t, true)
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "Bearer sk-zen-secret-key" {
					w.Header().Set("Retry-After", time.Now().Add(2*time.Minute).UTC().Format(http.TimeFormat))
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(code)
					fmt.Fprint(w, `{"error":{"message":"key sk-zen-secret-key refused"}}`)
					return
				}
				fmt.Fprint(w, ocReply(opencode.Chat))
			}
			status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), nil)
			if code == 400 || code == 422 {
				if status != code || e.count() != 1 {
					t.Fatalf("request error retried: %d %s calls=%d", status, b, e.count())
				}
				if v := e.s.pool.Views(e.s.cfg.Get())["zen1"]; v.State != nil && (v.State.HardQuarantined || !v.State.CooldownUntil.IsZero()) {
					t.Fatal("request quarantined account")
				}
				if bytes.Contains(b, []byte("sk-zen-secret-key")) {
					t.Fatal("credential leaked to error body")
				}
			} else if status != 200 || e.count() != 2 {
				t.Fatalf("failover failed %d %s calls=%d", status, b, e.count())
			}
			if code == 429 {
				st := e.s.pool.Views(e.s.cfg.Get())["zen1"].State
				if st == nil || time.Until(st.CooldownUntil) < 100*time.Second {
					t.Fatal("HTTP-date Retry-After not respected")
				}
			}
		})
	}
}
func TestOpenCodeSSEPrecommitAndNoReplayAfterContent(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			e := newOCEnv(t, true)
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if r.Header.Get("Authorization") == "Bearer sk-zen-secret-key" {
					if after {
						fmt.Fprint(w, "data: {\"id\":\"r\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
						w.(http.Flusher).Flush()
					}
					fmt.Fprint(w, "event: error\ndata: {\"error\":{\"status\":429,\"message\":\"rate limit\"}}\n\n")
					return
				}
				fmt.Fprint(w, ocStream(opencode.Chat))
			}
			status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", true), nil)
			want := 2
			if after {
				want = 1
			}
			if status != 200 || e.count() != want {
				t.Fatalf("stream replay boundary: %d %s calls=%d", status, b, e.count())
			}
		})
	}
}
func TestOpenCodeResponsesStickyScopeAndMissingState(t *testing.T) {
	e := newOCEnv(t, true)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.ClientKeys = append(c.ClientKeys, config.ClientKey{ID: "different", Provider: config.ProviderOpenCodeZen, Group: config.ProviderOpenCodeZen, KeyHash: config.HashKey("another-client"), Enabled: true})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/responses", ocRequest(opencode.Responses, "responses", false), nil)
	if status != 200 {
		t.Fatalf("%d %s", status, b)
	}
	status, b, _ = e.post(t, config.ProviderOpenCodeZen, "/v1/responses", `{"model":"responses","previous_response_id":"reply-1","input":[{"type":"function_call_output","call_id":"tool-original","output":"answer"}]}`, nil)
	if status != 200 {
		t.Fatalf("%d %s", status, b)
	}
	e.mu.Lock()
	same := e.keys[0] == e.keys[1]
	e.mu.Unlock()
	if !same {
		t.Fatal("stateful continuation moved account")
	}
	before := e.count()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"responses","previous_response_id":"reply-1","input":"next"}`))
	req.Header.Set("Authorization", "Bearer another-client")
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, req)
	if w.Code != 409 || e.count() != before {
		t.Fatalf("response binding crossed client key: %d %s", w.Code, w.Body)
	}
}
func TestOpenCodeUnknownQuotaAndDiagnosticNoSchedulerCost(t *testing.T) {
	e := newOCEnv(t, false)
	e.usageFailed.Store(true)
	a, _ := e.s.cfg.Get().Account("go1")
	before := e.s.pool.Views(e.s.cfg.Get())[a.ID]
	result := e.s.testOpenCode(context.Background(), e.s.cfg.Get(), a, true, "chat")
	after := e.s.pool.Views(e.s.cfg.Get())[a.ID]
	if before.Selections != after.Selections || before.RPM != after.RPM || after.Active != 0 {
		t.Fatal("diagnostic spent production lease/RPM")
	}
	if result["diagnostic"] == nil {
		t.Fatal("quota failure hidden")
	}
	q := e.s.quota.View(a.ID)
	if q.OpenCode.Usage == nil || q.OpenCode.UsageError == "" || q.OpenCode.Exhausted(time.Now(), time.Hour) {
		t.Fatal("unknown/stale quota treated as zero")
	}
	status, b, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), nil)
	if status != 200 {
		t.Fatalf("unknown quota blocked inference: %d %s", status, b)
	}
}
func TestOpenCodeCancellationReleasesLease(t *testing.T) {
	e := newOCEnv(t, false)
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"r\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.front.URL+"/v1/chat/completions", strings.NewReader(ocRequest(opencode.Chat, "chat", true)))
	req.Header.Set("Authorization", "Bearer downstream-"+config.ProviderOpenCodeZen)
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	_, _ = resp.Body.Read(buf)
	cancel()
	resp.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e.s.pool.Views(e.s.cfg.Get())["zen1"].Active == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("cancel leaked concurrency lease")
}
