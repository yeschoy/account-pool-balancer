package searchgateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/websearch"
)

func TestStrictConfiguration(t *testing.T) {
	c := configFor(t, "http://127.0.0.1:8790", "compat")
	base := string(marshal(c))
	cases := []string{
		strings.Replace(base, `"mode":"compat"`, `"mode":"compat","mode":"native"`, 1),
		strings.Replace(base, `"mode":"compat"`, `"mode":"guess"`, 1),
		strings.Replace(base, `"allowedModels":["test-model"]`, `"allowedModels":[]`, 1),
		strings.Replace(base, `http://127.0.0.1:8790/v1`, `https://user:secret@example.com/v1`, 1),
		strings.Replace(base, `http://127.0.0.1:8790/v1`, `https://example.com/v1?token=bad`, 1),
		strings.Replace(base, `https://search.example/api/search`, `http://search.example/api/search`, 1),
		strings.Replace(base, `"tokenEnv":"ROUTE_TOKEN"`, `"tokenEnv":"bad-name"`, 1),
		base + ` {}`, strings.Replace(base, `"listen":`, `"unexpected":true,"listen":`, 1), strings.Repeat(" ", 1<<20) + base,
	}
	for i, s := range cases {
		_, e := ReadConfig(strings.NewReader(s), func(string) string { return "fake-config-secret" })
		if e == nil {
			t.Fatalf("invalid configuration accepted, case %d", i)
		}
		if strings.Contains(e.Error(), "user:secret") {
			t.Fatal("config secret echoed")
		}
	}
	_, e := ReadConfig(strings.NewReader(base), func(string) string { return "" })
	if e == nil {
		t.Fatal("missing credential accepted")
	}
	if b := marshal(c); bytes.Contains(b, []byte(fakeToken)) || bytes.Contains(b, []byte(fakeUpstream)) {
		t.Fatal("resolved credentials serialized")
	}
}
func TestUpstreamFailureMatrixNoReplay(t *testing.T) {
	cases := []struct {
		name, wire, ct string
		status         int
		want           int
		stream         bool
		code           string
	}{
		{name: "http-error", status: 503, want: 502, wire: "credential-echo secret-prompt", code: "upstream_http_error"},
		{name: "json-in-progress", status: 200, want: 502, wire: `{"status":"in_progress","output":[]}`, code: "upstream_response_incomplete"},
		{name: "empty-sse", status: 200, want: 502, ct: "text/event-stream", wire: "", stream: true, code: "upstream_response_incomplete"},
		{name: "truncated-sse", status: 200, want: 200, ct: "text/event-stream", wire: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"x\"}}\n\n", stream: true, code: "upstream_response_incomplete"},
		{name: "failed-sse", status: 200, want: 502, ct: "text/event-stream", wire: "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"message\":\"credential-echo secret-prompt\"}}}\n\n", stream: true, code: "upstream_response_failed"},
		{name: "conflicting-sse", status: 200, want: 502, ct: "text/event-stream", wire: "event: response.completed\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"output\":[]}}\n\n", stream: true, code: "conflicting_upstream_event"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tt.ct != "" {
					w.Header().Set("Content-Type", tt.ct)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.wire)
			}))
			defer s.Close()
			g := startGateway(t, configFor(t, s.URL, "compat"), searchFunc(searchOK))
			body := requestJSON
			if tt.stream {
				body = strings.Replace(body, `"input":"question"`, `"input":"question","stream":true`, 1)
			}
			w := do(t, g, "route0", body, "s")
			if w.Code != tt.want || !strings.Contains(w.Body.String(), tt.code) || calls.Load() != 1 {
				t.Fatalf("%d %s calls=%d", w.Code, w.Body.String(), calls.Load())
			}
			if strings.Contains(w.Body.String(), "secret-prompt") || strings.Contains(w.Body.String(), "credential-echo") || strings.Contains(w.Body.String(), "event: response.completed") {
				t.Fatal("unsafe error or fabricated terminal")
			}
		})
	}
}
func TestSearchLimitAndUsageUnknown(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n%2 == 1 {
			reply(w, []json.RawMessage{searchCall("a")}, "completed")
		} else {
			_ = json.NewEncoder(w).Encode(object{"id": marshal("x"), "status": marshal("completed"), "output": marshal([]json.RawMessage{answer("Done")})})
		}
	}))
	defer srv.Close()
	g := startGateway(t, configFor(t, srv.URL, "compat"), searchFunc(searchOK))
	w := do(t, g, "route0", requestJSON, "s")
	o, _ := decodeObject(w.Body.Bytes())
	if w.Code != 200 || o["usage"] != nil {
		t.Fatalf("missing usage became complete: %s", w.Body.String())
	}
	var attempts atomic.Int32
	s2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		reply(w, []json.RawMessage{searchCall("forever")}, "completed")
	}))
	defer s2.Close()
	c := configFor(t, s2.URL, "compat")
	c.MaxSearches = 1
	var searches atomic.Int32
	g = startGateway(t, c, searchFunc(func(ctx context.Context, q websearch.Request) (websearch.Result, error) {
		searches.Add(1)
		return searchOK(ctx, q)
	}))
	w = do(t, g, "route0", requestJSON, "s")
	if w.Code != 502 || !strings.Contains(w.Body.String(), "search_call_limit") || searches.Load() != 1 || attempts.Load() != 2 {
		t.Fatalf("unbounded loop %d %s", w.Code, w.Body.String())
	}
}
func TestIncompleteDoesNotExecutePartialSearch(t *testing.T) {
	var searches atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, []json.RawMessage{searchCall("partial")}, "incomplete")
	}))
	defer s.Close()
	g := startGateway(t, configFor(t, s.URL, "compat"), searchFunc(func(context.Context, websearch.Request) (websearch.Result, error) {
		searches.Add(1)
		return websearch.Result{}, nil
	}))
	w := do(t, g, "route0", requestJSON, "s")
	o, _ := decodeObject(w.Body.Bytes())
	if w.Code != 200 || str(o["status"]) != "incomplete" || searches.Load() != 0 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
