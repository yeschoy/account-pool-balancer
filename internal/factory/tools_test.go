package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

func request(schema string) []byte {
	return []byte(`{ "model":"claude-opus-5-5","system":"Keep my identity and instructions","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_original","name":"task","input":{"n":900719925474099312345}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_original","content":"original tool result"}]}],"tools":[{"name":"task","cache_control":{"type":"ephemeral"},"input_schema":` + schema + `,"input_examples":[{"n":900719925474099312345}]}] }`)
}
func TestToolSchemaCompositionsRoundTrip(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		t.Run(keyword, func(t *testing.T) {
			body, names, err := WrapTools(request(`{"` + keyword + `":[{"type":"object"}]}`))
			if err != nil {
				t.Fatal(err)
			}
			if !names["task"] || !bytes.Contains(body, []byte(`"arguments":{"n":900719925474099312345}`)) || !bytes.Contains(body, []byte(`"required":["arguments"]`)) {
				t.Fatalf("invalid envelope %s", body)
			}
			for _, keep := range []string{"call_original", "original tool result", "Keep my identity and instructions", `"cache_control":{"type":"ephemeral"}`} {
				if !bytes.Contains(body, []byte(keep)) {
					t.Fatal("lost", keep)
				}
			}
			reply := []byte(`{"id":"msg_1","type":"message","content":[{"type":"tool_use","id":"call_new","name":"task","input":{"arguments":{"n":900719925474099312345}}},{"type":"tool_use","id":"call_ordinary","name":"read","input":{"arguments":"caller-owned"}}],"usage":{"input_tokens":7}}`)
			out, err := UnwrapReply(reply, names)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(out, []byte(`"input":{"n":900719925474099312345}`)) || !bytes.Contains(out, []byte(`"input":{"arguments":"caller-owned"}`)) {
				t.Fatalf("corrupt args %s", out)
			}
			if !bytes.Contains(out, []byte(`"input_tokens":7`)) {
				t.Fatal("usage lost")
			}
		})
	}
}
func TestToolSchemaReferencesAndOrdinaryData(t *testing.T) {
	schema := `{"$schema":"https://json-schema.org/draft/2020-12/schema","anyOf":[{"$ref":"#/$defs/value"},{"$ref":"#%2F$defs%2Fvalue"},{"$ref":"#anchor"},{"$dynamicRef":"#"}],"$defs":{"child":{"$id":"child.json","$ref":"#/$defs/own"},"value":{"$anchor":"anchor","type":"string"}},"properties":{"literal":{"const":{"$ref":"#literal"},"default":{"anyOf":[]}}}}`
	out, _, err := WrapTools(request(schema))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`"$ref":"#/properties/arguments/$defs/value"`, `"$ref":"#/properties/arguments%2F$defs%2Fvalue"`, `"$ref":"#anchor"`, `"$dynamicRef":"#/properties/arguments"`, `"$ref":"#/$defs/own"`, `"const":{"$ref":"#literal"}`, `"default":{"anyOf":[]}`} {
		if !bytes.Contains(out, []byte(value)) {
			t.Fatalf("missing %s in %s", value, out)
		}
	}
	for _, schema := range []string{`{"$id":"https://example.test/tool.json","oneOf":[{"$ref":"#/$defs/a"}]}`, `{"$schema":"http://json-schema.org/draft-04/schema#","id":"legacy.json","oneOf":[{"$ref":"#/$defs/a"}]}`} {
		out, _, err := WrapTools(request(schema))
		if err != nil || !bytes.Contains(out, []byte(`"$ref":"#/$defs/a"`)) {
			t.Fatalf("resource scope changed %s %v", out, err)
		}
	}
	for _, schema := range []string{`{"anyOf":[{"$recursiveRef":"#"}]}`, `{"oneOf":[],"$recursiveAnchor":true}`, `{"allOf":[{"$ref":"#%GG"}]}`} {
		if _, _, err := WrapTools(request(schema)); err == nil {
			t.Fatal("unsupported schema accepted", schema)
		}
	}
}
func TestOrdinaryToolBytesAndInvalidEnvelopes(t *testing.T) {
	in := request(`{"type":"object","properties":{"x":{"anyOf":[{"type":"string"}]}}}`)
	out, names, err := WrapTools(in)
	if err != nil || len(names) != 0 || !bytes.Equal(in, out) {
		t.Fatal("ordinary bytes changed", err)
	}
	for _, input := range []string{`{"wrong":{}}`, `{"arguments":{},"extra":true}`, `null`} {
		if _, err := UnwrapReply([]byte(`{"content":[{"type":"tool_use","name":"task","input":`+input+`}]}`), ToolNames{"task": true}); err == nil {
			t.Fatal("invalid envelope accepted", input)
		}
	}
	out, err = UnwrapReply([]byte(" {\"content\":[]} "), ToolNames{"task": true})
	if err != nil || string(out) != " {\"content\":[]} " {
		t.Fatal("unrelated reply changed")
	}
}
func event(typ string, v map[string]any, eol string) string {
	v["type"] = typ
	b, _ := json.Marshal(v)
	return "event: " + typ + eol + "data: " + string(b) + eol + eol
}

