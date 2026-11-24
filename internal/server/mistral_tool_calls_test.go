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

const mistralForeignToolSignature = `"signature":"synthetic-sig","extra_content":{"google":{"thoughtSignature":"synthetic-sig"}}`

func TestMistralDeepSeekForeignToolSignaturesHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			e := newMistralEnv(t)
			wire := `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":2}}`
			if stream {
				wire = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":2}}\n\ndata: [DONE]\n\n"
			}
			e.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var req struct {
					Messages []struct {
						ToolCalls []map[string]json.RawMessage `json:"tool_calls"`
					} `json:"messages"`
				}
				if json.Unmarshal([]byte(e.call(0).body), &req) != nil || len(req.Messages) != 8 || len(req.Messages[4].ToolCalls) != 3 {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				for i, call := range req.Messages[4].ToolCalls {
					if call["signature"] != nil || call["extra_content"] != nil {
						w.WriteHeader(http.StatusUnprocessableEntity)
						fmt.Fprintf(w, `{"detail":[{"loc":["body","messages",4,"assistant","tool_calls",%d,"signature"],"type":"extra_forbidden"},{"loc":["body","messages",4,"assistant","tool_calls",%d,"extra_content"],"type":"extra_forbidden"}]}`, i, i)
						return
					}
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				fmt.Fprint(w, wire)
			})
			const oldCall = `{"id":"call-A","type":"function","function":{"name":"lookup","arguments":"{\"n\":9007199254740993}"}}`
			const oldCall2 = `{"id":"call-B","function":{"name":"read","arguments":"{}"},"type":"function"}`
			const escaped = `"extra_\u0063ontent":{"g\u006fogle":{"thought\u0053ignature":"synthetic-\u0073ig"}},"s\u0069gnature":"synthetic-sig"`
			const native = `{"id":"call-C","type":"function","function":{"name":"native","arguments":"{\"signature\":\"not a field to remove\"}"}}`
			base := fmt.Sprintf(` {"model":"deepseek-v4-1-flash-1017","stream":%v,"reasoning_effort":"max","prompt_cache_key":"tool-cache","tools":[{"type":"function","function":{"name":"lookup","parameters":{"n":9007199254740993}}}],"messages":[{"role":"system","content":"instructions"},{"role":"user","content":"first"},{"role":"assistant","content":"earlier answer"},{"role":"user","content":"run tools"},{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"native thought"}],"signature":null}],"tool_calls":[%s,%s,%s]},{"role":"tool","tool_call_id":"call-A","content":"first result"},{"role":"tool","tool_call_id":"call-B","content":"second result"},{"role":"tool","tool_call_id":"call-C","content":"third result"}]} `, stream, oldCall, native, oldCall2)
			body := strings.Replace(base, oldCall, `{`+escaped+`,`+oldCall[1:], 1)
			body = strings.Replace(body, oldCall2, strings.TrimSuffix(oldCall2, `}`)+`,`+mistralForeignToolSignature+`}`, 1)
			status, reply := e.post(t, "/v1/chat/completions", body, nil)
			if status != 200 || e.count() != 1 || reply != wire {
				t.Fatalf("tool metadata triggered upstream rejection, replay or changed reply: %d calls=%d %s", status, e.count(), reply)
			}
			if sent := e.call(0); sent.body != base || sent.header.Get("X-Affinity") != "tool-cache" {
				t.Fatal("changed bytes outside the foreign metadata or changed cache affinity")
			}
			rec := mistralTerminalRequestLog(t, e)
			if rec.Result != "success" || len(rec.Attempts) != 1 || rec.InputTokens == nil || *rec.InputTokens != 100 {
				t.Fatal("lost usage or attempt accounting")
			}
		})
	}
}

