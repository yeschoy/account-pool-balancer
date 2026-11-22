package searchgateway

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/websearch"
)

const fakeToken = "fake-gateway-route-token"
const fakeUpstream = "fake-upstream-client-key"

type searchFunc func(context.Context, websearch.Request) (websearch.Result, error)

func (f searchFunc) Search(c context.Context, r websearch.Request) (websearch.Result, error) {
	return f(c, r)
}
func configFor(t *testing.T, base string, modes ...string) Config {
	t.Helper()
	routes := []Route{}
	for i, m := range modes {
		routes = append(routes, Route{ID: fmt.Sprintf("route%d", i), TokenEnv: "ROUTE_TOKEN", UpstreamBaseURL: base + "/v1", UpstreamKeyEnv: "UPSTREAM_KEY", Mode: m, AllowedModels: []string{"test-model"}})
	}
	c, err := ReadConfig(bytes.NewReader(marshal(Config{Routes: routes, Search: SearchConfig{Endpoint: "https://search.example/api/search", APIKeyEnv: "SEARCH_KEY"}})), func(k string) string {
		switch k {
		case "ROUTE_TOKEN":
			return fakeToken
		case "UPSTREAM_KEY":
			return fakeUpstream
		case "SEARCH_KEY":
			return "fake-search-backend-key"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func startGateway(t *testing.T, c Config, search websearch.Backend) *Gateway {
	t.Helper()
	g, e := New(c, Options{Search: search})
	if e != nil {
		t.Fatal(e)
	}
	return g
}
func do(t *testing.T, g http.Handler, route, body, session string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/r/"+route+"/v1/responses", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+fakeToken)
	if session != "" {
		r.Header.Set("Session-Id", session)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}
func reply(w http.ResponseWriter, output []json.RawMessage, status string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(object{"id": marshal("upstream-response"), "object": marshal("response"), "status": marshal(status), "model": marshal("test-model"), "output": marshal(output), "usage": marshal(map[string]any{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13, "input_tokens_details": map[string]int{"cached_tokens": 2}})})
}
func searchCall(callID string) json.RawMessage {
	return marshal(map[string]any{"id": "fc-" + callID, "type": "function_call", "name": searchFunction, "call_id": callID, "arguments": `{"query":"public documentation"}`, "status": "completed"})
}
func answer(text string) json.RawMessage {
	return marshal(map[string]any{"id": "msg1", "type": "message", "role": "assistant", "status": "completed", "content": []map[string]any{{"type": "output_text", "text": text, "annotations": []any{}}}})
}
func searchOK(_ context.Context, q websearch.Request) (websearch.Result, error) {
	return websearch.Result{Sources: []websearch.Source{{Title: "Docs", URL: "https://example.com/docs", Content: "Public documentation."}}}, nil
}

const requestJSON = `{"model":"test-model","input":"question","tools":[{"type":"web_search"}],"include":["web_search_call.action.sources"],"metadata":{"large":9007199254740993},"prompt_cache_key":"cache-unchanged"}`

func TestTwoRoundJSONAndHistoryIsolation(t *testing.T) {
	var calls atomic.Int32
	var uploadsMu sync.Mutex
	var uploads []object
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fakeUpstream || r.URL.Path != "/v1/responses" {
			t.Error("wrong credential/target")
		}
		b, _ := io.ReadAll(r.Body)
		o, e := decodeObject(b)
		if e != nil {
			t.Error(e)
		}
		uploadsMu.Lock()
		uploads = append(uploads, o)
		uploadsMu.Unlock()
		n := calls.Add(1)
		if n == 1 {
			if !bytes.Contains(b, []byte(`9007199254740993`)) || !bytes.Contains(b, []byte(`cache-unchanged`)) {
				t.Error("request fields changed")
			}
			reply(w, []json.RawMessage{searchCall("one")}, "completed")
		} else {
			reply(w, []json.RawMessage{answer("See [Docs](https://example.com/docs).")}, "completed")
		}
	}))
	defer upstream.Close()
	var searches atomic.Int32
	g := startGateway(t, configFor(t, upstream.URL, "compat", "compat"), searchFunc(func(ctx context.Context, q websearch.Request) (websearch.Result, error) {
		searches.Add(1)
		return searchOK(ctx, q)
	}))
	w := do(t, g, "route0", requestJSON, "session-A")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	result, e := decodeObject(w.Body.Bytes())
	if e != nil {
		t.Fatal(e)
	}
	out, _ := array(result["output"])
	if len(out) != 2 || searches.Load() != 1 || calls.Load() != 2 {
		t.Fatalf("out=%s calls=%d searches=%d", result["output"], calls.Load(), searches.Load())
	}
	ws, _ := decodeObject(out[0])
	if str(ws["type"]) != "web_search_call" || !bytes.Contains(ws["action"], []byte("https://example.com/docs")) {
		t.Fatal("missing real search source")
	}
	usage, _ := decodeObject(result["usage"])
	if n, _ := intField(usage, "input_tokens"); n != 20 {
		t.Fatalf("usage=%s", result["usage"])
	}
	uploadsMu.Lock()
	second := uploads[1]
	uploadsMu.Unlock()
	input, _ := array(second["input"])
	if len(input) != 3 || !bytes.Contains(input[2], []byte("untrusted_web_results")) {
		t.Fatalf("continuation=%s", second["input"])
	}
	follow := object{"model": marshal("test-model"), "input": marshal(append([]json.RawMessage{marshal(map[string]any{"role": "user", "content": "question"})}, out...)), "tools": marshal([]any{map[string]string{"type": "web_search"}})}
	w = do(t, g, "route0", string(marshal(follow)), "session-A")
	if w.Code != 200 || calls.Load() != 3 || searches.Load() != 1 {
		t.Fatalf("follow: %d %s", w.Code, w.Body.String())
	}
	for _, scope := range []struct{ route, session string }{{"route1", "session-A"}, {"route0", "session-B"}} {
		w = do(t, g, scope.route, string(marshal(follow)), scope.session)
		if w.Code != 409 {
			t.Fatalf("scope leak: %d %s", w.Code, w.Body.String())
		}
	}
	g.history.mu.Lock()
	for _, el := range g.history.items {
		v := el.Value.(historyEntry)
		v.expires = time.Now().Add(-time.Second)
		el.Value = v
	}
	g.history.mu.Unlock()
	w = do(t, g, "route0", string(marshal(follow)), "session-A")
	if w.Code != 409 || calls.Load() != 3 {
		t.Fatal("expired history contacted upstream")
	}
}
func TestMixedToolsReturnedToClient(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		reply(w, []json.RawMessage{searchCall("one"), marshal(map[string]any{"type": "function_call", "id": "fc-client", "call_id": "client-one", "name": "read_file", "arguments": `{"path":"README.md"}`, "status": "completed"})}, "completed")
	}))
	defer srv.Close()
	g := startGateway(t, configFor(t, srv.URL, "compat"), searchFunc(searchOK))
	body := strings.Replace(requestJSON, `[{"type":"web_search"}]`, `[{"type":"web_search"},{"type":"function","name":"read_file","parameters":{"type":"object"}}]`, 1)
	w := do(t, g, "route0", body, "session")
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"name":"read_file"`) || strings.Contains(w.Body.String(), searchFunction) {
		t.Fatal("internal function leaked or client tool lost")
	}
}
func TestRequestGatesNoBackendCalls(t *testing.T) {
	var called atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called.Add(1) }))
	defer srv.Close()
	g := startGateway(t, configFor(t, srv.URL, "compat", "disabled"), searchFunc(searchOK))
	cases := []struct {
		name, route, body string
		status            int
	}{
		{"disabled", "route1", requestJSON, 400}, {"model", "route0", strings.Replace(requestJSON, "test-model", "other-model", 1), 400},
		{"duplicate", "route0", `{"model":"test-model","mo\u0064el":"test-model"}`, 400},
		{"search-options", "route0", strings.Replace(requestJSON, `{"type":"web_search"}`, `{"type":"web_search","filters":{"unknown_domains":["example.com"]}}`, 1), 400},
		{"preview", "route0", strings.Replace(requestJSON, "web_search\"", "web_search_preview\"", 1), 400},
		{"continuation", "route0", strings.Replace(requestJSON, `"input":"question"`, `"input":"question","previous_response_id":"old"`, 1), 400},
		{"reserved", "route0", strings.Replace(requestJSON, `[{"type":"web_search"}]`, `[{"type":"web_search"},{"type":"function","name":"apb_web_search"}]`, 1), 400},
		{"history", "route0", `{"model":"test-model","input":[{"type":"web_search_call","id":"ws_unknown"}]}`, 409},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			w := do(t, g, tt.route, tt.body, "s")
			if w.Code != tt.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	r := httptest.NewRequest("POST", "/r/route0/v1/responses", strings.NewReader(requestJSON))
	r.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 401 || called.Load() != 0 {
		t.Fatal("gate contacted backend")
	}
}
func TestNativeBytePreservationAndSecondUpstream(t *testing.T) {
	var first, second atomic.Int32
	rawResponse := `{ "id":"native", "large":9007199254740993 }`
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first.Add(1)
		b, _ := io.ReadAll(r.Body)
		if string(b) != requestJSON {
			t.Error("native request bytes changed")
		}
		_, _ = io.WriteString(w, rawResponse)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"type":"web_search"`) {
			t.Error("hosted tool leaked upstream")
		}
		reply(w, []json.RawMessage{answer("Other provider")}, "completed")
	}))
	defer b.Close()
	c := configFor(t, a.URL, "native", "compat")
	c.Routes[1].UpstreamBaseURL = b.URL + "/v1"
	g := startGateway(t, c, searchFunc(searchOK))
	w := do(t, g, "route0", requestJSON, "s")
	if w.Body.String() != rawResponse || w.Code != 200 {
		t.Fatal("native response changed")
	}
	w = do(t, g, "route1", requestJSON, "s")
	if w.Code != 200 || first.Load() != 1 || second.Load() != 1 {
		t.Fatal("second provider routing failed")
	}
}
func TestSearchFailureNoRetryAndPrivacy(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		reply(w, []json.RawMessage{searchCall("one")}, "completed")
	}))
	defer srv.Close()
	var logs bytes.Buffer
	c := configFor(t, srv.URL, "compat")
	g, e := New(c, Options{Search: searchFunc(func(context.Context, websearch.Request) (websearch.Result, error) {
		return websearch.Result{}, &websearch.Error{Code: "search_upstream_error", Status: 401}
	}), Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if e != nil {
		t.Fatal(e)
	}
	for _, stream := range []bool{false, true} {
		body := requestJSON
		if stream {
			body = strings.Replace(body, `"input":"question"`, `"input":"question","stream":true`, 1)
		}
		w := do(t, g, "route0", body, "private-session")
		if stream {
			if w.Code != 200 || !strings.Contains(w.Body.String(), "response.failed") || strings.Contains(w.Body.String(), "response.completed") {
				t.Fatal(w.Body.String())
			}
		} else if w.Code != 502 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	if calls.Load() != 2 {
		t.Fatal("search failure replayed inference")
	}
	for _, secret := range []string{fakeToken, fakeUpstream, "private-session", "public documentation", "question"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("private value logged: %s", secret)
		}
	}
}

func sendFrame(w http.ResponseWriter, typ string, o object) {
	o = clone(o)
	o["type"] = marshal(typ)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, marshal(o))
	w.(http.Flusher).Flush()
}
func TestSSEProgressiveOutputTwoRoundsAndSingleLifecycle(t *testing.T) {
	var calls atomic.Int32
	gate := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		sendFrame(w, "response.created", object{"response": marshal(map[string]any{"id": "upstream-response"})})
		if n == 1 {
			item, _ := decodeObject(searchCall("one"))
			initial := clone(item)
			initial["arguments"] = marshal("")
			sendFrame(w, "response.output_item.added", object{"output_index": marshal(0), "item": marshal(initial)})
			sendFrame(w, "response.function_call_arguments.delta", object{"output_index": marshal(0), "item_id": item["id"], "delta": item["arguments"]})
			sendFrame(w, "response.output_item.done", object{"output_index": marshal(0), "item": marshal(item)})
			sendFrame(w, "response.completed", object{"response": marshal(map[string]any{"id": "upstream-one", "status": "completed", "output": []json.RawMessage{searchCall("one")}, "usage": map[string]int{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13}})})
			return
		}
		item, _ := decodeObject(answer("Hello"))
		initial := clone(item)
		initial["content"] = marshal([]any{})
		initial["status"] = marshal("in_progress")
		sendFrame(w, "response.output_item.added", object{"output_index": marshal(0), "item": marshal(initial)})
		sendFrame(w, "response.content_part.added", object{"output_index": marshal(0), "item_id": item["id"], "content_index": marshal(0), "part": marshal(map[string]any{"type": "output_text", "text": "", "annotations": []any{}})})
		sendFrame(w, "response.output_text.delta", object{"output_index": marshal(0), "item_id": item["id"], "content_index": marshal(0), "delta": marshal("Hello")})
		select {
		case <-gate:
		case <-r.Context().Done():
			return
		}
		sendFrame(w, "response.output_item.done", object{"output_index": marshal(0), "item": marshal(item)})
		sendFrame(w, "response.completed", object{"response": marshal(map[string]any{"id": "upstream-two", "status": "completed", "output": []json.RawMessage{answer("Hello")}, "usage": map[string]int{"input_tokens": 20, "output_tokens": 4, "total_tokens": 24}})})
		// Terminal must close this call, without waiting for connection EOF.
		<-r.Context().Done()
	}))
	defer upstream.Close()
	g := startGateway(t, configFor(t, upstream.URL, "compat"), searchFunc(searchOK))
	server := httptest.NewServer(g)
	defer server.Close()
	body := strings.Replace(requestJSON, `"input":"question"`, `"input":"question","stream":true`, 1)
	req, _ := http.NewRequest("POST", server.URL+"/r/route0/v1/responses", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+fakeToken)
	client := http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	events := []object{}
	types := []string{}
	seenDelta := false
	for {
		typ, data, err := readEvent(reader, 1<<20)
		if err != nil {
			t.Fatalf("before delta %v", err)
		}
		o, _ := decodeObject(data)
		events = append(events, o)
		types = append(types, typ)
		if typ == "response.output_text.delta" {
			seenDelta = true
			if string(o["output_index"]) != "1" {
				t.Fatal("output index not remapped")
			}
			break
		}
	}
	if !seenDelta {
		t.Fatal("not progressive")
	}
	release()
	for {
		typ, data, err := readEvent(reader, 1<<20)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		o, _ := decodeObject(data)
		events = append(events, o)
		types = append(types, typ)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected inference calls")
	}
	created, completed := 0, 0
	for i, o := range events {
		if string(o["sequence_number"]) != fmt.Sprint(i) {
			t.Fatal("sequence drift")
		}
		switch types[i] {
		case "response.created":
			created++
		case "response.completed":
			completed++
			r, _ := decodeObject(o["response"])
			u, _ := decodeObject(r["usage"])
			if n, _ := intField(u, "input_tokens"); n != 30 {
				t.Fatal("usage not aggregated")
			}
			if !strings.HasPrefix(str(r["id"]), "resp_apb_") {
				t.Fatal("upstream id leaked")
			}
		}
		if bytes.Contains(marshal(o), []byte(searchFunction)) {
			t.Fatal("internal function event leaked")
		}
	}
	if created != 1 || completed != 1 {
		t.Fatalf("created=%d completed=%d", created, completed)
	}
}
