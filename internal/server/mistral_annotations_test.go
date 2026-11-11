package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMistralDeepSeekEmptyAnnotationsHistory(t *testing.T) {
	for _, annotations := range []string{`[]`, `[ \n\t ]`, `null`} {
		annotations = strings.ReplaceAll(strings.ReplaceAll(annotations, `\n`, "\n"), `\t`, "\t")
		t.Run(annotations, func(t *testing.T) {
			native := `{ "type":"thinking", "thinking":[{"type":"text","text":"original thought"}], "closed":true }`
			answer := `{ "type":"text", "annotations":` + annotations + `, "text":"original answer\n" }`
			body := []byte(`{ "model":"deepseek-v4-1-flash-1017", "reasoning_effort":"high", "messages":[` +
				`{"role":"system","content":"original system"},` +
				`{"role":"user","content":"original question"},` +
				`{"role":"assistant","content":"original early answer"},` +
				`{"role":"user","content":"original later question"},` +
				`{"role":"assistant","reasoning":"original thought","content":[` + native + `,` + answer + `],"tool_calls":[{"id":"original-tool","function":{"arguments":"{\"n\":9007199254740993}"}}]},` +
				`{"role":"assistant","reasoning_content":"second thought","content":[{"type":"text","text":"second answer","annotations":` + annotations + `}]},` +
				`{"role":"user","content":[{"type":"image_url","image_url":"https://example.invalid/original.png"},{"type":"text","text":"next question"}]}],` +
				`"tools":[ {"z":9007199254740993,"a":1} ],"prompt_cache_key":"original-cache","metadata":{"session_id":"original-session"} }`)
			out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "unused")
			if err != nil || bytes.Contains(out, []byte(`"annotations"`)) {
				t.Fatalf("empty annotations not consumed: %v; %s", err, out)
			}
			var originalRoot, root map[string]json.RawMessage
			if json.Unmarshal(body, &originalRoot) != nil || json.Unmarshal(out, &root) != nil {
				t.Fatal("invalid test input or prepared JSON")
			}
			for _, key := range []string{"model", "tools", "prompt_cache_key", "metadata"} {
				if !bytes.Equal(originalRoot[key], root[key]) {
					t.Fatalf("unrelated field changed: %s", key)
				}
			}
			var oldMessages, messages []json.RawMessage
			_ = json.Unmarshal(originalRoot["messages"], &oldMessages)
			_ = json.Unmarshal(root["messages"], &messages)
			if len(messages) != len(oldMessages) {
				t.Fatal("history lost")
			}
			for _, i := range []int{0, 1, 2, 3, 6} {
				if !bytes.Equal(oldMessages[i], messages[i]) {
					t.Fatalf("unrelated history changed: %d", i)
				}
			}
			var oldAssistant, assistant map[string]json.RawMessage
			_ = json.Unmarshal(oldMessages[4], &oldAssistant)
			_ = json.Unmarshal(messages[4], &assistant)
			if !bytes.Equal(oldAssistant["tool_calls"], assistant["tool_calls"]) || !bytes.Contains(assistant["content"], []byte(native)) {
				t.Fatal("native thought bytes/closed metadata or tools changed")
			}
			if bytes.Count(assistant["content"], []byte(`original thought`)) != 1 || !bytes.Contains(assistant["content"], []byte(`original answer\n`)) || !bytes.Contains(messages[5], []byte(`second thought`)) || !bytes.Contains(messages[5], []byte(`second answer`)) {
				t.Fatal("thought or answer lost/duplicated")
			}
			// Exercise the final HTTP upload, not only the preparation helper.
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				call := e.call(e.count() - 1)
				if strings.Contains(call.body, `"annotations"`) {
					w.WriteHeader(422)
					fmt.Fprint(w, `{"detail":[{"type":"extra_forbidden","loc":["body","messages",4,"assistant","content",1,"text","annotations"]}]}`)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
			})
			status, reply := e.post(t, "/v1/chat/completions", string(body), nil)
			if status != 200 || e.count() != 1 || e.call(0).body != string(out) {
				t.Fatalf("mock upload failed or replayed: %d %s attempts=%d", status, reply, e.count())
			}
		})
	}
}

