package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// These are local HTTP wire checks, not a claim that Mistral accepts every
// OpenAI parameter or that a production request's encrypted upload was observed.
func TestMistralHTTPParamUploadOrder132Tools(t *testing.T) {
	for _, model := range []string{"deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash"} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/explicit=%t", model, explicit), func(t *testing.T) {
				e := newMistralEnv(t)
				enableCacheAuditForTest(t, e)
				var tools strings.Builder
				tools.WriteByte('[')
				for i := 131; i >= 0; i-- {
					if i < 131 {
						tools.WriteByte(',')
					}
					fmt.Fprintf(&tools, `{ "type":"function", "function":{ "name":"tool_%03d", "parameters":{ "type":"object", "properties":{ "z_last":{"type":"string"}, "a_first":{"type":"integer"} }, "required":["z_last","a_first"] } } }`, i)
				}
				tools.WriteByte(']')
				assistant := func(label string, tool bool) json.RawMessage {
					call := ""
					if tool {
						call = fmt.Sprintf(`,"tool_calls":[{"id":"call-%s","type":"function","function":{"name":"tool_131","arguments":"{\"z_last\":\"keep\",\"a_first\":9007199254740993}"}}]`, label)
					}
					return json.RawMessage(fmt.Sprintf(`{"role":"assistant","reasoning":"thought-%s","reasoning_content":" ","content":"answer-%s"%s}`, label, label, call))
				}
				system, _ := json.Marshal(strings.Repeat("stable synthetic instruction 中文 ", 2300))
				messages := []json.RawMessage{
					json.RawMessage(`{"role":"system","content":` + string(system) + `}`),
					json.RawMessage(`{"role":"user","content":"first-user-order-marker"}`),
				}
				for i := 0; i < 7; i++ {
					label := fmt.Sprint(i)
					messages = append(messages, assistant(label, true),
						json.RawMessage(fmt.Sprintf(`{"role":"tool","tool_call_id":"call-%s","content":"tool-result-%s"}`, label, label)),
						json.RawMessage(fmt.Sprintf(`{"role":"user","content":"user-%s"}`, label)))
				}
				messages = append(messages, assistant("last", false), json.RawMessage(`{"role":"user","content":"last-initial-user"}`))
				if len(messages) != 25 {
					t.Fatal("fixture must start at 25 messages")
				}
				headers := map[string]string{}
				if explicit {
					headers["Session-Id"] = "synthetic-param-order-session"
				}
				var previous []json.RawMessage
				var firstCacheKey, firstAffinity string
				for turn := 0; turn < 3; turn++ {
					history, err := json.Marshal(messages)
					if err != nil {
						t.Fatal(err)
					}
					// max_completion_tokens is intentionally the observed client
					// spelling. Verify preservation, not undocumented API support.
					body := fmt.Sprintf(`{ "tools":%s, "model":%q, "messages":%s, "max_completion_tokens":4096, "reasoning_effort":"max", "temperature":0.30e0, "top_p":0.90, "parallel_tool_calls":false, "stream":false, "metadata":{"z":9007199254740993,"a":1} }`, tools.String(), model, history)
					if len(body) <= 64<<10 {
						t.Fatal("must check a full body beyond detail-preview limits")
					}
					if status, _ := e.post(t, "/v1/chat/completions", body, headers); status != 200 || e.count() != turn+1 {
						t.Fatalf("turn %d status=%d, mock attempts=%d", turn, status, e.count())
					}
					call := e.call(turn)
					_, source := mistralOrderedFieldsForTest(t, body)
					_, sent := mistralOrderedFieldsForTest(t, call.body)
					for _, key := range []string{"model", "tools", "max_completion_tokens", "reasoning_effort", "temperature", "top_p", "parallel_tool_calls", "stream", "metadata"} {
						if !bytes.Equal(source[key], sent[key]) {
							t.Fatalf("turn %d altered parameter/schema bytes: %s", turn, key)
						}
					}
					if sent["max_tokens"] != nil {
						t.Fatal("current adapter unexpectedly renamed the preserved client limit")
					}
					cacheKey, affinity := string(sent["prompt_cache_key"]), call.header.Get("X-Affinity")
					if turn == 0 {
						firstCacheKey, firstAffinity = cacheKey, affinity
					}
					if cacheKey == "" || affinity == "" || cacheKey != firstCacheKey || affinity != firstAffinity {
						t.Fatal("stable input changed cache hint during parameter preparation")
					}
					var outgoing []json.RawMessage
					if err := json.Unmarshal(sent["messages"], &outgoing); err != nil || len(outgoing) != len(messages) {
						t.Fatal("messages were omitted/duplicated", err)
					}
					for i, original := range messages {
						var want, actual map[string]json.RawMessage
						if json.Unmarshal(original, &want) != nil || json.Unmarshal(outgoing[i], &actual) != nil {
							t.Fatal("invalid message")
						}
						for _, key := range []string{"role", "tool_calls", "tool_call_id"} {
							if !bytes.Equal(want[key], actual[key]) {
								t.Fatalf("turn %d moved/changed message %d association %s", turn, i, key)
							}
						}
						if string(want["role"]) != `"assistant"` {
							if !bytes.Equal(want["content"], actual["content"]) {
								t.Fatalf("turn %d moved/changed user/tool/system message %d", turn, i)
							}
						} else {
							var chunks []struct {
								Type, Text string
								Thinking   []struct{ Type, Text string }
							}
							var thought, answer string
							if json.Unmarshal(want["reasoning"], &thought) != nil || json.Unmarshal(want["content"], &answer) != nil || json.Unmarshal(actual["content"], &chunks) != nil || len(chunks) != 2 {
								t.Fatal("assistant thought/answer representation was lost")
							}
							if chunks[0].Type != "thinking" || len(chunks[0].Thinking) != 1 || chunks[0].Thinking[0].Text != thought || chunks[1].Type != "text" || chunks[1].Text != answer {
								t.Fatalf("turn %d moved/changed assistant message %d", turn, i)
							}
						}
					}
					for i, old := range previous {
						if !bytes.Equal(old, outgoing[i]) {
							t.Fatalf("turn %d rewrote or reordered previous prepared message %d", turn, i)
						}
					}
					previous = outgoing
					messages = append(messages, assistant(fmt.Sprintf("new-%d-a", turn), false),
						json.RawMessage(fmt.Sprintf(`{"role":"user","content":"new-%d-a"}`, turn)),
						assistant(fmt.Sprintf("new-%d-b", turn), false),
						json.RawMessage(fmt.Sprintf(`{"role":"user","content":"new-%d-b"}`, turn)))
				}
				records := cacheAuditRecordsForTest(t, e)
				if len(records) != 3 {
					t.Fatalf("expected 3 complete audit records, got %d", len(records))
				}
				for _, record := range records {
					if record.CacheAudit == nil || len(record.CacheAudit.Attempts) != 1 {
						t.Fatal("missing final-attempt audit")
					}
					attempt := record.CacheAudit.Attempts[0]
					if !attempt.Preparation.BodyEqual || len(attempt.BodyReads) != 1 || !attempt.BodyReads[0].MatchesExpected || !attempt.BodyReads[0].Complete || attempt.WriteSuccesses != 1 || attempt.WriteFailures != 0 {
						t.Fatal("actual mock HTTP upload differs from prepared bytes")
					}
					if record.CacheAudit.PreviousPrepared != nil && !*record.CacheAudit.PreviousPrepared.Comparison.PreviousSequenceRetained {
						t.Fatal("prepared history prefix was not retained")
					}
				}
			})
		}
	}
}
