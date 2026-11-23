package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

// mistralPerfBody builds a Chat history shaped like the traffic the pool
// serves: a system prompt, then alternating user text and assistant answers
// with the legacy reasoning_content the relay itself emits. target is the
// approximate body size; the returned value is valid JSON.
func mistralPerfBody(target int, model string) []byte {
	const turns = 64
	answer := strings.Repeat("a", target/2/turns)
	thought := strings.Repeat("t", target/2/turns)
	user := strings.Repeat("u", target/2/turns)
	var b strings.Builder
	b.WriteString(`{"model":"` + model + `","stream":true,"reasoning_effort":"high","prompt_cache_key":"bench-key","messages":[`)
	b.WriteString(`{"role":"system","content":"You are a helpful assistant."},`)
	for i := 0; i < turns; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"role":"user","content":"%s"},`, user)
		fmt.Fprintf(&b, `{"role":"assistant","reasoning_content":"%s","content":"%s"}`, thought, answer)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func mistralPerfEnv(tb testing.TB) (*config.Config, *mistralLayouts, *Server, http.Header, string) {
	tb.Helper()
	store, err := config.Load(tb.TempDir() + "/config.json")
	if err != nil {
		tb.Fatal(err)
	}
	if _, err := store.Update(-1, func(c *config.Config) error {
		c.Quota.Enabled = false
		c.Mistral.BaseURL = "https://api.mistral.ai/v1"
		c.Mistral.Routing = nil
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	cfg := store.Get()
	s := &Server{secret: []byte("bench-secret")}
	layouts := newMistralLayouts(s.secret)
	ck := config.ClientKey{ID: "k1", Group: config.MistralGroup, KeyHash: "hash"}
	ident := Identity{Fingerprint: "fingerprint", Source: "header", KeyType: "session", Confidence: "explicit"}
	scope := layouts.scope(cfg, ck, ident, "deepseek-v4-1-flash-1017", "/v1/chat/completions")
	if scope == "" {
		tb.Fatal("empty layout scope")
	}
	h := http.Header{}
	h.Set("X-Session-Id", "bench-session")
	return cfg, layouts, s, h, scope
}

// mistralPerfPopulate stores a text-first layout for every other assistant turn
// so the benchmark measures hits as well as misses.
func mistralPerfPopulate(tb testing.TB, store *mistralLayouts, scope string, body []byte) {
	tb.Helper()
	root := scanMistralObject(body)
	messages := root.get("messages")
	for i := skipSpace(messages, 1); i < len(messages) && messages[i] != ']'; {
		end := skipValue(messages, i)
		message := scanMistralObject(messages[i:end])
		if mistralString(message.get("role")) == "assistant" {
			answer := mistralString(message.get("content"))
			thought := mistralString(message.get("reasoning_content"))
			store.put(mistralLayoutRecord{Key: store.textKey(scope, answer, thought), Blocks: []mistralLayoutBlock{
				{Kind: "text", Bytes: len(answer)},
				{Kind: "thinking", Parts: []int{len(thought)}, Closed: 2},
			}})
		}
		i = skipSpace(messages, end)
		if i < len(messages) && messages[i] == ',' {
			i = skipSpace(messages, i+1)
		}
	}
}

func BenchmarkMistralPath(b *testing.B) {
	cfg, layouts, srv, h, scope := mistralPerfEnv(b)
	ck := config.ClientKey{ID: "k1", Group: config.MistralGroup, KeyHash: "hash"}
	cacheKey := "bench-key"
	for _, size := range []int{128 << 10, 512 << 10, 2 << 20} {
		body := mistralPerfBody(size, "deepseek-v4-1-flash-1017")
		mistralPerfPopulate(b, layouts, scope, body)
		prepared, _, err := prepareMistralRequest(body, "/v1/chat/completions", cacheKey)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("parse/%dk", size>>10), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, err := parseRoutingBodyWithHistory(body, true, false); err != nil {
					b.Fatal(err)
				}
			}
		})
		obj, err := parseRoutingBodyWithHistory(body, true, false)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(fmt.Sprintf("identity/%dk", size>>10), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if id := srv.mistralIdentity(cfg, ck.Group, h, obj); id.Fingerprint == "" {
					b.Fatal("no identity")
				}
			}
		})
		b.Run(fmt.Sprintf("prepare/%dk", size>>10), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				if _, _, err := prepareMistralRequest(body, "/v1/chat/completions", cacheKey); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("restore/%dk", size>>10), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				log := &mistralLayoutLog{}
				if _, err := layouts.restore(body, prepared, scope, log); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("prepare+restore/%dk", size>>10), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				got, _, err := prepareMistralRequest(body, "/v1/chat/completions", cacheKey)
				if err != nil {
					b.Fatal(err)
				}
				log := &mistralLayoutLog{}
				if _, err := layouts.restore(body, got, scope, log); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMistralLayoutObserve measures the per-SSE-frame compatibility
// capture that runs for the three reasoning models while a stream is relayed.
func BenchmarkMistralLayoutObserve(b *testing.B) {
	store := newMistralLayouts([]byte("x"))
	capture := store.capture("scope", &mistralLayoutLog{})
	native := []byte(`{"id":"r","choices":[{"index":0,"delta":{"content":"native text delta"},"finish_reason":null}],"usage":null}`)
	chunks := []byte(`{"id":"r","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"thought"}],"closed":true},{"type":"text","text":"answer"}],"reasoning_content":""},"finish_reason":null}],"usage":null}`)
	for name, frame := range map[string][]byte{"native": native, "chunks": chunks} {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				capture.observe(frame, true)
			}
		})
	}
}

