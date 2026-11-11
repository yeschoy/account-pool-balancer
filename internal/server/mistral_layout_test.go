package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Real local HTTP: the client sends back only the two ordinary strings, no
// carrier/annotations/extra provider metadata and no client changes.
func TestMistralLayoutServerOnlyHTTPRoundTrip(t *testing.T) {
	for _, model := range []string{"deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", model, stream), func(t *testing.T) {
				e := newMistralEnv(t)
				native := `[{"type":"text","text":" before 中文 "},{"type":"thinking","thinking":[{"type":"text","text":" reason "},{"type":"text","text":"中文\n"}],"closed":true,"signature":null},{"type":"text","text":" after\t "}]`
				e.setHandler(func(w http.ResponseWriter, r *http.Request) {
					if e.count() != 1 {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if !stream {
						fmt.Fprintf(w, `{"id":"local-reply","choices":[{"index":0,"message":{"role":"assistant","content":%s},"finish_reason":"stop"}]}`, native)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, content := range []string{
						`[{"type":"text","text":" before "}]`,
						`[{"type":"text","text":"中文 "}]`,
						`[{"type":"thinking","thinking":[{"type":"text","text":" reason "},{"type":"text","text":"中"}],"closed":false,"signature":null}]`,
						`[{"type":"thinking","thinking":[{"type":"text","text":"文\n"}],"closed":true}]`,
						`[{"type":"text","text":" after\t "}]`,
					} {
						fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":%s}}]}\n\n", content)
						w.(http.Flusher).Flush()
					}
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				})
				initial := fmt.Sprintf(`{"model":%q,"stream":%v,"messages":[{"role":"system","content":"original system"},{"role":"user","content":"original question"}]}`, model, stream)
				status, reply := e.post(t, "/v1/chat/completions", initial, map[string]string{"Session-Id": "local-layout-session"})
				if status != 200 {
					t.Fatalf("first local upload failed: %d %s", status, reply)
				}
				var answer, thought strings.Builder
				consume := func(raw []byte, field string) {
					var root struct {
						Choices []struct {
							Message, Delta struct {
								Content   string `json:"content"`
								Reasoning string `json:"reasoning_content"`
							}
						}
					}
					if json.Unmarshal(raw, &root) == nil && len(root.Choices) != 0 {
						m := root.Choices[0].Message
						if field == "delta" {
							m = root.Choices[0].Delta
						}
						answer.WriteString(m.Content)
						thought.WriteString(m.Reasoning)
					}
				}
				if stream {
					for _, event := range strings.Split(reply, "\n\n") {
						_, data, _ := sseFields([]byte(event))
						consume(data, "delta")
					}
				} else {
					consume([]byte(reply), "message")
				}
				if answer.String() != " before 中文  after\t " || thought.String() != " reason 中文\n" {
					t.Fatalf("visible answer or reasoning changed: answer=%q reasoning=%q reply=%s", answer.String(), thought.String(), reply)
				}
				a, _ := json.Marshal(answer.String())
				rc, _ := json.Marshal(thought.String())
				tools := `[ {"type":"function","function":{"name":"original","parameters":{"type":"object","properties":{"z":{"type":"integer"},"a":{"type":"string"}}}}} ]`
				calls := `[{"id":"original-call","function":{"name":"original","arguments":"{\"n\":9007199254740993}"}}]`
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"system","content":"original system"},{"role":"user","content":"original question"},{"role":"assistant","content":%s,"reasoning_content":%s,"tool_calls":%s},{"role":"tool","tool_call_id":"original-call","content":"original result"},{"role":"user","content":"next question"}],"tools":%s,"prompt_cache_key":"original-cache","metadata":{"n":9007199254740993}}`, model, a, rc, calls, tools)
				status, reply = e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "local-layout-session"})
				if status != 200 || e.count() != 2 {
					t.Fatalf("replay failed/switched: %d %s; attempts=%d", status, reply, e.count())
				}
				var root map[string]json.RawMessage
				_ = json.Unmarshal([]byte(e.call(1).body), &root)
				var messages []map[string]json.RawMessage
				_ = json.Unmarshal(root["messages"], &messages)
				if len(messages) != 5 || !bytes.Equal(root["tools"], []byte(tools)) || !bytes.Equal(messages[2]["tool_calls"], []byte(calls)) || !bytes.Contains(root["metadata"], []byte("9007199254740993")) {
					t.Fatal("history, schema order, tools or exact numbers changed")
				}
				var chunks []map[string]json.RawMessage
				_ = json.Unmarshal(messages[2]["content"], &chunks)
				if len(chunks) != 3 || string(chunks[0]["type"]) != `"text"` || string(chunks[1]["type"]) != `"thinking"` || string(chunks[2]["type"]) != `"text"` {
					t.Fatalf("server-only replay lost original text→thinking→text order: %s", messages[2]["content"])
				}
				if string(chunks[0]["text"]) != `" before 中文 "` || string(chunks[2]["text"]) != `" after\t "` || string(chunks[1]["closed"]) != "true" || string(chunks[1]["signature"]) != "null" {
					t.Fatal("whitespace or native metadata lost")
				}
				var parts []map[string]json.RawMessage
				_ = json.Unmarshal(chunks[1]["thinking"], &parts)
				if len(parts) != 2 || string(parts[0]["text"]) != `" reason "` || string(parts[1]["text"]) != `"中文\n"` {
					t.Fatal("thinking part boundary/continuation changed")
				}
				// Growing history must reproduce the exact same prepared old
				// assistant bytes, cache affinity and original routing HMAC.
				oldAssistant := bytes.Clone(root["messages"])
				var oldRaw []json.RawMessage
				_ = json.Unmarshal(oldAssistant, &oldRaw)
				growing := strings.Replace(body, `{"role":"user","content":"next question"}`, `{"role":"user","content":"next question"},{"role":"assistant","content":"ok"},{"role":"user","content":"third question"}`, 1)
				status, _ = e.post(t, "/v1/chat/completions", growing, map[string]string{"Session-Id": "local-layout-session"})
				var laterRoot map[string]json.RawMessage
				_ = json.Unmarshal([]byte(e.call(2).body), &laterRoot)
				var laterRaw []json.RawMessage
				_ = json.Unmarshal(laterRoot["messages"], &laterRaw)
				if status != 200 || len(laterRaw) != 7 || !bytes.Equal(oldRaw[2], laterRaw[2]) || e.call(1).header.Get("X-Affinity") != e.call(2).header.Get("X-Affinity") {
					t.Fatal("restored old history/cache affinity changed on later turn")
				}
				cfg := e.s.cfg.Get().ForProvider("mistral")
				fingerprint := ""
				for _, input := range []string{body, growing} {
					obj, err := parseRoutingBodyWithHistory([]byte(input), false, false)
					if err != nil {
						t.Fatal(err)
					}
					h := make(http.Header)
					h.Set("Session-Id", "local-layout-session")
					id := e.s.mistralIdentity(cfg, "mistral\x00client1", h, obj)
					if id.Fingerprint == "" || id.Source != "header" || fingerprint != "" && fingerprint != id.Fingerprint {
						t.Fatal("original identity was replaced by layout state")
					}
					fingerprint = id.Fingerprint
				}
			})
		}
	}
}
