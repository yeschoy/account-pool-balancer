package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
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

func TestMistralDeepSeekUserTextOneHourCacheHint(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		// Match the production rejection, decoding the field name even if escaped.
		var sent struct {
			Messages []struct{ Content json.RawMessage }
		}
		var chunks []map[string]json.RawMessage
		if json.Unmarshal([]byte(e.call(e.count()-1).body), &sent) != nil || len(sent.Messages) != 6 ||
			json.Unmarshal(sent.Messages[3].Content, &chunks) != nil || len(chunks) != 2 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if _, found := chunks[0]["cache_control"]; found {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"detail":[{"loc":["body","messages",3,"user","content",0,"text","cache_control"],"type":"extra_forbidden"}]}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	})
	const prefix = `{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high","prompt_cache_key":"local-cache","tools":[{"n":9007199254740993}],"messages":[{"role":"system","content":"instruction"},{"role":"user","content":"first"},{"role":"assistant","content":"reply"},{"role":"user","content":[{"type":"text","text":"原文\n"`
	const suffix = `},{"type":"image_url","image_url":"https://example.invalid/image.png"}]},{"role":"assistant","content":"second reply"},{"role":"user","content":"continue"}]}`
	for _, hint := range []string{`,"cache_control":{"type":"ephemeral","ttl":"1h"}`, `,"cache_contro\u006c":{"t\u0074l":"1\u0068","type":"ephemeral"}`} {
		before := e.count()
		status, reply := e.post(t, "/v1/chat/completions", prefix+hint+suffix, nil)
		if status != http.StatusOK || e.count() != before+1 || !strings.Contains(reply, `"content":"ok"`) {
			t.Fatalf("one-hour hint triggered mock rejection or retry: %d %s", status, reply)
		}
		call := e.call(before)
		if call.body != prefix+suffix || call.header.Get("X-Affinity") != "local-cache" {
			t.Fatal("removed more than the unsupported hint or changed cache affinity")
		}
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
		body := []byte(fmt.Sprintf(`{"model":%q,"reasoning_effort":"high","reasoning":{"effort":"high"},"messages":[{"role":"user","content":[{"type":"text","text":"original","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]}`, tc.model))
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
	for _, chunk := range []string{
		`{"role":"user","content":[{"type":"text","text":"original","cache_control":{"type":"ephemeral","ttl":"5m"}}]}`,
		`{"role":"user","content":[{"type":"text","text":"original","cache_control":{"type":"ephemeral","ttl":"1h","scope":"other"}}]}`,
		`{"role":"user","content":[{"type":"text","text":"original","cache_control":{"type":"other","ttl":"1h"}}]}`,
		`{"role":"user","content":[{"type":"text","text":"original","cache_control":null}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"original","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
		`{"role":"system","content":[{"type":"text","text":"original","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
		`{"role":"user","content":[{"type":"image_url","image_url":"https://example.invalid/image.png","cache_control":{"type":"ephemeral","ttl":"1h"}}]}`,
	} {
		body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high","messages":[` + chunk + `]}`)
		out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("unconfirmed hint shape or role was rewritten: %s (%v)", chunk, err)
		}
	}
}

func TestMistralDeepSeekUserTextDuplicateCacheTTL(t *testing.T) {
	e := newMistralEnv(t)
	for _, ttl := range []string{`"ttl":"5m","t\u0074l":"1h"`, `"ttl":"1h","t\u0074l":"5m"`} {
		body := `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":[{"type":"text","text":"original","cache_control":{"type":"ephemeral",` + ttl + `}}]}]}`
		status, reply := e.post(t, "/v1/chat/completions", body, nil)
		if status != http.StatusBadRequest || e.count() != 0 || !strings.Contains(reply, "duplicate ttl") {
			t.Fatalf("ambiguous hint was not rejected before upstream: %d %s", status, reply)
		}
	}
}