// mistralPerfGateway builds a real Balancer front server over a local mock
// upstream that answers one streaming Chat reply, so an end-to-end benchmark
// measures the gateway's own contribution (no model latency).
func mistralPerfGateway(tb testing.TB) (post func(body []byte) (firstByte, total time.Duration, err error)) {
	tb.Helper()
	frames := []string{
		`data: {"id":"r","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}],"usage":null}`,
		`data: {"id":"r","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`,
		`data: [DONE]`,
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, frame := range frames {
			if _, err := w.Write([]byte(frame + "\n\n")); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	tb.Cleanup(up.Close)
	dir := tb.TempDir()
	srv, err := New(dir, Options{})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { srv.stopPerch(); srv.logs.Close() })
	if _, err := srv.cfg.Update(-1, func(c *config.Config) error {
		c.Quota.Enabled = false
		c.Mistral.BaseURL = up.URL + "/v1"
		c.Routing.Strategy, c.Routing.ConcurrencyWaitMs = "ordered", 0
		c.Accounts = []config.Account{{ID: "m1", RuntimeKey: "mstrl_perf_key", Enabled: true, MaxConc: 4096, Priority: 1}}
		c.ClientKeys = []config.ClientKey{{ID: "perf", Group: config.MistralGroup, Provider: config.ProviderMistral, KeyHash: config.HashKey("perf-client"), Enabled: true}}
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	for !srv.logs.Ready() {
		time.Sleep(time.Millisecond)
	}
	front := httptest.NewServer(srv.Handler())
	tb.Cleanup(front.Close)
	client := front.Client()
	return func(body []byte) (time.Duration, time.Duration, error) {
		req, err := http.NewRequest(http.MethodPost, front.URL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			return 0, 0, err
		}
		req.Header.Set("Authorization", "Bearer perf-client")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Session-Id", "perf-session")
		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return 0, 0, err
		}
		defer resp.Body.Close()
		first := time.Since(start)
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			return 0, 0, err
		}
		return first, time.Since(start), nil
	}
}

func BenchmarkMistralGatewayStream(b *testing.B) {
	post := mistralPerfGateway(b)
	for _, size := range []int{128 << 10, 512 << 10} {
		body := mistralPerfBody(size, "deepseek-v4-1-flash-1017")
		b.Run(fmt.Sprintf("%dk", size>>10), func(b *testing.B) {
			// Warm the connection pool and layout/binding state before measuring.
			for i := 0; i < 3; i++ {
				if _, _, err := post(body); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			var firstTotal, allTotal time.Duration
			for b.Loop() {
				first, total, err := post(body)
				if err != nil {
					b.Fatal(err)
				}
				firstTotal += first
				allTotal += total
			}
			if n := time.Duration(b.N); n > 0 {
				b.ReportMetric(float64(firstTotal.Microseconds())/float64(b.N), "first-byte-us")
				b.ReportMetric(float64(allTotal.Microseconds())/float64(b.N), "total-us")
			}
		})
	}
}