func TestMistralDeepSeekForeignToolSignaturesScope(t *testing.T) {
	const native = `"id":"call","type":"function","function":{"name":"lookup","arguments":"{}"}`
	const exact = `{` + native + `,` + mistralForeignToolSignature + `}`
	for _, tc := range []struct{ name, model, path, role, call string }{
		{"GLM", "zai-glm-5-3", "/v1/chat/completions", "assistant", exact},
		{"non-exact model", "deepseek-v4-1-flash-1017-other", "/v1/chat/completions", "assistant", exact},
		{"non-chat", "deepseek-v4-1-flash-1017", "/v1/responses", "assistant", exact},
		{"non-assistant", "deepseek-v4-1-flash-1017", "/v1/chat/completions", "user", exact},
		{"unpaired flat signature", "deepseek-v4-1-flash-1017", "/v1/chat/completions", "assistant", `{` + native + `,"signature":"standalone"}`},
		{"unpaired Google metadata", "deepseek-v4-1-flash-1017", "/v1/chat/completions", "assistant", `{` + native + `,"extra_content":{"google":{"thoughtSignature":"synthetic-sig"}}}`},
		{"conflicting values", "deepseek-v4-1-flash-1017", "/v1/chat/completions", "assistant", `{` + native + `,"signature":"different","extra_content":{"google":{"thoughtSignature":"synthetic-sig"}}}`},
		{"empty values", "deepseek-v4-1-flash-1017", "/v1/chat/completions", "assistant", `{` + native + `,"signature":"","extra_content":{"google":{"thoughtSignature":""}}}`},
		{"additional metadata", "deepseek-v4-1-flash-1017", "/v1/chat/completions", "assistant", `{` + native + `,"signature":"synthetic-sig","extra_content":{"google":{"thoughtSignature":"synthetic-sig","other":true}}}`},
		{"additional provider", "deepseek-v4-1-flash-1017", "/v1/chat/completions", "assistant", `{` + native + `,"signature":"synthetic-sig","extra_content":{"google":{"thoughtSignature":"synthetic-sig"},"other":true}}`},
		{"unknown call type", "deepseek-v4-1-flash-1017", "/v1/chat/completions", "assistant", strings.Replace(exact, `"function"`, `"custom"`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			effort := `"reasoning_effort":"high"`
			if tc.path == "/v1/responses" {
				effort = `"reasoning":{"effort":"high"}`
			}
			body := []byte(fmt.Sprintf(` {"model":%q,%s,"messages":[{"role":%q,"tool_calls":[%s]}]} `, tc.model, effort, tc.role, tc.call))
			out, _, err := prepareMistralRequest(body, tc.path, "")
			if err != nil || !bytes.Equal(out, body) {
				t.Fatalf("changed data outside the observed scope: %s %v", out, err)
			}
		})
	}
	t.Run("other provider", func(t *testing.T) {
		e := newMistralEnv(t)
		_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
			c.Upstream.RuntimeBase = e.up.URL + "/v1"
			c.Accounts = append(c.Accounts, config.Account{ID: "other-account", Group: "default", RuntimeKey: "sk-amr-other-runtime-secret", Enabled: true, MaxConc: 1})
			c.ClientKeys = append(c.ClientKeys, config.ClientKey{ID: "other-provider", Group: "default", KeyHash: config.HashKey("other-client"), Enabled: true})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		body := ` {"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant","tool_calls":[` + exact + `]}]} `
		status, reply := e.post(t, "/v1/chat/completions", body, map[string]string{"Authorization": "Bearer other-client"})
		if status != 200 || e.count() != 1 || e.call(0).body != body {
			t.Fatalf("foreign tool metadata adaptation escaped Mistral: %d %s", status, reply)
		}
	})
}

func TestMistralDeepSeekForeignToolSignatureDuplicatesAreLocal(t *testing.T) {
	e := newMistralEnv(t)
	for _, tc := range []struct{ field, calls string }{
		{"tool_calls", `"tool_calls":null,"tool_\u0063alls":[{` + mistralForeignToolSignature + `}]`},
		{"tool_calls", `"tool_\u0063alls":[{` + mistralForeignToolSignature + `}],"tool_calls":null`},
		{"signature", `"tool_calls":[{"s\u0069gnature":"other",` + mistralForeignToolSignature + `}]`},
		{"extra_content", `"tool_calls":[{"extra_\u0063ontent":null,` + mistralForeignToolSignature + `}]`},
		{"google", `"tool_calls":[{"signature":"synthetic-sig","extra_content":{"g\u006fogle":null,"google":{"thoughtSignature":"synthetic-sig"}}}]`},
		{"thoughtSignature", `"tool_calls":[{"signature":"synthetic-sig","extra_content":{"google":{"thought\u0053ignature":null,"thoughtSignature":"synthetic-sig"}}}]`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			body := `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant",` + tc.calls + `}]}`
			status, reply := e.post(t, "/v1/chat/completions", body, nil)
			if status != 400 || e.count() != 0 || !strings.Contains(reply, "duplicate "+tc.field) || strings.Contains(reply, "synthetic-sig") {
				t.Fatalf("duplicate did not fail locally or echoed input: %d %s", status, reply)
			}
		})
	}
}

func TestMistralToolCallExtraContentValidationLocation(t *testing.T) {
	body := []byte(`{"detail":[{"loc":["body","messages",4,"assistant","tool_calls",1,"extra_content"],"type":"extra_forbidden","input":{"google":{"thoughtSignature":"synthetic-sig"}}},{"loc":["body","extra_content"],"type":"extra_forbidden","input":"private"}]}`)
	got, err := json.Marshal(mistralValidationDetails(body))
	const want = `[{"loc":["body","messages",4,"assistant","tool_calls",1,"extra_content"],"type":"extra_forbidden"},{"loc":["body","[other]"],"type":"extra_forbidden"}]`
	if err != nil || string(got) != want {
		t.Fatalf("structured diagnostics must contain only the scoped coordinate and validation type: %s (%v)", got, err)
	}
}
