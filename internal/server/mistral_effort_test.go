package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMistralDeepSeekDefaultEffort(t *testing.T) {
	e := newMistralEnv(t)
	for _, tc := range []struct{ name, fields, want string }{
		{"missing", "", `"high"`},
		{"enabled", `"thinking":{"type":"enabled"},`, `"high"`},
		{"disabled", `"thinking":{"type":"disabled"},`, `"none"`},
		{"boolean disabled", `"enable_thinking":false,`, `"none"`},
		{"native null", `"reasoning_effort":null,`, `null`},
		{"none", `"reasoning_effort":"none",`, `"none"`},
		{"low", `"reasoning_effort":"low",`, `"low"`},
		{"high", `"reasoning_effort":"high",`, `"high"`},
		{"xhigh", `"reasoning_effort":"xhigh",`, `"xhigh"`},
		{"max", `"reasoning_effort":"max",`, `"max"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{ "model":"deepseek-v4-1-flash-1017", ` + tc.fields + `"messages":[{"role":"user","content":"mock"}], "tools":[{"n":9007199254740993}], "metadata": {"session_id":"client-session"}, "prompt_cache_key":null, "max_tokens":16000 }`)
			out, generated, err := prepareMistralRequest(body, "/v1/chat/completions", "scope")
			var original, got map[string]json.RawMessage
			if err != nil || json.Unmarshal(out, &got) != nil || generated != "" || string(got["reasoning_effort"]) != tc.want {
				t.Fatalf("wrong default or explicit effort: %s %v", out, err)
			}
			_ = json.Unmarshal(body, &original)
			for _, key := range []string{"model", "messages", "tools", "metadata", "prompt_cache_key", "max_tokens"} {
				if !bytes.Equal(got[key], original[key]) {
					t.Fatalf("unrelated wire field changed: %s", key)
				}
			}
			if got["thinking"] != nil || got["enable_thinking"] != nil {
				t.Fatal("compatibility switch was not converted to native effort")
			}
			before := e.count()
			st, reply := e.post(t, "/v1/chat/completions", string(body), nil)
			if st != 200 || e.count() != before+1 {
				t.Fatalf("request did not reach mock upstream exactly once: %d %s", st, reply)
			}
			if e.call(before).body != string(out) {
				t.Fatal("upstream did not receive the prepared effort and preserved wire fields")
			}
		})
	}
}

func TestMistralDeepSeekMediumEffortCompatibility(t *testing.T) {
	for _, body := range []string{
		`{ "model":"deepseek-v4-1-flash-1017", "reasoning_effort":"medium", "messages":[{"role":"assistant","content":"answer","reasoning_content":"thought"}], "tools":[{"number":9007199254740993}], "prompt_cache_key":"client-cache" }`,
		`{ "reasoning_effort":"medium", "messages":[{"role":"assistant","content":"answer","reasoning_content":"thought"}], "model":"deepseek-v4-1-flash-1017", "tools":[{"number":9007199254740993}], "prompt_cache_key":"client-cache" }`,
	} {
		out, generated, err := prepareMistralRequest([]byte(body), "/v1/chat/completions", "scope")
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(out, &fields) != nil || generated != "" || string(fields["reasoning_effort"]) != `"high"` {
			t.Fatalf("effort normalization failed: %s %v", out, err)
		}
		if string(fields["prompt_cache_key"]) != `"client-cache"` || !bytes.Contains(fields["tools"], []byte("9007199254740993")) || !bytes.Contains(fields["messages"], []byte(`"thinking"`)) || !bytes.Contains(fields["messages"], []byte(`"answer"`)) {
			t.Fatal("lost client cache, tools, numeric values or reasoning history")
		}
	}
	e := newMistralEnv(t)
	if st, body := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"medium","messages":[{"role":"user","content":"mock"}]}`, nil); st != 200 || e.count() != 1 || !strings.Contains(e.call(0).body, `"reasoning_effort":"high"`) {
		t.Fatalf("upstream did not receive supported effort exactly once: %d %s", st, body)
	}
}

func TestMistralEffortCompatibilityScope(t *testing.T) {
	for _, model := range []string{"deepseek-v4-1-flash-1017", "mistral-large-latest", "other-model"} {
		for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/fim/completions"} {
			for _, effort := range []string{"", `"none"`, `"low"`, `"high"`, `"xhigh"`, `"max"`, `null`, `"medium"`} {
				if model == "deepseek-v4-1-flash-1017" && path == "/v1/chat/completions" && (effort == "" || effort == `"medium"`) {
					continue
				}
				fields := ""
				if effort != "" {
					fields = `"reasoning_effort":` + effort + `, `
				}
				body := []byte(`{ "model":"` + model + `", ` + fields + `"messages":[], "prompt_cache_key":"keep" }`)
				out, _, err := prepareMistralRequest(body, path, "scope")
				if err != nil || !bytes.Equal(out, body) {
					t.Fatalf("another model/native effort/protocol changed: %s %s %s", model, path, effort)
				}
			}
		}
	}
}
