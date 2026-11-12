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

// Production GLM422 rejected messages[0] with role=developer. Only the role
// value may change; GLM must not acquire DeepSeek's unrelated parameter cleanup.
func TestMistralGLMDeveloperRoleWirePreservation(t *testing.T) {
	messages := `[ { "\u0072ole" : "devel\u006fper", "content" : [{"type":"text","text":"developer remains in text","annotations":[]}], "n":9007199254740993,"tag":1,"tag":2 }, {"role":"system","content":"native system"}, {"role":"user","content":[{"type":"text","text":"question","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png","MimeType":""}}]}, {"role":"assistant","content":"answer","tool_calls":[{"id":"call","type":"function","function":{"name":"run","arguments":"{\"role\":\"developer\",\"n\":9007199254740993}"}}]}, {"role":"tool","tool_call_id":"call","content":"whole result"}, {"role":"developer","content":"late instructions"} ]`
	wantMessages := strings.Replace(messages, `"devel\u006fper"`, `"system"`, 1)
	wantMessages = strings.Replace(wantMessages, `{"role":"developer","content":"late instructions"}`, `{"role":"system","content":"late instructions"}`, 1)
	for _, model := range []string{"zai-glm-5-3", "zai-glm-5-3-flash"} {
		for _, cache := range []string{`"caller-cache"`, `null`, `""`} {
			t.Run(model+"/cache="+cache, func(t *testing.T) {
				body := []byte(fmt.Sprintf(` { "model":%q, "reasoning_effort":"high", "store":false,"seed":9007199254740993,"verbosity":"high","logit_bias":{"1":1},"user":"native-label","prompt_cache_retention":"24h","prompt_cache_key":%s,"metadata":{"session_id":"role-session","n":9007199254740993},"tools":[{"type":"function","function":{"name":"run","parameters":{"role":"developer","n":9007199254740993}}}], "messages":%s } `, model, cache, messages))
				original := append([]byte(nil), body...)
				out, generated, err := prepareMistralRequest(body, "/v1/chat/completions", "must-not-replace-cache")
				want := bytes.Replace(body, []byte(messages), []byte(wantMessages), 1)
				if err != nil || generated != "" || !bytes.Equal(out, want) || !bytes.Equal(body, original) {
					t.Fatalf("GLM must change only the exact role values and retain input: %s\n%v", out, err)
				}
			})
		}
	}
}

func TestMistralGLMDeveloperRoleActualHTTP(t *testing.T) {
	for _, model := range []string{"zai-glm-5-3", "zai-glm-5-3-flash"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", model, stream), func(t *testing.T) {
				e := newMistralEnv(t)
				wire := `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":64}}}`
				if stream {
					wire = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":64}}}\n\ndata: [DONE]\n\n"
				}
				e.setHandler(func(w http.ResponseWriter, r *http.Request) {
					var sent struct {
						Messages []struct{ Role string } `json:"messages"`
					}
					_ = json.Unmarshal([]byte(e.call(e.count()-1).body), &sent)
					for i, message := range sent.Messages {
						if message.Role == "developer" {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(422)
							fmt.Fprintf(w, `{"detail":[{"type":"union_tag_invalid","loc":["body","messages",%d],"msg":"Input tag 'developer' found using 'role' does not match expected roles"}]}`, i)
							return
						}
					}
					w.Header().Set("Content-Type", "application/json")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					fmt.Fprint(w, wire)
				})
				body := fmt.Sprintf(` {"model":%q,"stream":%v,"reasoning_effort":"max","prompt_cache_key":"role-cache","metadata":{"session_id":"role-session","n":9007199254740993},"tools":[{"type":"function","function":{"name":"run","parameters":{"n":9007199254740993}}}],"messages":[{"role":"developer","content":"first instructions"},{"role":"user","content":"question"},{"role":"assistant","reasoning_content":"whole thought","content":"whole answer","tool_calls":[{"id":"call","type":"function","function":{"name":"run","arguments":"{\"n\":9007199254740993}"}}]},{"role":"tool","tool_call_id":"call","content":"whole result"},{"role":"developer","content":"late instructions"}]} `, model, stream)
				native := strings.ReplaceAll(body, `"role":"developer"`, `"role":"system"`)
				want, _, err := prepareMistralRequest([]byte(native), "/v1/chat/completions", "")
				if err != nil {
					t.Fatal(err)
				}
				status, reply := e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "role-session"})
				if status != 200 || reply != wire || e.count() != 1 {
					t.Fatalf("GLM role rejected/replayed or native reply changed: %d %s; calls=%d", status, reply, e.count())
				}
				if call := e.call(0); call.body != string(want) || call.header.Get("X-Affinity") != "role-cache" {
					t.Fatal("final upload changed history, thoughts, tools, large numbers or cache affinity")
				}
				rec := mistralTerminalRequestLog(t, e)
				if rec.Result != "success" || len(rec.Attempts) != 1 || rec.InputTokens == nil || *rec.InputTokens != 100 || rec.CachedTokens == nil || *rec.CachedTokens != 64 {
					t.Fatal("role compatibility changed actual usage or one-attempt success")
				}
			})
		}
	}
}

