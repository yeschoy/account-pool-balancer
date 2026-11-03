package opencode

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func toolHistory(p Protocol) []byte {
	switch p {
	case Chat:
		return []byte(`{"model":"m","stream":true,"max_tokens":100,"messages":[{"role":"system","content":"rules"},{"role":"user","content":"question"},{"role":"assistant","tool_calls":[{"id":"call_original","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}}]},{"role":"tool","tool_call_id":"call_original","content":"answer"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`)
	case Responses:
		return []byte(`{"model":"m","stream":true,"max_output_tokens":100,"input":[{"role":"system","content":"rules"},{"role":"user","content":"question"},{"type":"function_call","call_id":"call_original","name":"lookup","arguments":"{\"q\":1}"},{"type":"function_call_output","call_id":"call_original","output":"answer"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}`)
	default:
		return []byte(`{"model":"m","stream":true,"max_tokens":100,"system":"rules","messages":[{"role":"user","content":"question"},{"role":"assistant","content":[{"type":"tool_use","id":"call_original","name":"lookup","input":{"q":1}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_original","content":"answer"}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`)
	}
}
func TestRequestBridgeToolHistoryAllPairs(t *testing.T) {
	for _, from := range []Protocol{Chat, Responses, Messages} {
		for _, to := range []Protocol{Chat, Responses, Messages} {
			t.Run(string(from)+"-"+string(to), func(t *testing.T) {
				b, err := ConvertRequest(from, to, toolHistory(from))
				if err != nil {
					t.Fatal(err)
				}
				if !json.Valid(b) || bytes.Count(b, []byte("call_original")) != 2 {
					t.Fatalf("tool chain corrupted: %s", b)
				}
				var m object
				_ = json.Unmarshal(b, &m)
				r, err := decodeRequest(to, m)
				if err != nil {
					t.Fatalf("target request invalid: %v: %s", err, b)
				}
				if len(r.tools) != 1 || r.tools[0].name != "lookup" {
					t.Fatal("tool definition lost")
				}
			})
		}
	}
}
func TestNativePreservesEverything(t *testing.T) {
	b := []byte("{ \"model\":\"m\",\"thinking\":{\"type\":\"adaptive\"},\"previous_response_id\":\"r1\",\"opaque\":[1,2] }")
	for _, p := range []Protocol{Chat, Responses, Messages} {
		out, err := ConvertRequest(p, p, b)
		if err != nil || !bytes.Equal(out, b) {
			t.Fatal("native bytes changed")
		}
	}
}
func TestBridgeRejectsLossyFieldsAndOrphanTools(t *testing.T) {
	tests := []struct {
		p Protocol
		b string
	}{
		{Chat, `{"model":"m","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`},
		{Responses, `{"model":"m","previous_response_id":"r1","input":"next"}`},
		{Messages, `{"model":"m","max_tokens":10,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"secret","signature":"signed"}]}]}`},
		{Chat, `{"model":"m","messages":[{"role":"tool","tool_call_id":"orphan","content":"result"}]}`},
		{Responses, `{"model":"m","input":"hi","tools":[{"type":"web_search"}]}`},
	}
	for _, tt := range tests {
		to := Chat
		if tt.p == Chat {
			to = Messages
		}
		if b, err := ConvertRequest(tt.p, to, []byte(tt.b)); err == nil {
			t.Fatalf("lossy conversion accepted: %s", b)
		}
	}
	if _, err := ConvertRequest(Chat, Messages, []byte(`{"model":"m","max_tokens":1,"messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`)); err == nil {
		t.Fatal("developer precedence silently merged")
	}
}
func TestImageBridgeAndDetailsRejection(t *testing.T) {
	b := []byte(`{"model":"m","max_tokens":10,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}},{"type":"text","text":"describe"}]}]}`)
	out, err := ConvertRequest(Chat, Messages, b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"media_type":"image/png"`)) || !bytes.Contains(out, []byte("aGVsbG8=")) {
		t.Fatalf("image lost %s", out)
	}
	b = bytes.Replace(b, []byte(`"url":`), []byte(`"detail":"high","url":`), 1)
	if _, err := ConvertRequest(Chat, Messages, b); err == nil {
		t.Fatal("image detail silently lost")
	}
}
func TestReplyJSONAndSSEAllPairs(t *testing.T) {
	source := &Reply{ID: "reply_1", Model: "m", Stop: "tools", Parts: []part{{kind: "text", text: "hello"}, {kind: "call", id: "original_tool_id", name: "lookup", args: `{"q":1}`}}, Usage: Tokens{Known: true, CachedKnown: true, CacheWriteKnown: true, Input: 100, Output: 20, Cached: 30, CacheWrite: 5}}
	for _, from := range []Protocol{Chat, Responses, Messages} {
		m, err := source.Encode(from)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(m)
		decoded, err := DecodeReply(from, b)
		if err != nil {
			t.Fatal(err)
		}
		stream, err := source.Stream(from)
		if err != nil {
			t.Fatal(err)
		}
		collapsed, err := ReadStream(from, bytes.NewReader(stream))
		if err != nil {
			t.Fatalf("%s: %v\n%s", from, err, stream)
		}
		for _, reply := range []*Reply{decoded, collapsed} {
			if reply.Usage.Input != 100 || reply.Usage.Cached != 30 || reply.Usage.CacheWrite != 5 || reply.Usage.Output != 20 {
				t.Fatalf("%s usage %+v", from, reply.Usage)
			}
			if len(reply.Parts) != 2 || reply.Parts[1].id != "original_tool_id" || reply.Parts[1].args != `{"q":1}` {
				t.Fatalf("%s tool chain %+v", from, reply.Parts)
			}
			for _, to := range []Protocol{Chat, Responses, Messages} {
				if _, err := reply.Encode(to); err != nil {
					t.Fatal(err)
				}
				out, err := reply.Stream(to)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(out, []byte("original_tool_id")) {
					t.Fatal("SSE tool ID lost")
				}
			}
		}
	}
}
func TestStreamPartialArgumentsAndTruncation(t *testing.T) {
	stream := `data: {"id":"r1","model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"tool_unchanged","type":"function","function":{"name":"run","arguments":"{\"x\":"}}]},"finish_reason":null}]}

data: {"id":"r1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}

data: [DONE]

`
	r, err := ReadStream(Chat, strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	if r.Parts[1].id != "tool_unchanged" || r.Parts[1].args != `{"x":1}` {
		t.Fatalf("bad partial tool %+v", r.Parts)
	}
	if _, err := ReadStream(Chat, strings.NewReader(strings.ReplaceAll(stream, "data: [DONE]", ""))); err == nil {
		t.Fatal("truncated SSE accepted")
	}
}
func TestConvertedReasoningIsExplicitlyRejected(t *testing.T) {
	if _, err := DecodeReply(Messages, []byte(`{"id":"r","model":"m","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"x","signature":"s"}]}`)); err == nil {
		t.Fatal("signed thinking lost")
	}
}
