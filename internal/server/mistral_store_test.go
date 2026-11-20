package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMistralDeepSeekStatelessStoreCompatibility(t *testing.T) {
	for _, body := range []string{
		`{"store":false, "model":"deepseek-v4-1-flash-1017","messages":[],"tools":[{"n":9007199254740993}]}`,
		`{"model":"deepseek-v4-1-flash-1017", "store":null, "messages":[{"role":"assistant","reasoning_content":"thought","content":"answer"}],"reasoning_effort":"medium","tools":[{"n":9007199254740993}]}`,
		"{\"model\":\"deepseek-v4-1-flash-1017\",\"messages\":[],\"tools\":[{\"n\":9007199254740993}], \n\t\"store\":false \n}",
	} {
		out, generated, err := prepareMistralRequest([]byte(body), "/v1/chat/completions", "scope")
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(out, &fields) != nil || generated != "scope" {
			t.Fatalf("invalid root removal: %s %v", out, err)
		}
		if _, exists := fields["store"]; exists || !bytes.Contains(fields["tools"], []byte("9007199254740993")) {
			t.Fatal("store remained or neighboring tools changed")
		}
		if _, exists := fields["reasoning_effort"]; exists && string(fields["reasoning_effort"]) != `"high"` {
			t.Fatal("combined effort/content/store changes overlapped")
		}
	}
	e := newMistralEnv(t)
	st, body := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","store":false,"messages":[],"metadata":{"store":true}}`, nil)
	if st != 200 || e.count() != 1 || strings.Contains(e.call(0).body, `"store":false`) || !strings.Contains(e.call(0).body, `"metadata":{"store":true}`) {
		t.Fatalf("native attempt/root-only scope lost: %d %s", st, body)
	}
}

// Channel 182: both hosted GLM models reject even store:false on native Chat.
func TestMistralGLMStatelessStoreActualHTTP(t *testing.T) {
	for _, model := range []string{"zai-glm-5-3", "zai-glm-5-3-flash"} {
		for _, tc := range []struct {
			name, store string
			stream      bool
		}{
			{"false", `"store":false,`, false},
			{"escaped-null", `"st\u006fre":null,`, true},
		} {
			t.Run(model+"/"+tc.name, func(t *testing.T) {
				e := newMistralEnv(t)
				wire := `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":64}}}`
				if tc.stream {
					wire = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":2,\"prompt_tokens_details\":{\"cached_tokens\":64}}}\n\ndata: [DONE]\n\n"
				}
				e.setHandler(func(w http.ResponseWriter, r *http.Request) {
					var fields map[string]json.RawMessage
					w.Header().Set("Content-Type", "application/json")
					if json.Unmarshal([]byte(e.call(0).body), &fields) != nil {
						w.WriteHeader(400)
						return
					}
					if _, present := fields["store"]; present {
						w.WriteHeader(422)
						fmt.Fprint(w, `{"detail":[{"type":"extra_forbidden","loc":["body","store"]}]}`)
						return
					}
					if tc.stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					fmt.Fprint(w, wire)
				})
				want := fmt.Sprintf(`{"model":%q,"stream":%v,"reasoning_effort":"max","prompt_cache_key":"stable-key","metadata":{"store":true},"messages":[{"role":"system","content":"instructions"},{"role":"user","content":"question"}],"tools":[{"type":"function","function":{"name":"run","parameters":{"default":9007199254740993}}}]}`, model, tc.stream)
				body := "{" + tc.store + want[1:]
				status, reply := e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "store-session"})
				if status != 200 || reply != wire || e.count() != 1 {
					t.Fatalf("store rejected or response changed: %d %s (attempts %d)", status, reply, e.count())
				}
				if call := e.call(0); call.body != want || call.header.Get("X-Affinity") != "stable-key" {
					t.Fatal("root-only store adaptation changed request bytes or cache affinity")
				}
				rec := mistralTerminalRequestLog(t, e)
				if rec.Result != "success" || rec.InputTokens == nil || *rec.InputTokens != 100 || rec.CachedTokens == nil || *rec.CachedTokens != 64 {
					t.Fatal("missing or changed upstream cache usage")
				}
			})
		}
	}
}

func TestMistralStoreTrueAndAmbiguousRejectedLocally(t *testing.T) {
	for _, model := range []string{"deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash"} {
		t.Run(model, func(t *testing.T) {
			e := newMistralEnv(t)
			for _, fields := range []string{`"store":true`, `"store":"false"`, `"store":0`, `"store":false,"st\u006fre":null`} {
				st, body := e.post(t, "/v1/chat/completions", fmt.Sprintf(`{"model":%q,%s,"messages":[]}`, model, fields), nil)
				if st != 400 || e.count() != 0 || !strings.Contains(body, "stateless") {
					t.Fatalf("unsupported persistence reached upstream: %d %s", st, body)
				}
			}
		})
	}
}

func TestMistralStoreOtherModelAndProtocolUnchanged(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"other-model", "/v1/chat/completions"},
		{"zai-glm-5-3-other", "/v1/chat/completions"},
		{"deepseek-v4-1-flash-1017", "/v1/responses"},
		{"deepseek-v4-1-flash-1017", "/v1/fim/completions"},
		{"zai-glm-5-3", "/v1/responses"},
		{"zai-glm-5-3-flash", "/v1/responses"},
		{"zai-glm-5-3-flash", "/v1/fim/completions"},
	} {
		body := []byte(`{ "model":"` + tc.model + `", "reasoning":{"effort":"high"}, "store":false, "messages":[] }`)
		out, _, err := prepareMistralRequest(body, tc.path, "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatal("other model/protocol store behavior changed")
		}
	}
}