func TestMistralGLMDeveloperRoleDuplicatesAreLocal(t *testing.T) {
	for _, model := range []string{"zai-glm-5-3", "zai-glm-5-3-flash"} {
		t.Run(model, func(t *testing.T) {
			e := newMistralEnv(t)
			for _, message := range []string{
				`{"role":"developer","role":"user","content":"private-role-content"}`,
				`{"role":"developer","\u0072ole":"developer","content":"private-role-content"}`,
				`{"role":"user","role":"developer","content":"private-role-content"}`,
			} {
				status, reply := e.post(t, "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[%s]}`, model, message), nil)
				if status != 400 || e.count() != 0 || !strings.Contains(reply, "duplicate role") || strings.Contains(reply, "private-role-content") {
					t.Fatalf("duplicate role was collapsed/forwarded or leaked input: %d %s", status, reply)
				}
			}
		})
	}
}

func TestMistralGLMDeveloperRoleScope(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"mistral-large-4-0", "/v1/chat/completions"},
		{"zai-glm-5-3-other", "/v1/chat/completions"},
		{"zai-glm-5-3", "/v1/responses"},
		{"zai-glm-5-3-flash", "/v1/responses"},
		{"zai-glm-5-3", "/v1/fim/completions"},
		{"zai-glm-5-3-flash", "/v1/responses/input_tokens"},
	} {
		body := []byte(fmt.Sprintf(` {"model":%q,"reasoning":{"effort":"high"},"messages":[{"role":"developer","content":"keep"}],"input":[{"role":"developer","content":"keep"}]} `, tc.model))
		out, _, err := prepareMistralRequest(body, tc.path, "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("role adaptation escaped exact model/Chat scope: %s %s %s %v", tc.model, tc.path, out, err)
		}
	}
	for _, model := range []string{"zai-glm-5-3", "zai-glm-5-3-flash"} {
		t.Run(model+"/other-provider", func(t *testing.T) {
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
			body := fmt.Sprintf(` {"model":%q,"messages":[{"role":"developer","content":"keep"}]} `, model)
			status, reply := e.post(t, "/v1/chat/completions", body, map[string]string{"Authorization": "Bearer other-client"})
			if status != 200 || e.count() != 1 || e.call(0).body != body {
				t.Fatalf("role adaptation escaped Mistral pool: %d %s", status, reply)
			}
		})
	}
}

func TestMistralGLMUnknownRoleIsNotGuessedOrRetried(t *testing.T) {
	for _, model := range []string{"zai-glm-5-3", "zai-glm-5-3-flash"} {
		t.Run(model, func(t *testing.T) {
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(422)
				fmt.Fprint(w, `{"detail":[{"type":"union_tag_invalid","loc":["body","messages",0]}]}`)
			})
			body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"function","content":"keep unknown role"}]}`, model)
			status, _ := e.post(t, "/v1/chat/completions", body, nil)
			if status != 422 || e.count() != 1 || !strings.Contains(e.call(0).body, `"role":"function"`) {
				t.Fatal("unknown role was guessed, dropped or replayed")
			}
		})
	}
}
