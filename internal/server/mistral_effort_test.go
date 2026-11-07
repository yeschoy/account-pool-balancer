package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

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
			for _, effort := range []string{`"none"`, `"low"`, `"high"`, `"xhigh"`, `"max"`, `null`, `"medium"`} {
				if model == "deepseek-v4-1-flash-1017" && path == "/v1/chat/completions" && effort == `"medium"` {
					continue
				}
				body := []byte(`{ "model":"` + model + `", "reasoning_effort":` + effort + `, "messages":[], "prompt_cache_key":"keep" }`)
				out, _, err := prepareMistralRequest(body, path, "scope")
				if err != nil || !bytes.Equal(out, body) {
					t.Fatalf("another model/native effort/protocol changed: %s %s %s", model, path, effort)
				}
			}
		}
	}
}
