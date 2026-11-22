package searchgateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in synthetic integration. No credentials/default network access in tests.
// Run against ONE fixed upstream account, never a public production client route.
func TestLiveResponsesSearch(t *testing.T) {
	base, key := os.Getenv("SEARCH_GATEWAY_LIVE_BASE"), os.Getenv("SEARCH_GATEWAY_LIVE_MODEL_KEY")
	searchKey := os.Getenv("SEARCH_GATEWAY_LIVE_SEARCH_KEY")
	if base == "" || key == "" || searchKey == "" {
		t.Skip("explicit live credentials required")
	}
	model := os.Getenv("SEARCH_GATEWAY_LIVE_MODEL")
	if model == "" {
		model = "deepseek-v4-1-flash-1017"
	}
	endpoint := os.Getenv("SEARCH_GATEWAY_LIVE_SEARCH_ENDPOINT")
	if endpoint == "" {
		t.Fatal("explicit search endpoint required")
	}
	c := Config{RequestTimeoutSec: 90, MaxSearches: 1, Search: SearchConfig{Endpoint: endpoint, APIKeyEnv: "SEARCH", TimeoutSec: 20, MaxResults: 3}, Routes: []Route{{ID: "live", TokenEnv: "ROUTE", UpstreamBaseURL: base, UpstreamKeyEnv: "MODEL", Mode: "compat", AllowedModels: []string{model}}}}
	env := func(k string) string {
		switch k {
		case "ROUTE":
			return "fake-live-only-route-token"
		case "MODEL":
			return key
		case "SEARCH":
			return searchKey
		}
		return ""
	}
	resolved, e := ReadConfig(bytes.NewReader(marshal(c)), env)
	if e != nil {
		t.Fatal("live configuration invalid")
	}
	var calls atomic.Int32
	client := &http.Client{Transport: liveTransport{base: http.DefaultTransport, calls: &calls, modelHost: base, t: t}}
	g, e := New(resolved, Options{Client: client})
	if e != nil {
		t.Fatal(e)
	}
	input := marshal([]map[string]any{{"role": "user", "content": "Use web search exactly once for Mistral Responses API documentation. Then give one short sentence and one Markdown link to an actual returned documentation source."}})
	body := object{"model": marshal(model), "input": input, "stream": marshal(true), "store": marshal(false), "max_output_tokens": marshal(768), "reasoning": marshal(map[string]string{"effort": "low"}), "tools": marshal([]any{map[string]any{"type": "web_search", "external_web_access": true, "search_context_size": "low", "filters": map[string]any{"allowed_domains": []string{"docs.mistral.ai"}}}}), "tool_choice": marshal(map[string]string{"type": "web_search"}), "include": marshal([]string{"web_search_call.action.sources"})}
	execute := func(payload object) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		req := httptest.NewRequest("POST", "/r/live/v1/responses", bytes.NewReader(marshal(payload))).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer fake-live-only-route-token")
		req.Header.Set("Session-Id", "synthetic-live-docs-test")
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)
		return w
	}
	start := time.Now()
	w := execute(body)
	final, events := liveTerminal(t, w)
	if str(final["status"]) != "completed" {
		t.Fatal("live response did not complete")
	}
	output, e := array(final["output"])
	if e != nil {
		t.Fatal("missing output")
	}
	searches, citations, texts := 0, 0, 0
	for _, raw := range output {
		o, _ := decodeObject(raw)
		if str(o["type"]) == "web_search_call" {
			searches++
			action, _ := decodeObject(o["action"])
			if !bytes.Contains(action["sources"], []byte("docs.mistral.ai")) {
				t.Fatal("search sources missing")
			}
		}
		if str(o["type"]) == "message" {
			parts, _ := array(o["content"])
			for _, part := range parts {
				p, _ := decodeObject(part)
				if str(p["text"]) != "" {
					texts++
				}
				a, _ := array(p["annotations"])
				for _, v := range a {
					a, _ := decodeObject(v)
					if str(a["type"]) == "url_citation" {
						citations++
					}
				}
			}
		}
	}
	if searches != 1 || texts == 0 || citations == 0 || calls.Load() != 2 {
		t.Fatalf("live output shape: searches=%d texts=%d citations=%d modelCalls=%d", searches, texts, citations, calls.Load())
	}
	t.Logf("synthetic live search passed: events=%d calls=%d citations=%d elapsedMs=%d usagePresent=%t", events, calls.Load(), citations, time.Since(start).Milliseconds(), final["usage"] != nil)
	// Model the actual Codex public schema: it omits action.sources on replay.
	for i, raw := range output {
		o, _ := decodeObject(raw)
		if str(o["type"]) == "web_search_call" {
			a, _ := decodeObject(o["action"])
			delete(a, "sources")
			o["action"] = marshal(a)
			output[i] = marshal(o)
		}
	}
	history, _ := array(input)
	history = append(history, output...)
	history = append(history, marshal(map[string]any{"role": "user", "content": "Repeat only the documentation URL you just used. Do not search again."}))
	body["input"] = marshal(history)
	body["tool_choice"] = marshal("none")
	body["max_output_tokens"] = marshal(512)
	w = execute(body)
	follow, _ := liveTerminal(t, w)
	if str(follow["status"]) != "completed" || calls.Load() != 3 {
		t.Fatal("live history follow-up failed")
	}
	t.Log("synthetic live history replay passed; exactly 3 fixed-account model calls, no real client or NewAPI claimed")
}

