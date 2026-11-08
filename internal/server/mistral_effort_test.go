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
		{"disabled", `"thinking":{"type":"disabled"},`, `"high"`},
		{"boolean disabled", `"enable_thinking":false,`, `"high"`},
		{"native null", `"reasoning_effort":null,`, `"high"`},
		{"none", `"reasoning_effort":"none",`, `"high"`},
		{"low", `"reasoning_effort":"low",`, `"low"`},
		{"high", `"reasoning_effort":"high",`, `"high"`},
		{"xhigh", `"reasoning_effort":"xhigh",`, `"high"`},
		{"unknown tier", `"reasoning_effort":"minimal",`, `"high"`},
		{"empty tier", `"reasoning_effort":"",`, `"high"`},
		{"invalid number", `"reasoning_effort":5,`, `"high"`},
		{"invalid boolean", `"reasoning_effort":false,`, `"high"`},
		{"invalid object", `"reasoning_effort":{},`, `"high"`},
		{"invalid array", `"reasoning_effort":[],`, `"high"`},
		{"max", `"reasoning_effort":"max",`, `"high"`},
		{"nested high", `"reasoning":{"effort":"high"},`, `"high"`},
		{"nested none", `"reasoning":{"effort":"none"},`, `"high"`},
		{"nested low", `"reasoning":{"effort":"low"},`, `"low"`},
		{"nested xhigh", `"reasoning":{"effort":"xhigh"},`, `"high"`},
		{"nested unknown", `"reasoning":{"effort":"minimal"},`, `"high"`},
		{"nested invalid type", `"reasoning":{"effort":{}},`, `"high"`},
		{"nested max", `"reasoning":{"effort":"max"},`, `"high"`},
		{"max and high aliases", `"reasoning_effort":"max","reasoning":{"effort":"high"},`, `"high"`},
		{"nested medium", `"reasoning":{"effort":"medium"},`, `"high"`},
		{"nested null", `"reasoning":{"effort":null},`, `"high"`},
		{"nested null enabled", `"reasoning":{"effort":null},"thinking":{"type":"enabled"},`, `"high"`},
		{"nested null disabled", `"reasoning":{"effort":null},"enable_thinking":false,`, `"high"`},
		{"nested explicit enabled", `"reasoning":{"effort":"low"},"enable_thinking":true,`, `"low"`},
		{"nested explicit disabled", `"reasoning":{"effort":"none"},"thinking":{"type":"disabled"},`, `"high"`},
		{"equal effort aliases", `"reasoning_effort":"low","reasoning":{"effort":"low"},`, `"low"`},
		{"equal null aliases", `"reasoning":{"effort":null},"reasoning_effort":null,`, `"high"`},
		{"null and high aliases", `"reasoning":{"effort":null},"reasoning_effort":"high",`, `"high"`},
		{"high and null aliases", `"reasoning":{"effort":"high"},"reasoning_effort":null,`, `"high"`},
		{"equal normalized aliases", `"reasoning_effort":"medium","reasoning":{"effort":"high"},`, `"high"`},
		{"equal normalized reverse aliases", `"reasoning":{"effort":"medium"},"reasoning_effort":"high",`, `"high"`},
		{"escaped alias values", `"reasoning_effort":"h\u0069gh","reasoning":{"effort":"high"},`, `"h\u0069gh"`},
		{"disabled with low", `"thinking":{"type":"disabled"},"reasoning_effort":"low",`, `"low"`},
		{"disabled with medium", `"thinking":{"type":"disabled"},"reasoning_effort":"medium",`, `"high"`},
		{"disabled with high", `"thinking":{"type":"disabled"},"reasoning_effort":"high",`, `"high"`},
		{"disabled with xhigh", `"thinking":{"type":"disabled"},"reasoning_effort":"xhigh",`, `"high"`},
		{"disabled with max", `"thinking":{"type":"disabled"},"reasoning_effort":"max",`, `"high"`},
		{"boolean disabled with xhigh", `"enable_thinking":false,"reasoning_effort":"xhigh",`, `"high"`},
		{"boolean disabled with max", `"enable_thinking":false,"reasoning_effort":"max",`, `"high"`},
		{"enabled with none", `"thinking":{"type":"enabled"},"reasoning_effort":"none",`, `"high"`},
		{"boolean enabled with none", `"enable_thinking":true,"reasoning_effort":"none",`, `"high"`},
		{"disabled with nested low", `"thinking":{"type":"disabled"},"reasoning":{"effort":"low"},`, `"low"`},
		{"disabled with nested xhigh", `"thinking":{"type":"disabled"},"reasoning":{"effort":"xhigh"},`, `"high"`},
		{"disabled with nested max", `"enable_thinking":false,"reasoning":{"effort":"max"},`, `"high"`},
		{"enabled with nested none", `"enable_thinking":true,"reasoning":{"effort":"none"},`, `"high"`},
		{"switch disagreement with explicit high", `"thinking":{"type":"enabled"},"enable_thinking":false,"reasoning_effort":"high",`, `"high"`},
		{"switch disagreement with explicit none", `"thinking":{"type":"disabled"},"enable_thinking":true,"reasoning_effort":"none",`, `"high"`},
		{"switch disagreement without effort", `"thinking":{"type":"enabled"},"enable_thinking":false,`, `"high"`},
		{"switch disagreement with nested max", `"thinking":{"type":"enabled"},"enable_thinking":false,"reasoning":{"effort":"max"},`, `"high"`},
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
			if got["thinking"] != nil || got["enable_thinking"] != nil || got["reasoning"] != nil || bytes.Count(out, []byte(`"reasoning_effort"`)) != 1 {
				t.Fatal("compatibility controls remained or native effort was duplicated")
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
		`{ "reasoning":{"effort":"medium"}, "store":false, "thinking":{"type":"enabled"}, "model":"deepseek-v4-1-flash-1017", "messages":[{"role":"assistant","content":"answer","reasoning":"thought"}], "tools":[{"number":9007199254740993}], "prompt_cache_key":"client-cache" }`,
		`{ "model":"deepseek-v4-1-flash-1017", "messages":[{"role":"assistant","content":"answer","reasoning":"thought"}], "tools":[{"number":9007199254740993}], "prompt_cache_key":"client-cache", "thinking":{"type":"enabled"}, "store":null, "reasoning":{"effort":"medium"} }`,
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

func TestMistralNestedEffortScopeAndCache(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"other-model", "/v1/chat/completions"},
		{"deepseek-v4-1-flash-1017", "/v1/responses"},
		{"deepseek-v4-1-flash-1017", "/v1/responses/input_tokens"},
		{"deepseek-v4-1-flash-1017", "/v1/fim/completions"},
	} {
		for _, reasoning := range []string{`{"effort":"high"}`, `{"effort":"none"}`, `{"effort":"high","summary":"auto"}`, `null`} {
			body := []byte(` {"model":"` + tc.model + `","reasoning":` + reasoning + `,"messages":[],"prompt_cache_key":"keep"} `)
			out, generated, err := prepareMistralRequest(body, tc.path, "scope")
			if err != nil || generated != "" || !bytes.Equal(out, body) {
				t.Fatalf("nested effort escaped exact model/Chat scope: %s %s %s", tc.model, tc.path, reasoning)
			}
		}
	}
	e := newMistralEnv(t)
	native := ` {"model":"deepseek-v4-1-flash-1017","reasoning":{"effort":"low","summary":"auto"},"input":"mock","prompt_cache_key":"keep"} `
	st, reply := e.post(t, "/v1/responses", native, nil)
	if st != 200 || e.count() != 1 || e.call(0).body != native {
		t.Fatalf("Responses reasoning was converted or rejected: %d %s", st, reply)
	}
	body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning":{"effort":"low"},"messages":[]}`)
	out, key, err := prepareMistralRequest(body, "/v1/chat/completions", "scope")
	var got map[string]json.RawMessage
	if err != nil || json.Unmarshal(out, &got) != nil || key != "apb-mistral-scope" || string(got["prompt_cache_key"]) != `"apb-mistral-scope"` || string(got["reasoning_effort"]) != `"low"` || got["reasoning"] != nil {
		t.Fatalf("alias conversion and cache injection overlapped: %s %v", out, err)
	}
}

func TestMistralEffortCompatibilityScope(t *testing.T) {
	for _, model := range []string{"deepseek-v4-1-flash-1017", "mistral-large-latest", "other-model"} {
		for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/fim/completions"} {
			for _, effort := range []string{"", `"none"`, `"low"`, `"high"`, `"xhigh"`, `"max"`, `null`, `"medium"`} {
				if model == "deepseek-v4-1-flash-1017" && path == "/v1/chat/completions" && effort != `"low"` && effort != `"high"` {
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
