package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

// These are protocol-shape mocks, not claims of model authorization or real
// Codex/Cherry Studio end-to-end validation. Tool payloads must stay lossless.
func TestMistralModelMatrixToolHistoryAndCache(t *testing.T) {
	models := append([]string{"mistral-large-4-0"}, mistralReasoningTestModels...)
	patch := `{"patch":"*** Begin Patch\n*** Update File: file.txt\n@@\n-old\n+new \"quote\" \\ slash\n*** End Patch\n","n":9007199254740993}`
	arguments, _ := json.Marshal(patch)
	denial, _ := json.Marshal("Deny: apply_patch was not approved\nKeep original file unchanged.\n")
	for _, model := range models {
		for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s%s/stream=%v", model, path, stream), func(t *testing.T) {
					e := newMistralEnv(t)
					effort := "max"
					thought := `"reasoning":"prior thought"`
					if model == "mistral-large-4-0" {
						effort, thought = "medium", `"reasoning_content":"prior thought"`
					}
					role := "system"
					if model == "deepseek-v4-1-flash-1017" {
						role = "developer"
					}
					messages := fmt.Sprintf(`[{"role":%q,"content":"instructions"},{"role":"user","content":"patch file"},{"role":"assistant","content":null,%s,"tool_calls":[{"id":"patch_id","type":"function","function":{"name":"apply_patch","arguments":%s}}]},{"role":"tool","tool_call_id":"patch_id","content":%s},{"role":"user","content":"continue without changing files"}]`, role, thought, arguments, denial)
					input := fmt.Sprintf(`[{"type":"reasoning","summary":[{"type":"summary_text","text":"prior thought"}]},{"type":"function_call","call_id":"patch_id","name":"apply_patch","arguments":%s},{"type":"function_call_output","call_id":"patch_id","output":%s},{"role":"user","content":"continue"}]`, arguments, denial)
					tools := `[{"type":"function","function":{"name":"apply_patch","parameters":{"type":"object","properties":{"patch":{"type":"string"}}}}}]`
					var history string
					if path == "/v1/chat/completions" {
						history = `"messages":` + messages
					} else {
						history = `"input":` + input
						tools = `[{"type":"function","name":"apply_patch","parameters":{"type":"object","properties":{"patch":{"type":"string"}}}}]`
					}
					reasoning := fmt.Sprintf(`"reasoning":{"effort":%q}`, effort)
					if model == "mistral-large-4-0" && path == "/v1/chat/completions" {
						reasoning = `"reasoning_effort":"medium"`
					}
					extra := ""
					if path == "/v1/responses" || model == "deepseek-v4-1-flash-1017" {
						extra = `,"store":false`
					}
					if path == "/v1/chat/completions" && model == "deepseek-v4-1-flash-1017" {
						extra += `,"seed":9007199254740993`
					}
					body := fmt.Sprintf(`{ "model":%q,"stream":%v,%s,%s,"tools":%s,"metadata":{"session_id":"mock-session","n":9007199254740993},"prompt_cache_key":"client-cache"%s }`, model, stream, reasoning, history, tools, extra)
					toolCall := fmt.Sprintf(`[{"id":"next_patch","type":"function","function":{"name":"apply_patch","arguments":%s}}]`, arguments)
					content := `[{"type":"thinking","thinking":[{"type":"text","text":"next thought"}]},{"type":"text","text":"No files changed"}]`
					wire := fmt.Sprintf(`{"choices":[{"message":{"content":%s,"tool_calls":%s},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":3}}}`, content, toolCall)
					if stream {
						wire = fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%s,\"tool_calls\":%s},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":4,\"prompt_tokens_details\":{\"cached_tokens\":3}}}\n\ndata: [DONE]\n\n", content, toolCall)
					}
					if path == "/v1/responses" {
						wire = fmt.Sprintf(`{"id":"reply","object":"response","status":"completed","output":[{"type":"function_call","call_id":"next_patch","name":"apply_patch","arguments":%s}],"usage":{"input_tokens":12,"output_tokens":4,"input_tokens_details":{"cached_tokens":3}}}`, arguments)
						if stream {
							wire = "data: {\"type\":\"response.completed\",\"response\":" + wire + "}\n\n"
						}
					}
					e.setHandler(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
						}
						fmt.Fprint(w, wire)
					})
					status, reply := e.post(t, path, body, map[string]string{"Session-Id": "mock-session", "User-Agent": "mock-client"})
					if status != 200 || e.count() != 1 {
						t.Fatalf("matrix request failed/replayed: %d %s", status, reply)
					}
					var original, sent map[string]json.RawMessage
					_ = json.Unmarshal([]byte(body), &original)
					_ = json.Unmarshal([]byte(e.call(0).body), &sent)
					for _, key := range []string{"model", "tools", "metadata", "prompt_cache_key"} {
						if !bytes.Equal(original[key], sent[key]) {
							t.Fatalf("native tool/cache/numeric field changed: %s", key)
						}
					}
					if e.call(0).header.Get("X-Affinity") != "client-cache" {
						t.Fatal("lost existing cache affinity")
					}
					if path == "/v1/responses" {
						if !bytes.Equal(original["input"], sent["input"]) || reply != wire || !bytes.Equal(original["reasoning"], sent["reasoning"]) {
							t.Fatal("native Responses reasoning/tool history/response changed")
						}
					} else {
						if string(sent["reasoning_effort"]) != fmt.Sprintf("%q", effort) {
							t.Fatal("lost model-specific native Chat effort")
						}
						var before, after []map[string]json.RawMessage
						_ = json.Unmarshal(original["messages"], &before)
						_ = json.Unmarshal(sent["messages"], &after)
						if len(after) != len(before) || !bytes.Equal(before[2]["tool_calls"], after[2]["tool_calls"]) || !bytes.Equal(before[3]["content"], after[3]["content"]) || !bytes.Equal(before[3]["tool_call_id"], after[3]["tool_call_id"]) || !bytes.Contains(after[2]["content"], []byte("prior thought")) || after[2]["reasoning"] != nil || after[2]["reasoning_content"] != nil {
							t.Fatal("Deny/apply_patch history, multiline arguments or thought was lost")
						}
						if !strings.Contains(reply, `"reasoning_content":"next thought"`) || !strings.Contains(reply, string(arguments)) || !strings.Contains(reply, `"content":"No files changed"`) {
							t.Fatal("response adaptation lost native thought/answer/tool argument string")
						}
						if model == "deepseek-v4-1-flash-1017" {
							if string(after[0]["role"]) != `"system"` || sent["store"] != nil || sent["seed"] != nil || string(sent["random_seed"]) != "9007199254740993" {
								t.Fatal("DeepSeek-specific compatibility regressed")
							}
						} else if !bytes.Equal(before[0]["role"], after[0]["role"]) || !bytes.Equal(original["store"], sent["store"]) || !bytes.Equal(original["seed"], sent["seed"]) || sent["random_seed"] != nil {
							t.Fatal("DeepSeek-specific parameter cleanup leaked to another model")
						}
					}
					rec := mistralTerminalRequestLog(t, e)
					if rec.Result != "success" || rec.CachedTokens == nil || *rec.CachedTokens != 3 {
						t.Fatal("lost actual usage/cache accounting")
					}
				})
			}
		}
	}
}

