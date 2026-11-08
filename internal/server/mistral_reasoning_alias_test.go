package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMistralDeepSeekReasoningAliasPreservesHistory(t *testing.T) {
	for _, content := range []string{`"answer"`, `null`, `[{"type":"text","text":"answer"},{"type":"image_url","image_url":"https://example.invalid/image"}]`} {
		body := []byte(`{ "model":"deepseek-v4-1-flash-1017", "messages":[{"role":"system","content":"s"},{"role":"user","content":"u"},{"role":"assistant","content":"previous"},{"role":"tool","tool_call_id":"original-id","content":"result"},{ "role":"assistant", "reasoning":"original thought", "content":` + content + `,"tool_calls": [{"id":"original-id","function":{"arguments":"{\"n\":9007199254740993}"}}],"number":9007199254740993}], "tools": [ {"n":9007199254740993} ], "metadata": {"session_id":"client-session"}, "prompt_cache_key":"client-cache" }`)
		out, generated, err := prepareMistralRequest(body, "/v1/chat/completions", "scope")
		if err != nil || !json.Valid(out) || generated != "" {
			t.Fatalf("alias conversion failed: %v", err)
		}
		var got, original map[string]json.RawMessage
		_ = json.Unmarshal(out, &got)
		_ = json.Unmarshal(body, &original)
		for _, key := range []string{"model", "tools", "metadata", "prompt_cache_key"} {
			if !bytes.Equal(got[key], original[key]) {
				t.Fatalf("unrelated wire bytes changed: %s", key)
			}
		}
		var messages, originals []json.RawMessage
		_ = json.Unmarshal(got["messages"], &messages)
		_ = json.Unmarshal(original["messages"], &originals)
		for i := 0; i < 4; i++ {
			if !bytes.Equal(messages[i], originals[i]) {
				t.Fatal("original history order/bytes changed")
			}
		}
		var last map[string]json.RawMessage
		_ = json.Unmarshal(messages[4], &last)
		if last["reasoning"] != nil || last["reasoning_content"] != nil || string(last["number"]) != "9007199254740993" || !bytes.Contains(last["tool_calls"], []byte("original-id")) {
			t.Fatal("alias remained or tool/numeric data lost")
		}
		if !bytes.Contains(last["content"], []byte(`"type":"thinking"`)) || !bytes.Contains(last["content"], []byte("original thought")) || content != "null" && !bytes.Contains(last["content"], []byte("answer")) {
			t.Fatal("thought or original answer was discarded")
		}
	}
}

func TestMistralReasoningAliasEmptyAndCanonicalAgreement(t *testing.T) {
	for _, fields := range []string{
		`"reasoning":null`,
		`"reasoning":""`,
		`"reasoning":"thought","reasoning_content":"thought"`,
		`"reasoning":null,"reasoning_content":"thought"`,
		`"reasoning":"","reasoning_content":"thought"`,
		`"reasoning":"thought","reasoning_content":null`,
		`"reasoning":"thought","reasoning_content":""`,
	} {
		out, _, err := prepareMistralRequest([]byte(`{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant",`+fields+`,"content":"answer"}]}`), "/v1/chat/completions", "")
		if err != nil || bytes.Contains(out, []byte(`"reasoning":`)) || bytes.Contains(out, []byte("reasoning_content")) || !bytes.Contains(out, []byte("answer")) {
			t.Fatalf("empty/equivalent alias not preserved: %s %v", out, err)
		}
		want := 0
		if strings.Contains(fields, "thought") {
			want = 1
		}
		if bytes.Count(out, []byte(`"type":"thinking"`)) != want || bytes.Count(out, []byte("thought")) != want {
			t.Fatal("invented or duplicated thought")
		}
	}
}

func TestMistralReasoningAliasAmbiguityRejectedBeforeUpstream(t *testing.T) {
	e := newMistralEnv(t)
	for _, fields := range []string{
		`"reasoning":{},"content":"answer"`,
		`"reasoning":[],"content":"answer"`,
		`"reasoning":1,"content":"answer"`,
		`"reasoning":"a","reasoning_content":"b","content":"answer"`,
		`"reasoning":"a","reasoning_content":{},"content":"answer"`,
		`"reasoning":"a","reasoning":"a","content":"answer"`,
		`"reasoning":"a","reas\u006fning":"a","content":"answer"`,
		`"reasoning":"a","content":"answer","content":"different"`,
	} {
		st, reply := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant",`+fields+`}]}`, nil)
		if st != 400 || e.count() != 0 || !strings.Contains(reply, "invalid_mistral_request") {
			t.Fatalf("ambiguous thought discarded or forwarded: %d %s", st, reply)
		}
	}
	st, reply := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant","reasoning":"thought","content":"answer"}]}`, nil)
	if st != 200 || e.count() != 1 || strings.Contains(e.call(0).body, `"reasoning":`) || !strings.Contains(e.call(0).body, `"type":"thinking"`) {
		t.Fatalf("native mock did not receive one lossless conversion: %d %s", st, reply)
	}
}

func TestMistralReasoningAliasScope(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"other-model", "/v1/chat/completions"},
		{"deepseek-v4-1-flash-1017", "/v1/responses"},
		{"deepseek-v4-1-flash-1017", "/v1/fim/completions"},
	} {
		body := []byte(` {"model":"` + tc.model + `","messages":[{"role":"assistant","reasoning":"native","content":"answer"}]} `)
		out, _, err := prepareMistralRequest(body, tc.path, "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatal("alias compatibility escaped exact scope")
		}
	}
	body := []byte(` {"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","reasoning":"native","content":"answer"}]} `)
	out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
	var original, got map[string]json.RawMessage
	_ = json.Unmarshal(body, &original)
	if err != nil || json.Unmarshal(out, &got) != nil || len(got) != len(original)+1 || string(got["reasoning_effort"]) != `"high"` {
		t.Fatal("default effort missing or unrelated field added")
	}
	for key, value := range original {
		if !bytes.Equal(got[key], value) {
			t.Fatalf("non-assistant alias or unrelated field changed: %s", key)
		}
	}
}
