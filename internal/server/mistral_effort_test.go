package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var mistralReasoningTestModels = []string{"deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash"}

func TestMistralThreeModelChatEffort(t *testing.T) {
	for _, model := range mistralReasoningTestModels {
		t.Run(model, func(t *testing.T) {
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
				{"max", `"reasoning_effort":"max",`, `"max"`},
				{"xhigh", `"reasoning_effort":"xhigh",`, `"high"`},
				{"unknown tier", `"reasoning_effort":"minimal",`, `"high"`},
				{"empty tier", `"reasoning_effort":"",`, `"high"`},
				{"invalid number", `"reasoning_effort":5,`, `"high"`},
				{"invalid boolean", `"reasoning_effort":false,`, `"high"`},
				{"invalid object", `"reasoning_effort":{},`, `"high"`},
				{"invalid array", `"reasoning_effort":[],`, `"high"`},
				{"nested high", `"reasoning":{"effort":"high"},`, `"high"`},
				{"nested none", `"reasoning":{"effort":"none"},`, `"high"`},
				{"nested low", `"reasoning":{"effort":"low"},`, `"low"`},
				{"nested max", `"reasoning":{"effort":"max"},`, `"max"`},
				{"nested xhigh", `"reasoning":{"effort":"xhigh"},`, `"high"`},
				{"nested unknown", `"reasoning":{"effort":"minimal"},`, `"high"`},
				{"nested invalid type", `"reasoning":{"effort":{}},`, `"high"`},
				{"nested medium", `"reasoning":{"effort":"medium"},`, `"high"`},
				{"nested null", `"reasoning":{"effort":null},`, `"high"`},
				{"nested null enabled", `"reasoning":{"effort":null},"thinking":{"type":"enabled"},`, `"high"`},
				{"nested null disabled", `"reasoning":{"effort":null},"enable_thinking":false,`, `"high"`},
				{"nested explicit enabled", `"reasoning":{"effort":"low"},"enable_thinking":true,`, `"low"`},
				{"nested explicit disabled", `"reasoning":{"effort":"none"},"thinking":{"type":"disabled"},`, `"high"`},
				{"equal effort aliases", `"reasoning_effort":"low","reasoning":{"effort":"low"},`, `"low"`},
				{"equal max aliases", `"reasoning_effort":"max","reasoning":{"effort":"max"},`, `"max"`},
				{"equal null aliases", `"reasoning":{"effort":null},"reasoning_effort":null,`, `"high"`},
				{"null and high aliases", `"reasoning":{"effort":null},"reasoning_effort":"high",`, `"high"`},
				{"high and null aliases", `"reasoning":{"effort":"high"},"reasoning_effort":null,`, `"high"`},
				{"equal normalized aliases", `"reasoning_effort":"medium","reasoning":{"effort":"high"},`, `"high"`},
				{"equal normalized reverse aliases", `"reasoning":{"effort":"medium"},"reasoning_effort":"high",`, `"high"`},
				{"escaped alias values", `"reasoning_effort":"m\u0061x","reasoning":{"effort":"max"},`, `"m\u0061x"`},
				{"disabled with low", `"thinking":{"type":"disabled"},"reasoning_effort":"low",`, `"low"`},
				{"disabled with medium", `"thinking":{"type":"disabled"},"reasoning_effort":"medium",`, `"high"`},
				{"disabled with high", `"thinking":{"type":"disabled"},"reasoning_effort":"high",`, `"high"`},
				{"disabled with xhigh", `"thinking":{"type":"disabled"},"reasoning_effort":"xhigh",`, `"high"`},
				{"disabled with max", `"thinking":{"type":"disabled"},"reasoning_effort":"max",`, `"max"`},
				{"boolean disabled with xhigh", `"enable_thinking":false,"reasoning_effort":"xhigh",`, `"high"`},
				{"boolean disabled with max", `"enable_thinking":false,"reasoning_effort":"max",`, `"max"`},
				{"enabled with none", `"thinking":{"type":"enabled"},"reasoning_effort":"none",`, `"high"`},
				{"boolean enabled with none", `"enable_thinking":true,"reasoning_effort":"none",`, `"high"`},
				{"disabled with nested low", `"thinking":{"type":"disabled"},"reasoning":{"effort":"low"},`, `"low"`},
				{"disabled with nested max", `"enable_thinking":false,"reasoning":{"effort":"max"},`, `"max"`},
				{"enabled with nested none", `"thinking":{"type":"enabled"},"reasoning":{"effort":"none"},`, `"high"`},
				{"switch disagreement with high", `"thinking":{"type":"enabled"},"enable_thinking":false,"reasoning_effort":"high",`, `"high"`},
				{"switch disagreement with none", `"thinking":{"type":"disabled"},"enable_thinking":true,"reasoning_effort":"none",`, `"high"`},
				{"switch disagreement without effort", `"thinking":{"type":"enabled"},"enable_thinking":false,`, `"high"`},
				{"switch disagreement with max", `"thinking":{"type":"enabled"},"enable_thinking":false,"reasoning":{"effort":"max"},`, `"max"`},
			} {
				t.Run(tc.name, func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{ "model":%q, %s"messages":[{"role":"user","content":"mock"}], "tools":[{"n":9007199254740993}], "metadata": {"session_id":"client-session"}, "prompt_cache_key":null, "max_tokens":16000 }`, model, tc.fields))
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
					if st != 200 || e.count() != before+1 || e.call(before).body != string(out) {
						t.Fatalf("prepared body did not reach mock exactly once: %d %s", st, reply)
					}
				})
			}
		})
	}
}

func TestMistralThreeModelResponsesEffort(t *testing.T) {
	for _, model := range mistralReasoningTestModels {
		t.Run(model, func(t *testing.T) {
			e := newMistralEnv(t)
			for _, tc := range []struct{ fields, want string }{
				{"", `"high"`},
				{`"reasoning":null,`, `"high"`},
				{`"reasoning":{},`, `"high"`},
				{`"reasoning":{"summary":"auto","n":9007199254740993},`, `"high"`},
				{`"reasoning":{"effort":"low"},`, `"low"`},
				{`"reasoning":{"effort":"high"},`, `"high"`},
				{`"reasoning":{"effort":"max"},`, `"max"`},
				{`"reasoning":{"effort":"m\u0061x","summary":"auto","n":9007199254740993},`, `"m\u0061x"`},
				{`"reasoning":{"effort":null},`, `"high"`},
				{`"reasoning":{"effort":"none"},`, `"high"`},
				{`"reasoning":{"effort":"medium"},`, `"high"`},
				{`"reasoning":{"effort":"xhigh"},`, `"high"`},
				{`"reasoning":{"effort":{}},`, `"high"`},
				{`"reasoning_effort":"max",`, `"max"`},
				{`"reasoning_effort":"low","reasoning":{"effort":"low","summary":"auto"},`, `"low"`},
				{`"reasoning_effort":"max","reasoning":{"effort":"max"},`, `"max"`},
				{`"reasoning_effort":"max","reasoning":{"summary":"auto"},`, `"max"`},
			} {
				t.Run(tc.fields, func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{ "model":%q, %s"input":[{"type":"reasoning","summary":[]}], "tools":[{"type":"function","name":"apply_patch"}], "metadata":{"n":9007199254740993}, "prompt_cache_key":"keep", "max_output_tokens":16000 }`, model, tc.fields))
					out, generated, err := prepareMistralRequest(body, "/v1/responses", "scope")
					var original, got map[string]json.RawMessage
					var reasoning map[string]json.RawMessage
					if err != nil || json.Unmarshal(out, &got) != nil || json.Unmarshal(got["reasoning"], &reasoning) != nil || generated != "" || string(reasoning["effort"]) != tc.want || got["reasoning_effort"] != nil {
						t.Fatalf("wrong native Responses effort: %s %v", out, err)
					}
					_ = json.Unmarshal(body, &original)
					for _, key := range []string{"model", "input", "tools", "metadata", "prompt_cache_key", "max_output_tokens"} {
						if !bytes.Equal(got[key], original[key]) {
							t.Fatalf("Responses native field changed: %s", key)
						}
					}
					var prior map[string]json.RawMessage
					_ = json.Unmarshal(original["reasoning"], &prior)
					for key, raw := range prior {
						if key != "effort" && !bytes.Equal(reasoning[key], raw) {
							t.Fatal("discarded native reasoning fields")
						}
					}
					before := e.count()
					if st, reply := e.post(t, "/v1/responses", string(body), nil); st != 200 || e.count() != before+1 || e.call(before).body != string(out) {
						t.Fatalf("Responses effort lost before upstream: %d %s", st, reply)
					}
				})
			}
		})
	}
}

