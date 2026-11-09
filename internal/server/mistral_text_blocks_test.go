package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMistralDeepSeekTextBlockCompatibility(t *testing.T) {
	messages := make([]string, 110)
	for i := range messages {
		messages[i] = fmt.Sprintf(`{"role":"user","content":"original history %d"}`, i)
	}
	messages = append(messages, `{"role":"assistant","content":[ {"type":"text"} ],"tool_calls":[{"id":"original-tool","function":{"arguments":"{\"n\":9007199254740993}"}}]}`,
		`{"role":"user","content":[{"type":"image_url","image_url":"https://example.invalid/original.png"},{ "type":"text", "text":"完整 original text\n", "cache_control":{ "type":"ephemeral" } }]}`)
	body := []byte(`{ "model":"deepseek-v4-1-flash-1017", "messages":[` + strings.Join(messages, ",") + `], "tools":[ {"z":9007199254740993,"a":1} ], "prompt_cache_key":"original-cache", "metadata":{"session_id":"original-session"} }`)
	out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "unused")
	if err != nil || bytes.Contains(out, []byte(`"cache_control"`)) {
		t.Fatalf("standard hint was not adapted: %v", err)
	}
	var oldRoot, root map[string]json.RawMessage
	_ = json.Unmarshal(body, &oldRoot)
	_ = json.Unmarshal(out, &root)
	for _, key := range []string{"model", "tools", "prompt_cache_key", "metadata"} {
		if !bytes.Equal(oldRoot[key], root[key]) {
			t.Fatalf("unrelated root field changed: %s", key)
		}
	}
	var oldMessages, gotMessages []json.RawMessage
	_ = json.Unmarshal(oldRoot["messages"], &oldMessages)
	_ = json.Unmarshal(root["messages"], &gotMessages)
	if len(gotMessages) != len(oldMessages) {
		t.Fatal("history lost")
	}
	for i := 0; i < 110; i++ {
		if !bytes.Equal(oldMessages[i], gotMessages[i]) {
			t.Fatalf("old history bytes changed: %d", i)
		}
	}
	var assistant, originalAssistant, user map[string]json.RawMessage
	_ = json.Unmarshal(gotMessages[110], &assistant)
	_ = json.Unmarshal(oldMessages[110], &originalAssistant)
	_ = json.Unmarshal(gotMessages[111], &user)
	if !bytes.Contains(assistant["content"], []byte(`"text":""`)) || !bytes.Equal(assistant["tool_calls"], originalAssistant["tool_calls"]) {
		t.Fatal("empty text slot not completed or tool calls changed")
	}
	if !bytes.Contains(user["content"], []byte(`"text":"完整 original text\n"`)) || !bytes.Contains(user["content"], []byte(`https://example.invalid/original.png`)) {
		t.Fatal("real text or image lost")
	}
	e := newMistralEnv(t)
	status, reply := e.post(t, "/v1/chat/completions", string(body), nil)
	if status != 200 || e.count() != 1 || bytes.Contains([]byte(e.call(0).body), []byte(`"cache_control"`)) {
		t.Fatalf("mock upstream did not receive compatible full history: %d %s", status, reply)
	}
}

func TestMistralDeepSeekTextBlockScopeAndUnknownHints(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"zai-glm-5-3", "/v1/chat/completions"},
		{"zai-glm-5-3-flash", "/v1/chat/completions"},
		{"other-model", "/v1/chat/completions"},
		{"deepseek-v4-1-flash-1017", "/v1/responses"},
		{"deepseek-v4-1-flash-1017", "/v1/fim/completions"},
	} {
		body := []byte(fmt.Sprintf(`{"model":%q,"reasoning_effort":"high","reasoning":{"effort":"high"},"messages":[{"role":"user","content":[{"type":"text","text":"original","cache_control":{"type":"ephemeral"}}]}]}`, tc.model))
		// Native Responses uses only its nested effort; other paths/models have no adapter changes.
		if tc.path == "/v1/responses" {
			body = bytes.Replace(body, []byte(`"reasoning_effort":"high",`), nil, 1)
		} else if tc.model != "deepseek-v4-1-flash-1017" || tc.path == "/v1/chat/completions" {
			body = bytes.Replace(body, []byte(`"reasoning":{"effort":"high"},`), nil, 1)
		}
		out, _, err := prepareMistralRequest(body, tc.path, "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("text compatibility escaped scope: %s %s %v", tc.model, tc.path, err)
		}
	}
	for _, hint := range []string{`{"type":"ephemeral","ttl":"5m"}`, `{"type":"other"}`, `null`} {
		body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high","messages":[{"role":"user","content":[{"type":"text","text":"original","cache_control":` + hint + `}]}]}`)
		out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatal("unknown or explicit TTL hint was silently discarded")
		}
	}
}