type shortReader struct{ io.ReadCloser }

func (r shortReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.ReadCloser.Read(p)
}
func TestStreamingToolRoundTripAndInterleaving(t *testing.T) {
	for _, eol := range []string{"\n", "\r\n", "\r"} {
		t.Run(fmt.Sprintf("%q", eol), func(t *testing.T) {
			wrapped := `{"arguments":{"n":900719925474099312345,"text":"汉字 😀"}}`
			stream := ": keep-alive" + eol + eol + event("message_start", map[string]any{"message": map[string]any{"id": "msg_1", "usage": map[string]any{"input_tokens": 7}}}, eol)
			stream += "id: event_original" + eol + "retry: 1000" + eol + event("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "tool_use", "id": "call_original", "name": "task", "input": map[string]any{}}}, eol)
			stream += event("content_block_start", map[string]any{"index": 2, "content_block": map[string]any{"type": "tool_use", "id": "read_original", "name": "read", "input": map[string]any{}}}, eol)
			// Split JSON syntax, not UTF-8 code points; the reader splits all bytes.
			for _, piece := range []string{wrapped[:12], wrapped[12:40], wrapped[40:]} {
				stream += event("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": piece}}, eol)
			}
			ordinary := event("content_block_delta", map[string]any{"index": 2, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"arguments":"caller-owned"}`}}, eol)
			stream += ordinary + event("content_block_stop", map[string]any{"index": 2}, eol) + event("content_block_stop", map[string]any{"index": 1}, eol) + event("message_delta", map[string]any{"usage": map[string]any{"output_tokens": 9}}, eol) + event("message_stop", map[string]any{}, eol)
			out, err := io.ReadAll(UnwrapStream(shortReader{io.NopCloser(strings.NewReader(stream))}, ToolNames{"task": true}))
			if err != nil {
				t.Fatal(err)
			}
			if eol == "\r" {
				ordinary = strings.ReplaceAll(ordinary, "\r", "\n")
			}
			for _, keep := range []string{ordinary, ": keep-alive", "id: event_original", "retry: 1000", "call_original", "read_original", `\"n\":900719925474099312345`, `汉字 😀`, `"output_tokens":9`} {
				if !bytes.Contains(out, []byte(keep)) {
					t.Fatalf("missing %s in %s", keep, out)
				}
			}
			if bytes.Contains(out, []byte(`\"arguments\":{`)) {
				t.Fatalf("envelope leaked %s", out)
			}
		})
	}
}
func TestStreamingInvalidIncompleteAndUpstreamError(t *testing.T) {
	start := event("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "tool_use", "name": "task", "input": map[string]any{}}}, "\n")
	for _, suffix := range []string{"", event("message_stop", map[string]any{}, "\n"), start, event("content_block_stop", map[string]any{"index": 1}, "\n"), event("content_block_delta", map[string]any{"index": 1, "delta": map[string]any{"type": "input_json_delta", "partial_json": strings.Repeat("a", maxToolBytes+1)}}, "\n")} {
		if _, err := io.ReadAll(UnwrapStream(io.NopCloser(strings.NewReader(start+suffix)), ToolNames{"task": true})); !errors.Is(err, ErrToolAdaptation) {
			t.Fatal("invalid stream accepted", err)
		}
	}
	upstream := event("error", map[string]any{"error": map[string]any{"type": "overloaded_error", "message": "try later"}}, "\n")
	out, err := io.ReadAll(UnwrapStream(io.NopCloser(strings.NewReader(start+upstream)), ToolNames{"task": true}))
	if err != nil || !bytes.Contains(out, []byte(upstream)) {
		t.Fatal("upstream error lost", err)
	}
	initial := event("content_block_start", map[string]any{"index": 1, "content_block": map[string]any{"type": "tool_use", "name": "task", "input": map[string]any{"arguments": map[string]any{"x": 1}}}}, "\n") + event("content_block_stop", map[string]any{"index": 1}, "\n") + event("message_stop", map[string]any{}, "\n")
	out, err = io.ReadAll(UnwrapStream(io.NopCloser(strings.NewReader(initial)), ToolNames{"task": true}))
	if err != nil || !bytes.Contains(out, []byte(`\"x\":1`)) {
		t.Fatal("initial input lost", err)
	}
}

type closeReader struct {
	io.Reader
	closed bool
}

func (r *closeReader) Close() error { r.closed = true; return nil }
func TestStreamClosePropagates(t *testing.T) {
	r := &closeReader{Reader: strings.NewReader("")}
	_ = UnwrapStream(r, ToolNames{"task": true}).Close()
	if !r.closed {
		t.Fatal("upstream not closed")
	}
}