func TestMistralThreeModelReasoningRejectsAmbiguity(t *testing.T) {
	for _, model := range mistralReasoningTestModels {
		for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
			t.Run(model+path, func(t *testing.T) {
				e := newMistralEnv(t)
				for _, fields := range []string{
					`"reasoning_effort":"high","reasoning":{"effort":"max"}`,
					`"reasoning_effort":"max","reasoning":{"effort":"high"}`,
					`"reasoning_effort":"low","reasoning":{"effort":"max"}`,
					`"reasoning_effort":"max","reasoning_effort":"max"`,
					`"reasoning":{"effort":"max","eff\u006frt":"max"}`,
					`"reasoning":{"effort":"low"},"reas\u006fning":{"effort":"low"}`,
					`"reasoning":false`,
					`"reasoning":"private-body"`,
				} {
					st, reply := e.post(t, path, fmt.Sprintf(`{"model":%q,%s,"messages":[],"input":"mock"}`, model, fields), nil)
					if st != 400 || e.count() != 0 || !strings.Contains(reply, "invalid_mistral_request") || strings.Contains(reply, "private-body") {
						t.Fatalf("ambiguous effort forwarded/disclosed: %d %s", st, reply)
					}
				}
			})
		}
	}
}

func TestMistralEffortCompatibilityScope(t *testing.T) {
	for _, model := range append([]string{"mistral-large-4-0", "mistral-large-latest", "other-model"}, mistralReasoningTestModels...) {
		for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/responses/input_tokens", "/v1/fim/completions"} {
			if mistralReasoningModel(model) && (path == "/v1/chat/completions" || path == "/v1/responses") {
				continue
			}
			for _, fields := range []string{"", `"reasoning_effort":"medium",`, `"reasoning":{"effort":"max","summary":"auto"},`, `"reasoning_effort":null,`} {
				body := []byte(fmt.Sprintf(` {"model":%q,%s"messages":[],"input":"mock","prompt_cache_key":"keep"} `, model, fields))
				out, generated, err := prepareMistralRequest(body, path, "scope")
				if err != nil || generated != "" || !bytes.Equal(out, body) {
					t.Fatalf("reasoning adaptation escaped model/path scope: %s %s %s", model, path, fields)
				}
			}
		}
	}
}
