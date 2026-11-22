package searchgateway

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONUpstreamEmitsRealTextForStreamCaller(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reply(w, []json.RawMessage{answer("Actual complete text")}, "completed")
	}))
	defer s.Close()
	g := startGateway(t, configFor(t, s.URL, "compat"), searchFunc(searchOK))
	body := strings.Replace(requestJSON, `"input":"question"`, `"input":"question","stream":true`, 1)
	w := do(t, g, "route0", body, "s")
	reader := bufio.NewReader(w.Body)
	delta, completed := 0, 0
	for {
		typ, data, e := readEvent(reader, 1<<20)
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		o, _ := decodeObject(data)
		if typ == "response.output_text.delta" {
			delta++
			if str(o["delta"]) != "Actual complete text" {
				t.Fatal("fabricated text")
			}
		}
		if typ == "response.completed" {
			completed++
		}
	}
	if w.Code != 200 || delta != 1 || completed != 1 {
		t.Fatalf("delta=%d complete=%d %s", delta, completed, w.Body.String())
	}
}
func TestUnsupportedInterleavedFunctionStreamFailsExplicitly(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		call, _ := decodeObject(searchCall("a"))
		sendFrame(w, "response.output_item.added", object{"output_index": marshal(0), "item": marshal(call)})
		sendFrame(w, "response.output_item.added", object{"output_index": marshal(1), "item": answer("Do not reorder")})
	}))
	defer s.Close()
	g := startGateway(t, configFor(t, s.URL, "compat"), searchFunc(searchOK))
	body := strings.Replace(requestJSON, `"input":"question"`, `"input":"question","stream":true`, 1)
	w := do(t, g, "route0", body, "s")
	if !strings.Contains(w.Body.String(), "unsupported_interleaved_tool_stream") || strings.Contains(w.Body.String(), "event: response.completed") {
		t.Fatal(w.Body.String())
	}
}