func TestMistralApplyPatchRefusalDoesNotStripOrReplay(t *testing.T) {
	for _, model := range mistralReasoningTestModels {
		t.Run(model, func(t *testing.T) {
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":{"message":"[unsupported_tool] unsupported function tool type \"apply_patch\""}}`)
			})
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"mock"}],"tools":[{"type":"function","function":{"name":"apply_patch"}}]}`, model)
			status, reply := e.post(t, "/v1/chat/completions", body, nil)
			if status != 400 || e.count() != 1 || !strings.Contains(e.call(0).body, `"name":"apply_patch"`) || !strings.Contains(reply, "unsupported_tool") {
				t.Fatalf("tool refusal hidden by removal/replay: %d %s", status, reply)
			}
		})
	}
}

func TestMistralGLMDoesNotInheritDeepSeekParameters(t *testing.T) {
	for _, model := range []string{"zai-glm-5-3", "zai-glm-5-3-flash"} {
		body := []byte(fmt.Sprintf(` {"model":%q,"reasoning_effort":"high","store":false,"seed":42,"verbosity":"high","logit_bias":{"1":1},"user":"native-label","messages":[{"role":"developer","content":"native"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image.png","MimeType":""}}]}]} `, model))
		out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("DeepSeek-only cleanup escaped scope: %s %s %v", model, out, err)
		}
	}
}

func TestMistralThreeModelInvalidChatControlsAreLocal(t *testing.T) {
	for _, model := range mistralReasoningTestModels {
		t.Run(model, func(t *testing.T) {
			e := newMistralEnv(t)
			for _, fields := range []string{
				`"thinking":{"type":"enabled","budget_tokens":1024}`,
				`"thinking":{"type":"enabled","type":"disabled"}`,
				`"thinking":null`, `"enable_thinking":null`,
				`"enable_thinking":true,"enable_thinking":false`,
				`"reasoning":{"effort":"max","summary":"private-body"}`,
				`"reasoning":{}`,
				`"messages":[{"role":"assistant","reasoning":"a","reasoning_content":"b"}]`,
				`"messages":[{"role":"assistant","reasoning":{"text":"private-body"}}]`,
			} {
				status, reply := e.post(t, "/v1/chat/completions", fmt.Sprintf(`{"model":%q,%s}`, model, fields), nil)
				if status != 400 || e.count() != 0 || !strings.Contains(reply, "invalid_mistral_request") || strings.Contains(reply, "private-body") {
					t.Fatalf("invalid controls/history forwarded or leaked: %d %s", status, reply)
				}
			}
		})
	}
}

func TestMistralReasoningPolicyDoesNotChangeOtherPool(t *testing.T) {
	e := newMistralEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Upstream.RuntimeBase = e.up.URL + "/v1"
		c.Accounts = append(c.Accounts, config.Account{ID: "native-other", Group: "default", RuntimeKey: "sk-amr-mock-other", Enabled: true})
		c.ClientKeys = append(c.ClientKeys, config.ClientKey{ID: "other-client", Group: "default", KeyHash: config.HashKey("mock-other-client"), Enabled: true})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, model := range mistralReasoningTestModels {
		for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
			body := fmt.Sprintf(` {"model":%q,"messages":[{"role":"assistant","reasoning":"native","content":"answer"}],"input":"mock","thinking":{"type":"disabled"}} `, model)
			before := e.count()
			if st, reply := e.post(t, path, body, map[string]string{"Authorization": "Bearer mock-other-client"}); st != 200 || e.count() != before+1 || e.call(before).body != body {
				t.Fatalf("other pool received Mistral defaults: %d %s", st, reply)
			}
		}
	}
}