func TestGatewayCancellationAndConcurrency(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	var once sync.Once
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		once.Do(func() { close(entered) })
		<-r.Context().Done()
		close(canceled)
	}))
	defer s.Close()
	c := configFor(t, s.URL, "compat")
	c.MaxConcurrent = 1
	g := startGateway(t, c, searchFunc(searchOK))
	server := httptest.NewServer(g)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/r/route0/v1/responses", strings.NewReader(requestJSON))
	req.Header.Set("Authorization", "Bearer "+fakeToken)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, _ := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not entered")
	}
	w := do(t, g, "route0", requestJSON, "s")
	if w.Code != 429 {
		t.Fatal("concurrency unbounded")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("client cancellation not propagated")
	}
	<-done
}
func TestHistoryLRUPrivacyAndRestart(t *testing.T) {
	c := configFor(t, "http://127.0.0.1:1", "compat")
	c.HistoryMaxEntries = 1
	h := newHistory(c)
	a := historyEntry{id: "first", scope: "s", public: marshal(map[string]string{"id": "first"}), call: marshal("call"), result: marshal("result")}
	b := historyEntry{id: "second", scope: "s", public: marshal(map[string]string{"id": "second"}), call: marshal("call"), result: marshal("result")}
	if h.put(a) != nil || h.put(b) != nil {
		t.Fatal("insert failed")
	}
	if _, e := h.get("first", "s", a.public); e == nil {
		t.Fatal("LRU did not evict")
	}
	if _, e := h.get("second", "s", marshal(map[string]string{"id": "changed"})); e == nil {
		t.Fatal("tampered history accepted")
	}
	if _, e := newHistory(c).get("second", "s", b.public); e == nil {
		t.Fatal("restart resurrected history")
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = h.put(b); _, _ = h.get("second", "s", b.public) }()
	}
	wg.Wait()
	if h.bytes > h.maxBytes || h.lru.Len() > 1 {
		t.Fatal("cache unbounded")
	}
}
func TestNoSearchCompatPassthroughAndRedirect(t *testing.T) {
	var calls atomic.Int32
	native := ` { "model":"test-model", "input":"plain", "large":9007199254740993 } `
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(100) }))
	defer trap.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		if string(b) != native {
			t.Error("plain input bytes changed")
		}
		if r.Header.Get("X-Forwarded-Host") != "" {
			t.Error("untrusted header forwarded")
		}
		if r.Header.Get("Session-Id") != "session-original" {
			t.Error("session lost")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: native\n\n")
	}))
	defer s.Close()
	c := configFor(t, s.URL, "compat")
	g := startGateway(t, c, searchFunc(searchOK))
	w := do(t, g, "route0", native, "session-original")
	if w.Body.String() != "data: native\n\n" || calls.Load() != 1 {
		t.Fatal("plain native response changed")
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Header().Set("Location", trap.URL); w.WriteHeader(307) }))
	defer redirect.Close()
	c.Routes[0].UpstreamBaseURL = redirect.URL + "/v1"
	g = startGateway(t, c, searchFunc(searchOK))
	w = do(t, g, "route0", requestJSON, "s")
	if w.Code != 502 || calls.Load() != 1 {
		t.Fatal("upstream credential redirect followed")
	}
}
