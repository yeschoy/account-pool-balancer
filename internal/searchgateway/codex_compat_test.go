package searchgateway

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"account-pool-balancer/internal/websearch"
)

func TestCodexOptionsPreserveIntent(t *testing.T) {
	valid := []string{
		`{"type":"web_search","external_web_access":true}`,
		`{"type":"web_search_2025_08_26","external_web_access":true,"indexed_web_access":false,"search_context_size":"low","filters":{"allowed_domains":["Docs.Example.com","example.org"]},"user_location":{"type":"approximate"},"search_content_types":["text"]}`,
		`{"type":"web_search","user_location":null,"filters":null}`,
	}
	for _, raw := range valid {
		o, _ := decodeObject([]byte(raw))
		if _, e := parseSearchOptions(o); e != nil {
			t.Fatalf("valid declaration %s: %v", raw, e)
		}
	}
	invalid := []string{
		`{"type":"web_search","external_web_access":false}`, `{"type":"web_search","external_web_access":null}`,
		`{"type":"web_search","indexed_web_access":true}`, `{"type":"web_search","search_content_types":["text","image"]}`,
		`{"type":"web_search","search_context_size":"giant"}`, `{"type":"web_search","user_location":{"type":"approximate","city":"Paris"}}`,
		`{"type":"web_search","filters":{"allowed_domains":["example.com/path"]}}`,
		`{"type":"web_search","filters":{"allowed_domains":["*.example.com"]}}`,
		`{"type":"web_search","filters":{"allowed_domains":["127.0.0.1"]}}`,
		`{"type":"web_search","filters":{"allowed_domains":null}}`,
	}
	for _, raw := range invalid {
		o, _ := decodeObject([]byte(raw))
		if _, e := parseSearchOptions(o); e == nil {
			t.Fatalf("unsafe declaration accepted: %s", raw)
		}
	}
}
func TestOptionsReachSearchBackendAndCodexHistoryDropsSources(t *testing.T) {
	var modelCalls, searches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := modelCalls.Add(1)
		if n == 1 {
			reply(w, []json.RawMessage{searchCall("one")}, "completed")
		} else {
			reply(w, []json.RawMessage{answer("文档 [Docs](https://example.com/docs)")}, "completed")
		}
	}))
	defer srv.Close()
	c := configFor(t, srv.URL, "compat")
	c.Routes[0].AllowedModels = append(c.Routes[0].AllowedModels, "other-model")
	g := startGateway(t, c, searchFunc(func(ctx context.Context, q websearch.Request) (websearch.Result, error) {
		searches.Add(1)
		if q.ContextSize != "low" || len(q.AllowedDomains) != 1 || q.AllowedDomains[0] != "example.com" {
			t.Errorf("options not applied: %+v", q)
		}
		return searchOK(ctx, q)
	}))
	body := strings.Replace(requestJSON, `{"type":"web_search"}`, `{"type":"web_search","external_web_access":true,"search_context_size":"low","filters":{"allowed_domains":["Example.com"]}}`, 1)
	w := do(t, g, "route0", body, "codex-thread")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	o, _ := decodeObject(w.Body.Bytes())
	out, _ := array(o["output"])
	public, _ := decodeObject(out[0])
	action, _ := decodeObject(public["action"])
	delete(action, "sources")
	public["action"] = marshal(action)
	public["encrypted_content"] = marshal(nil)
	public["internal_chat_message_metadata_passthrough"] = marshal(nil)
	out[0] = marshal(public)
	input := append([]json.RawMessage{marshal(map[string]any{"role": "user", "content": "question"})}, out...)
	next := object{"model": marshal("test-model"), "input": marshal(input)}
	w = do(t, g, "route0", string(marshal(next)), "codex-thread")
	if w.Code != 200 || modelCalls.Load() != 3 || searches.Load() != 1 {
		t.Fatalf("Codex history failed: %d %s", w.Code, w.Body.String())
	}
	next["model"] = marshal("other-model")
	w = do(t, g, "route0", string(marshal(next)), "codex-thread")
	if w.Code != 409 || modelCalls.Load() != 3 {
		t.Fatal("cross-model history accepted")
	}
	action["query"] = marshal("changed search")
	public["action"] = marshal(action)
	out[0] = marshal(public)
	next["model"] = marshal("test-model")
	next["input"] = marshal(append([]json.RawMessage{input[0]}, out...))
	w = do(t, g, "route0", string(marshal(next)), "codex-thread")
	if w.Code != 409 {
		t.Fatal("changed authoritative action accepted")
	}
}
func TestTrueSourceCitationsUnicodeAndSpoofProtection(t *testing.T) {
	text := "中文😀 [Docs](https://example.com/docs) https://example.com/docs-FAKE https://other.com"
	part := object{"type": marshal("output_text"), "text": marshal(text), "annotations": marshal([]any{})}
	updated, added := citedPart(part, []websearch.Source{{Title: "Docs", URL: "https://example.com/docs"}})
	if str(updated["text"]) != text || len(added) != 1 {
		t.Fatalf("citations changed text or matched fake prefix: %s", marshal(updated))
	}
	a, _ := decodeObject(added[0])
	start, _ := intField(a, "start_index")
	end, _ := intField(a, "end_index")
	if string([]rune(text)[start:end]) != "https://example.com/docs" {
		t.Fatal("Unicode offsets drift")
	}
	_, again := citedPart(updated, []websearch.Source{{Title: "Docs", URL: "https://example.com/docs"}})
	if len(again) != 0 {
		t.Fatal("existing citation duplicated")
	}
}
func TestFunctionDoneThenTextKeepsStreamAndCitations(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		output := []json.RawMessage{}
		if n == 1 {
			call := searchCall("a")
			initial, _ := decodeObject(call)
			initial["arguments"] = marshal("")
			sendFrame(w, "response.output_item.added", object{"output_index": marshal(0), "item": marshal(initial)})
			sendFrame(w, "response.output_item.done", object{"output_index": marshal(0), "item": call})
			output = append(output, call)
		}
		at := len(output)
		msg := answer("Read https://example.com/docs")
		initial, _ := decodeObject(msg)
		initial["content"] = marshal([]any{})
		sendFrame(w, "response.output_item.added", object{"output_index": marshal(at), "item": marshal(initial)})
		sendFrame(w, "response.output_text.delta", object{"output_index": marshal(at), "item_id": initial["id"], "content_index": marshal(0), "delta": marshal("Read https://example.com/docs")})
		sendFrame(w, "response.output_item.done", object{"output_index": marshal(at), "item": msg})
		output = append(output, msg)
		sendFrame(w, "response.completed", object{"response": marshal(map[string]any{"status": "completed", "output": output, "usage": map[string]int{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13}})})
	}))
	defer s.Close()
	g := startGateway(t, configFor(t, s.URL, "compat"), searchFunc(searchOK))
	body := strings.Replace(requestJSON, `"input":"question"`, `"input":"question","stream":true`, 1)
	w := do(t, g, "route0", body, "s")
	if strings.Contains(w.Body.String(), "response.failed") {
		t.Fatal(w.Body.String())
	}
	reader := bufio.NewReader(w.Body)
	annotations, completed := 0, 0
	lastSequence := -1
	for {
		typ, data, e := readEvent(reader, 1<<20)
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		o, _ := decodeObject(data)
		seq, _ := intField(o, "sequence_number")
		if int(seq) != lastSequence+1 {
			t.Fatal("sequence not global")
		}
		lastSequence = int(seq)
		if typ == "response.output_text.annotation.added" {
			annotations++
			if !strings.Contains(string(o["annotation"]), `"type":"url_citation"`) {
				t.Fatal("wrong citation")
			}
		}
		if typ == "response.completed" {
			completed++
			final, _ := decodeObject(o["response"])
			out, _ := array(final["output"])
			if len(out) != 3 {
				t.Fatalf("wrong combined output %s", final["output"])
			}
			ws, _ := decodeObject(out[0])
			if str(ws["type"]) != "web_search_call" {
				t.Fatal("search position reordered")
			}
			if !strings.Contains(string(out[2]), "url_citation") {
				t.Fatal("missing final citation")
			}
		}
	}
	if annotations != 1 || completed != 1 || calls.Load() != 2 {
		t.Fatalf("annotations=%d completed=%d calls=%d", annotations, completed, calls.Load())
	}
}
