package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

// Production 2026-11-18: DeepSeek Chat returned 422 at body.temperature,
// less_than_equal, input=1.62, ctx.le=1.5. Do not clamp a sampling choice or
// infer the same model-specific limit for other models/protocols/providers.
func TestMistralDeepSeekTemperatureRejectsBeforeAdmission(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(422)
		fmt.Fprint(w, `{"detail":[{"loc":["body","temperature"],"type":"less_than_equal","input":1.62,"ctx":{"le":1.5}}]}`)
	})
	for _, stream := range []bool{false, true} {
		for _, fields := range []string{
			`"temperature":1.62`, `"temperature":1.5001`, `"temperature":2`,
			`"temperature":1.62e0`, `"temperature":1e309`,
			`"temperature":"private-temperature"`, `"temperature":true`,
			`"temperature":{}`, `"temperature":[]`,
			`"temperature":1,"temperature":1.62`,
			`"temperature":1.62,"temperat\u0075re":1`,
			`"temperature":1,"temperat\u0075re":1`,
		} {
			t.Run(fmt.Sprintf("stream=%v/%s", stream, fields), func(t *testing.T) {
				status, reply := e.post(t, "/v1/chat/completions", fmt.Sprintf(`{"model":"deepseek-v4-1-flash-1017","stream":%v,%s,"messages":[{"role":"user","content":"private-question"}]}`, stream, fields), nil)
				if status != http.StatusBadRequest || e.count() != 0 || !strings.Contains(reply, `"code":"invalid_request"`) || strings.Contains(strings.ToLower(reply), "mistral") || !strings.Contains(reply, "temperature") {
					t.Fatalf("temperature was not rejected locally: %d %s attempts=%d", status, reply, e.count())
				}
				if strings.Contains(reply, "private-") || strings.Contains(reply, "1.62") {
					t.Fatalf("unsafe validation reply: %s", reply)
				}
				if !strings.Contains(fields, `,"`) && !strings.Contains(reply, "1.5") {
					t.Fatalf("temperature error omitted the actionable upper bound: %s", reply)
				}
			})
		}
	}
	for _, view := range e.s.pool.Views(e.s.cfg.Get()) {
		if view.Samples != 0 || view.Status != "ok" || view.Active != 0 {
			t.Fatal("local parameter rejection affected account health/admission")
		}
	}
	logs, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 100, 0)
	if len(logs.Items) != 24 {
		t.Fatalf("missing local request records: %d", len(logs.Items))
	}
	for _, raw := range logs.Items {
		var rec requestLog
		if json.Unmarshal(raw, &rec) != nil || rec.Status != 400 || rec.Result != "failed" || rec.ErrorCategory != "request" || len(rec.Attempts) != 0 || rec.AccountID != "" {
			t.Fatalf("local rejection recorded as upstream/account failure: %s", raw)
		}
	}
}

