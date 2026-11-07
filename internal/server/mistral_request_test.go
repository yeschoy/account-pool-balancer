package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestMistralReasoningCompatibilityAndNativeFields(t *testing.T) {
	body := []byte(` { "model":"m", "tools": [ {"id":"keep-id-123", "number":9007199254740993} ], "messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"answer","reasoning_content":"thought <private>","tool_calls":[{"id":"tool-original","function":{"arguments":"{\"x\":1}"}}]},{"role":"tool","tool_call_id":"tool-original","content":"result"},{"role":"assistant","reasoning_content":"second thought","content":[{"type":"text","text":"second answer"},{"type":"image_url","image_url":"https://example.invalid/image"}]}] } `)
	out, key, err := prepareMistralRequest(body, "/v1/chat/completions", "scope-hmac")
	if err != nil || key != "apb-mistral-scope-hmac" || !json.Valid(out) {
		t.Fatalf("prepare: %q %v", key, err)
	}
	var original, got map[string]json.RawMessage
	_ = json.Unmarshal(body, &original)
	_ = json.Unmarshal(out, &got)
	if !bytes.Equal(got["model"], original["model"]) || !bytes.Equal(got["tools"], original["tools"]) {
		t.Fatal("model/tools/numeric wire values changed")
	}
	var messages []map[string]json.RawMessage
	_ = json.Unmarshal(got["messages"], &messages)
	if len(messages) != 4 || string(messages[2]["tool_call_id"]) != `"tool-original"` || !bytes.Contains(messages[1]["tool_calls"], []byte("tool-original")) {
		t.Fatal("message history/tool calls were lost")
	}
	for _, i := range []int{1, 3} {
		if _, exists := messages[i]["reasoning_content"]; exists {
			t.Fatal("illegal assistant field remains")
		}
		var chunks []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"thinking"`
		}
		if err := json.Unmarshal(messages[i]["content"], &chunks); err != nil || chunks[0].Type != "thinking" || len(chunks[0].Thinking) != 1 || chunks[0].Thinking[0].Type != "text" {
			t.Fatal("reasoning was not mapped to native thinking chunks")
		}
		wantThought, wantAnswer := "thought <private>", "answer"
		if i == 3 {
			wantThought, wantAnswer = "second thought", "second answer"
		}
		if chunks[0].Thinking[0].Text != wantThought || chunks[1].Text != wantAnswer {
			t.Fatal("reasoning/answer content was changed")
		}
	}
	if !bytes.Contains(messages[3]["content"], []byte("image_url")) {
		t.Fatal("existing content chunks were removed")
	}
}

func TestMistralCachePreservationAndProtocolScope(t *testing.T) {
	for _, value := range []string{`"client-cache"`, `null`, `""`} {
		body := []byte(` {"model":"m", "prompt_cache_key":` + value + `,"messages":[]} `)
		out, generated, err := prepareMistralRequest(body, "/v1/chat/completions", "scope")
		if err != nil || generated != "" || !bytes.Equal(out, body) {
			t.Fatal("client cache value/native bytes overwritten")
		}
	}
	for _, path := range []string{"/v1/responses/input_tokens", "/v1/fim/completions", "/v1/embeddings", "/v1/messages"} {
		body := []byte(`{"model":"m","messages":[{"role":"assistant","reasoning_content":"keep-native"}]}`)
		out, generated, err := prepareMistralRequest(body, path, "scope")
		if err != nil || generated != "" || !bytes.Equal(out, body) {
			t.Fatal("non-Chat/Responses generation endpoint changed")
		}
	}
	body := []byte(` {"model":"m", "input":[{"type":"reasoning","summary":[]}]} `)
	out, key, err := prepareMistralRequest(body, "/v1/responses", "scope")
	if err != nil || key != "apb-mistral-scope" || !bytes.Contains(out, []byte(`"input":[{"type":"reasoning","summary":[]}]`)) {
		t.Fatal("Responses history changed or cache key missing")
	}
	out, key, err = prepareMistralRequest(body, "/v1/responses", "")
	if err != nil || key != "" || !bytes.Equal(out, body) {
		t.Fatal("identity-free request got a random cache key")
	}
}

func TestMistralReasoningEmptyAndInvalidValues(t *testing.T) {
	for _, value := range []string{`null`, `""`, `" "`, `"thought"`} {
		body := []byte(`{"model":"m","messages":[{"role":"assistant","content":null,"reasoning_content":` + value + `,"tool_calls":[{"id":"keep"}]}]}`)
		out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		if err != nil || bytes.Contains(out, []byte("reasoning_content")) || !bytes.Contains(out, []byte(`"id":"keep"`)) {
			t.Fatal("empty/null/tool-only assistant handling failed", err)
		}
	}
	e := newMistralEnv(t)
	for _, value := range []string{`123`, `{}`, `[]`} {
		status, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"assistant","reasoning_content":`+value+`}]}`, nil)
		if status != 400 || e.count() != 0 {
			t.Fatal("unrepresentable reasoning was dropped or forwarded")
		}
	}
}

func TestMistralCacheStableIsolatedAndCallerKeyPreserved(t *testing.T) {
	e := newMistralEnv(t)
	var first string
	for i, auth := range []string{"mistral-client", "mistral-client", "mistral-other-client"} {
		st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"question"},{"role":"assistant","content":"answer","reasoning_content":"thought"}]}`, map[string]string{"X-Pi-Session-Id": "raw-private-session", "Authorization": "Bearer " + auth})
		if st != 200 {
			t.Fatal(st)
		}
		var body map[string]json.RawMessage
		_ = json.Unmarshal([]byte(e.call(i).body), &body)
		var key string
		_ = json.Unmarshal(body["prompt_cache_key"], &key)
		if !strings.HasPrefix(key, "apb-mistral-") || strings.Contains(key, "raw-private-session") || strings.Contains(key, auth) {
			t.Fatal("cache key is absent or leaks caller/session")
		}
		if i == 0 {
			first = key
		} else if (i == 1) != (key == first) {
			t.Fatal("session cache is unstable or crosses downstream callers")
		}
		if bytes.Contains(body["messages"], []byte("reasoning_content")) || !bytes.Contains(body["messages"], []byte(`"type":"thinking"`)) {
			t.Fatal("old assistant history was not normalized")
		}
	}
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Routing.FallbackHash = false; return nil }); err != nil {
		t.Fatal(err)
	}
	st, _ := e.post(t, "/v1/responses", `{"model":"m","store":false,"input":"hello","prompt_cache_key":"caller-cache"}`, nil)
	if st != 200 || !strings.Contains(e.call(3).body, `"prompt_cache_key":"caller-cache"`) {
		t.Fatal("Responses caller cache key changed")
	}
}
