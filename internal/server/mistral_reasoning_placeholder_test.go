package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMistralReasoningWhitespacePlaceholder(t *testing.T) {
	// Production history replay supplied a real reasoning string alongside
	// reasoning_content=" ". A placeholder must not discard the real thought.
	thought := "\n  完整 original thought\t\n"
	for _, model := range []string{"deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash"} {
		for _, placeholder := range []string{" ", "\t\r\n", "\u00a0\u2003"} {
			for _, realAlias := range []bool{true, false} {
				a, b := thought, placeholder
				if !realAlias {
					a, b = placeholder, thought
				}
				body := []byte(fmt.Sprintf(`{ "model":%q,"messages":[{"role":"user","content":"original user"},{"role":"assistant","reasoning":%q,"reasoning_content":%q,"content":"answer","tool_calls":[{"id":"tool-original","function":{"arguments":"{\"n\":9007199254740993}"}}],"number":9007199254740993}],"tools":[ {"z":9007199254740993,"a":1} ],"metadata":{"session_id":"original-session"},"prompt_cache_key":"original-cache" }`, model, a, b))
				out, generated, err := prepareMistralRequest(body, "/v1/chat/completions", "unused")
				if err != nil {
					t.Fatalf("model=%s alias=%t placeholder=%q: %v", model, realAlias, placeholder, err)
				}
				if generated != "" || !json.Valid(out) {
					t.Fatal("invalid prepared request or overwritten cache key")
				}
				var before, after map[string]json.RawMessage
				_ = json.Unmarshal(body, &before)
				_ = json.Unmarshal(out, &after)
				for _, key := range []string{"model", "tools", "metadata", "prompt_cache_key"} {
					if !bytes.Equal(before[key], after[key]) {
						t.Fatalf("unrelated field bytes changed: %s", key)
					}
				}
				var oldMessages, messages []json.RawMessage
				_ = json.Unmarshal(before["messages"], &oldMessages)
				_ = json.Unmarshal(after["messages"], &messages)
				if len(messages) != 2 || !bytes.Equal(oldMessages[0], messages[0]) {
					t.Fatal("history changed")
				}
				var oldMessage, message map[string]json.RawMessage
				_ = json.Unmarshal(oldMessages[1], &oldMessage)
				_ = json.Unmarshal(messages[1], &message)
				if message["reasoning"] != nil || message["reasoning_content"] != nil {
					t.Fatal("legacy thought fields remained")
				}
				for _, key := range []string{"tool_calls", "number"} {
					if !bytes.Equal(oldMessage[key], message[key]) {
						t.Fatalf("tool or integer bytes changed: %s", key)
					}
				}
				var chunks []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Thinking []struct {
						Text string `json:"text"`
					} `json:"thinking"`
				}
				if json.Unmarshal(message["content"], &chunks) != nil || len(chunks) != 2 || chunks[0].Type != "thinking" || len(chunks[0].Thinking) != 1 || chunks[0].Thinking[0].Text != thought || chunks[1].Text != "answer" {
					t.Fatal("full thought whitespace or answer changed")
				}
			}
		}
	}
}

func TestMistralReasoningWhitespaceDoesNotHideRealConflict(t *testing.T) {
	e := newMistralEnv(t)
	for _, model := range []string{"deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash"} {
		body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"assistant","reasoning":" thought ","reasoning_content":"thought","content":"answer"}]}`, model)
		status, reply := e.post(t, "/v1/chat/completions", body, nil)
		if status != 400 || e.count() != 0 || !strings.Contains(reply, "conflicts with reasoning_content") {
			t.Fatalf("real nonblank conflict was silently resolved: %d %s", status, reply)
		}
	}
	body := `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant","reasoning":"complete original thought","reasoning_content":" ","content":"answer"}]}`
	status, reply := e.post(t, "/v1/chat/completions", body, nil)
	if status != 200 || e.count() != 1 || !strings.Contains(e.call(0).body, "complete original thought") {
		t.Fatalf("placeholder never reached mock upstream losslessly: %d %s", status, reply)
	}
}

func TestMistralReasoningWhitespaceScope(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"deepseek-v4-1-flash-1017", "/v1/responses"},
		{"zai-glm-5-3", "/v1/fim/completions"},
	} {
		body := []byte(fmt.Sprintf(`{"model":%q,"reasoning":{"effort":"high"},"messages":[{"role":"assistant","reasoning":"thought","reasoning_content":" ","content":"answer"}]}`, tc.model))
		out, _, err := prepareMistralRequest(body, tc.path, "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("placeholder policy escaped exact scope: %s %s %v", tc.model, tc.path, err)
		}
	}
	// Other models retain the pre-existing canonical adaptation, without
	// consuming the model-specific alias or substituting its real thought.
	body := []byte(`{"model":"other-model","messages":[{"role":"assistant","reasoning":"original-alias","reasoning_content":" ","content":"answer"}]}`)
	out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
	if err != nil || !bytes.Contains(out, []byte(`"reasoning":"original-alias"`)) || bytes.Contains(out, []byte(`"text":"original-alias"`)) {
		t.Fatal("other-model alias was consumed or selected")
	}
}