func TestMistralDeepSeekTemperaturePreservesValidWireAndContent(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				// newMistralEnv has already captured/consumed the HTTP body.
				raw := []byte(e.call(e.count() - 1).body)
				var sent struct {
					Temperature *float64 `json:"temperature"`
				}
				if json.Unmarshal(raw, &sent) != nil || sent.Temperature != nil && *sent.Temperature > 1.5 {
					w.WriteHeader(422)
					fmt.Fprint(w, `{"detail":[{"loc":["body","temperature"],"type":"less_than_equal","ctx":{"le":1.5}}]}`)
					return
				}
				message := `{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"thought"}]},{"type":"text","text":"answer"}],"tool_calls":[{"id":"call","type":"function","function":{"name":"read","arguments":"{\"n\":9007199254740993}"}}]}`
				w.Header().Set("Content-Type", "application/json")
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":%s,\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n", message)
				} else {
					fmt.Fprintf(w, `{"choices":[{"message":%s,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":17,"completion_tokens":5}}`, message)
				}
			})
			for _, field := range []string{"", `"temperature":null,`, `"temperature":0,`, `"temperature":-0,`, `"temperature":0.30e0,`, `"temperature":1.5,`, `"temperat\u0075re":15e-1,`} {
				body := fmt.Sprintf(` { "model":"deepseek-v4-1-flash-1017", %s "stream":%v, "reasoning_effort":"max", "prompt_cache_key":"synthetic-cache", "tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","n":9007199254740993}}}], "metadata":{"n":9007199254740993}, "messages":[{"role":"system","content":"instruction"},{"role":"user","content":"question"},{"role":"assistant","reasoning_content":"old thought","content":"old answer","tool_calls":[{"id":"old-call","function":{"name":"read","arguments":"{\"n\":9007199254740993}"}}]},{"role":"tool","tool_call_id":"old-call","content":"result"}] } `, field, stream)
				before := e.count()
				status, reply := e.post(t, "/v1/chat/completions", body, nil)
				if status != 200 || e.count() != before+1 || !strings.Contains(reply, `"content":"answer"`) || !strings.Contains(reply, `"reasoning_content":"thought"`) || !strings.Contains(reply, `9007199254740993`) || stream && !strings.Contains(reply, "data: [DONE]") {
					t.Fatalf("valid temperature/content failed or replayed: %s %d %s", field, status, reply)
				}
				var original, sent map[string]json.RawMessage
				_ = json.Unmarshal([]byte(body), &original)
				_ = json.Unmarshal([]byte(e.call(before).body), &sent)
				for _, key := range []string{"model", "temperature", "stream", "reasoning_effort", "prompt_cache_key", "tools", "metadata"} {
					if !bytes.Equal(original[key], sent[key]) {
						t.Fatalf("unrelated wire field changed: %s", key)
					}
				}
				if !strings.Contains(string(sent["messages"]), `"thinking"`) || !strings.Contains(string(sent["messages"]), `old thought`) || !strings.Contains(string(sent["messages"]), `old answer`) || !strings.Contains(string(sent["messages"]), `9007199254740993`) || e.call(before).header.Get("X-Affinity") != "synthetic-cache" {
					t.Fatal("temperature validation changed history/tools/cache affinity")
				}
			}
		})
	}
}

func TestMistralTemperatureLimitScope(t *testing.T) {
	// No inferred lower bound: this fix is grounded in the observed upper
	// bound, not a blanket range/default rewrite. Native validation owns it.
	for _, temperature := range []string{`-0.1`, `1e-1000`, `null`} {
		body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high","temperature":` + temperature + `,"messages":[]}`)
		out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("unobserved constraint changed native input: %s %v", out, err)
		}
	}
	for _, model := range []string{"zai-glm-5-3", "zai-glm-5-3-flash", "mistral-large-4-0", "deepseek-v4-1-flash-1017-latest", "m"} {
		body := []byte(fmt.Sprintf(`{"model":%q,"reasoning_effort":"high","temperature":1.62,"messages":[]}`, model))
		out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("other model temperature changed: %s %v", out, err)
		}
	}
	for _, path := range []string{"/v1/responses", "/v1/responses/input_tokens", "/v1/fim/completions", "/v1/embeddings"} {
		body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning":{"effort":"high"},"temperature":1.62,"input":"question"}`)
		out, _, err := prepareMistralRequest(body, path, "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("other protocol temperature changed: %s %v", out, err)
		}
	}
	e := newEnv(t, []config.Account{{ID: "native-other"}}, nil)
	e.up.mu.Lock()
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		if body["temperature"] != float64(1.62) {
			t.Error("other provider temperature changed")
		}
		writeOK(w, key)
	}
	e.up.mu.Unlock()
	response := e.post("/v1/chat/completions", map[string]any{"model": "deepseek-v4-1-flash-1017", "temperature": 1.62, "messages": []any{map[string]any{"role": "user", "content": "q"}}}, nil)
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != 200 || e.up.callCount() != 1 {
		t.Fatal("Mistral temperature guard leaked into another provider")
	}
}