type liveTransport struct {
	base      http.RoundTripper
	calls     *atomic.Int32
	modelHost string
	t         *testing.T
}

func (l liveTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasPrefix(r.URL.String(), l.modelHost) {
		if l.calls.Add(1) > 3 {
			return nil, fmt.Errorf("live test model request cap")
		}
	}
	resp, e := l.base.RoundTrip(r)
	if resp != nil {
		l.t.Logf("live HTTP diagnostic: model=%t status=%d contentType=%s", strings.HasPrefix(r.URL.String(), l.modelHost), resp.StatusCode, resp.Header.Get("Content-Type"))
		if resp.StatusCode >= 400 { // Only coordinates/types/codes, never free-form messages or inputs.
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(b))
			var o map[string]json.RawMessage
			if json.Unmarshal(b, &o) == nil {
				var errObj map[string]json.RawMessage
				if json.Unmarshal(o["error"], &errObj) == nil {
					l.t.Logf("live safe error: code=%s type=%s", safeDiagnosticCode(errObj["code"]), safeDiagnosticCode(errObj["type"]))
				}
			}
		}
	}
	return resp, e
}
func safeDiagnosticCode(b json.RawMessage) string {
	s := str(b)
	if s == "" {
		s = string(b)
	}
	if len(s) > 64 {
		return "omitted"
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "omitted"
		}
	}
	return s
}
func liveTerminal(t *testing.T, w *httptest.ResponseRecorder) (object, int) {
	t.Helper()
	if w.Code != 200 {
		o, _ := decodeObject(w.Body.Bytes())
		err, _ := decodeObject(o["error"])
		t.Fatalf("live gateway HTTP%d code=%s", w.Code, str(err["code"]))
	}
	reader := bufio.NewReader(bytes.NewReader(w.Body.Bytes()))
	count := 0
	for {
		typ, data, e := readEvent(reader, 1<<20)
		if e != nil {
			t.Fatalf("live stream ended without completion: %T", e)
		}
		count++
		o, _ := decodeObject(data)
		if typ == "response.failed" {
			response, _ := decodeObject(o["response"])
			err, _ := decodeObject(response["error"])
			t.Fatalf("live gateway failed: code=%s", str(err["code"]))
		}
		if typ == "response.completed" || typ == "response.incomplete" {
			response, _ := decodeObject(o["response"])
			return response, count
		}
	}
}