func TestMistralDeepSeekAnnotationsScopeAndMeaningfulValues(t *testing.T) {
	for _, value := range []string{`[{"type":"url_citation","url":"https://example.invalid/citation","title":"original citation"}]`, `"unknown"`, `{}`, `false`} {
		body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high","messages":[{"role":"assistant","content":[{"type":"text","text":"answer","annotations":` + value + `}]}]}`)
		out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		if err != nil || !bytes.Equal(body, out) {
			t.Fatal("real/unknown annotations silently changed")
		}
	}
	for _, tc := range []struct{ model, path, role, chunk string }{
		{"zai-glm-5-3", "/v1/chat/completions", "assistant", `{"type":"text","text":"answer","annotations":[]}`},
		{"zai-glm-5-3-flash", "/v1/chat/completions", "assistant", `{"type":"text","text":"answer","annotations":[]}`},
		{"other-model", "/v1/chat/completions", "assistant", `{"type":"text","text":"answer","annotations":[]}`},
		{"deepseek-v4-1-flash-1017", "/v1/responses", "assistant", `{"type":"text","text":"answer","annotations":[]}`},
		{"deepseek-v4-1-flash-1017", "/v1/fim/completions", "assistant", `{"type":"text","text":"answer","annotations":[]}`},
		{"deepseek-v4-1-flash-1017", "/v1/chat/completions", "user", `{"type":"text","text":"question","annotations":[]}`},
		{"deepseek-v4-1-flash-1017", "/v1/chat/completions", "system", `{"type":"text","text":"system","annotations":[]}`},
		{"deepseek-v4-1-flash-1017", "/v1/chat/completions", "assistant", `{"type":"thinking","thinking":[{"type":"text","text":"thought","annotations":[]}]}`},
	} {
		control := `"reasoning_effort":"high"`
		if tc.path == "/v1/responses" {
			control = `"reasoning":{"effort":"high"}`
		}
		body := []byte(fmt.Sprintf(`{"model":%q,%s,"messages":[{"role":%q,"content":[%s]}]}`, tc.model, control, tc.role, tc.chunk))
		out, _, err := prepareMistralRequest(body, tc.path, "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("annotation adapter escaped exact scope: %+v %v", tc, err)
		}
	}
	// Empty metadata and the known type-only text slot compose without losing a block.
	body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high","messages":[{"role":"assistant","content":[{"type":"text","cache_control":{"type":"ephemeral"},"annotations":[]}]}]}`)
	out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
	if err != nil || !bytes.Contains(out, []byte(`{"type":"text","text":""}`)) {
		t.Fatalf("empty metadata prevented known text-slot compatibility: %s %v", out, err)
	}
	for _, fields := range []string{`"annotations":[],"annotations":null`, `"annotations":[],"\u0061nnotations":[]`} {
		e := newMistralEnv(t)
		body := `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"assistant","content":[{"type":"text","text":"answer",` + fields + `}]}]}`
		status, _ := e.post(t, "/v1/chat/completions", body, nil)
		if status != 400 || e.count() != 0 {
			t.Fatalf("ambiguous annotation fields were sent: %d attempts=%d", status, e.count())
		}
	}
	body = []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high","messages":[{"role":"assistant","content":[{"type":"text","text":"answer","\u0061nnotations":[]}]}]}`)
	out, _, err = prepareMistralRequest(body, "/v1/chat/completions", "")
	if err != nil || bytes.Contains(out, []byte(`nnotations`)) {
		t.Fatal("escaped empty annotation was not consumed")
	}
}

func TestMistralAnnotationValidationLocationIsPrivate(t *testing.T) {
	raw := []byte(`{"detail":[{"type":"extra_forbidden","loc":["body","messages",5,"assistant","content",1,"text","annotations"],"input":[{"private":"original-private-citation"}],"ctx":{"loc":["private-session"]}}]}`)
	b, _ := json.Marshal(mistralValidationDetails(raw))
	if string(b) != `[{"loc":["body","messages",5,"assistant","content",1,"text","annotations"],"type":"extra_forbidden"}]` {
		t.Fatalf("fixed schema name lost or private annotations leaked: %s", b)
	}
}
