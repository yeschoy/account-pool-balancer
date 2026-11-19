package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/factory"
)

func TestFactoryCrossProtocolMatrix(t *testing.T) {
	requests := map[string]string{
		"chat":      `{"model":%q,"max_tokens":64,"messages":[{"role":"system","content":"Original instructions"},{"role":"user","content":"hi"}]%s}`,
		"responses": `{"model":%q,"max_output_tokens":64,"instructions":"Original instructions","input":"hi"%s}`,
		"messages":  `{"model":%q,"max_tokens":64,"system":"Original instructions","messages":[{"role":"user","content":"hi"}]%s}`,
	}
	paths := map[string]string{"chat": "/v1/chat/completions", "responses": "/v1/responses", "messages": "/v1/messages"}
	models := map[string]string{"chat": "deepseek-v4.1-flash", "responses": "gpt-5.4", "messages": "claude-opus-5-5"}
	for from, template := range requests {
		for to, model := range models {
			if from == to {
				continue
			}
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s-%s-stream-%t", from, to, stream), func(t *testing.T) {
					e := newFactoryEnv(t)
					e.mu.Lock()
					e.handler = func(w http.ResponseWriter, r *http.Request) {
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							switch to {
							case "chat":
								fmt.Fprint(w, "data: {\"id\":\"chat_1\",\"model\":\"deepseek-v4.1-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
							case "responses":
								fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.4\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"OK\"}]}],\"usage\":{\"input_tokens\":7,\"output_tokens\":1}}}\n\n")
							case "messages":
								fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-5-5\",\"usage\":{\"input_tokens\":7}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"OK\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
							}
							return
						}
						w.Header().Set("Content-Type", "application/json")
						switch to {
						case "chat":
							fmt.Fprint(w, `{"id":"chat_1","model":"deepseek-v4.1-flash","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":1}}`)
						case "responses":
							fmt.Fprint(w, `{"id":"resp_1","model":"gpt-5.4","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":7,"output_tokens":1}}`)
						case "messages":
							fmt.Fprint(w, `{"id":"msg_1","model":"claude-opus-5-5","type":"message","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":1}}`)
						}
					}
					e.mu.Unlock()
					extra := ""
					if stream {
						extra = `,"stream":true`
					}
					rsp, out := e.do(t, paths[from], fmt.Sprintf(template, model, extra), nil)
					if rsp.StatusCode != 200 || !strings.Contains(out, "OK") || rsp.Header.Get("X-Compatibility-Mode") != "buffered" {
						t.Fatal(rsp.StatusCode, out)
					}
					calls := e.snapshot()
					if len(calls) != 1 || !strings.HasSuffix(calls[0].path, paths[to][3:]) || !strings.Contains(calls[0].body, "Original instructions") {
						t.Fatalf("wrong route %+v", calls)
					}
					// Droid's client identity, not the caller's; and the body opens
					// with droid's line while the task instructions survive.
					if h := calls[0].header; h.Get("User-Agent") != "factory-cli/"+factory.DroidVersion || h.Get("X-Factory-Client") != "cli" || h.Get("X-Client-Version") != factory.DroidVersion || h.Get("X-Provider-Routing-Source") != factory.RoutingSourceConfiguredOrder || h.Get("X-Factory-Request-Surface") != "cli_tui" || h.Get("X-Session-Id") == "" || h.Get("X-Assistant-Message-Id") == "" || len(h.Get("Traceparent")) != 55 {
						t.Fatalf("droid client identity missing: %v", h)
					}
					if !strings.Contains(calls[0].body, factory.DroidLine) || strings.Count(calls[0].body, factory.DroidLine) != 1 {
						t.Fatalf("system prompt not opened as droid's (calls=%d path=%s): %s", len(calls), calls[0].path, calls[0].body)
					}
				})
			}
		}
	}
}
func TestFactoryToolSchemaHTTPRoundTrip(t *testing.T) {
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", "req_factory_tools")
		fmt.Fprint(w, `{"type":"message","id":"msg_1","model":"claude-opus-5-5","content":[{"type":"tool_use","name":"task","id":"new_call","input":{"arguments":{"n":900719925474099312345}}}],"stop_reason":"tool_use","usage":{"input_tokens":7,"output_tokens":3}}`)
	}
	e.mu.Unlock()
	body := `{"model":"claude-opus-5-5","max_tokens":64,"system":"Original system","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"old_call","name":"task","input":{"n":900719925474099312345}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"old_call","content":"original result"}]}],"tools":[{"name":"task","input_schema":{"anyOf":[{"type":"object"}]}}]}`
	rsp, out := e.do(t, "/v1/messages", body, nil)
	if rsp.StatusCode != 200 || !strings.Contains(out, `"input":{"n":900719925474099312345}`) {
		t.Fatal(rsp.StatusCode, out)
	}
	c := e.snapshot()[0]
	if !strings.Contains(c.body, `"arguments":{"n":900719925474099312345}`) || !strings.Contains(c.body, `"required":["arguments"]`) {
		t.Fatal(c.body)
	}
	// The same schema when reached via Chat must also unwrap before response conversion.
	chat := `{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"task","parameters":{"oneOf":[{"type":"object"}]}}}]}`
	rsp, out = e.do(t, "/v1/chat/completions", chat, nil)
	if rsp.StatusCode != 200 || !strings.Contains(out, `\"n\":900719925474099312345`) || strings.Contains(out, `\"arguments\":`) {
		t.Fatal(rsp.StatusCode, out)
	}
}
func TestFactoryFastModeAndCompatibilityPersistence(t *testing.T) {
	e := newFactoryEnv(t)
	for _, v := range []struct{ path, body, model, field, beta string }{
		{"/v1/messages", `{"model":"claude-opus-5-5","speed":"fast","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, "claude-opus-5-5-fast", `"speed":"fast"`, factoryFastBeta},
		{"/v1/messages", `{"model":"claude-opus-5-fast","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, "claude-opus-5-fast", `"speed":"fast"`, factoryFastBeta},
		{"/v1/responses", `{"model":"gpt-5.4","service_tier":"priority","input":"hi"}`, "gpt-5.4-fast", `"service_tier":"priority"`, ""},
	} {
		rsp, out := e.do(t, v.path, v.body, map[string]string{"Anthropic-Beta": "client-beta"})
		if rsp.StatusCode != 200 {
			t.Fatal(rsp.StatusCode, out)
		}
		calls := e.snapshot()
		c := calls[len(calls)-1]
		if !strings.Contains(c.body, `"model":"`+v.model+`"`) || !strings.Contains(c.body, v.field) {
			t.Fatal(c.body)
		}
		if v.beta != "" && c.header.Get("Anthropic-Beta") != "client-beta,"+v.beta {
			t.Fatal(c.header)
		}
	}
	w := deployAdmin(t, e, "GET", "/api/settings?provider=factory", nil)
	var doc struct {
		Settings settingsDoc `json:"settings"`
	}
	if json.Unmarshal(w.Body.Bytes(), &doc) != nil || !doc.Settings.Factory.Compatibility {
		t.Fatal("missing conversion setting")
	}
	doc.Settings.Factory.Compatibility = false
	w = deployAdmin(t, e, "PUT", "/api/settings?provider=factory", doc.Settings)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if err := e.s.Reload(); err != nil {
		t.Fatal(err)
	}
	before := len(e.snapshot())
	rsp, _ := e.do(t, "/v1/chat/completions", `{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rsp.StatusCode != 400 || len(e.snapshot()) != before {
		t.Fatal("disabled conversion sent")
	}
	if e.s.cfg.Get().Factory.Compatibility || e.s.cfg.Get().OpenCode.Compatibility {
		t.Fatal("cross-provider config changed or disabled setting was lost")
	}
}
func TestFactoryConversionRejectsLossWithoutUpstream(t *testing.T) {
	e := newFactoryEnv(t)
	for _, body := range []string{
		`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_object"}}`,
		`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"developer","content":"high precedence"},{"role":"user","content":"hi"}]}`,
		`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"task","parameters":{"anyOf":[{"$recursiveRef":"#"}]}}}]}`,
	} {
		rsp, out := e.do(t, "/v1/chat/completions", body, nil)
		if rsp.StatusCode != 400 {
			t.Fatal(rsp.StatusCode, out)
		}
	}
	if len(e.snapshot()) != 0 {
		t.Fatal("invalid conversion reached upstream")
	}
	v := e.s.pool.Views(e.s.cfg.Get())["f1"]
	if v.Samples != 0 {
		t.Fatal("local conversion charged health", v)
	}
}
func TestFactoryToolStreamFailureNoReplay(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprint(malformed), func(t *testing.T) {
			e := newFactoryEnv(t)
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"original_id\",\"name\":\"task\",\"input\":{}}}\n\n")
				if !malformed {
					fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"arguments\\\":{\\\"x\\\":1}}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				}
			}
			e.mu.Unlock()
			rsp, out := e.do(t, "/v1/messages", `{"model":"claude-opus-5-5","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"task","input_schema":{"anyOf":[{"type":"object"}]}}]}`, nil)
			if rsp.StatusCode != 200 || len(e.snapshot()) != 1 {
				t.Fatal(rsp.StatusCode, out)
			}
			if malformed {
				if !strings.Contains(out, "factory_tool_adaptation") || strings.Contains(out, "message_stop") {
					t.Fatal("truncation hidden", out)
				}
			} else if !strings.Contains(out, `\"x\":1`) || strings.Contains(out, `\"arguments\":`) {
				t.Fatal(out)
			}
		})
	}
}

func TestFactoryConvertedResponseErrorsNeverReplay(t *testing.T) {
	for _, body := range []string{
		`{"id":"msg_1","type":"message","content":[{"type":"tool_use","id":"new","name":"task","input":{"wrong":1}}],"stop_reason":"tool_use","usage":{"input_tokens":7,"output_tokens":3}}`,
		`{"id":"msg_1","type":"message","content":[{"type":"thinking","thinking":"signed reasoning","signature":"signed"}],"stop_reason":"end_turn","usage":{"input_tokens":7,"output_tokens":3}}`,
	} {
		t.Run(body, func(t *testing.T) {
			e := newFactoryEnv(t)
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}
			e.mu.Unlock()
			rsp, out := e.do(t, "/v1/chat/completions", `{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"task","parameters":{"anyOf":[{"type":"object"}]}}}]}`, nil)
			if rsp.StatusCode != 502 || len(e.snapshot()) != 1 {
				t.Fatal(rsp.StatusCode, out)
			}
			if v := e.s.pool.Views(e.s.cfg.Get())["f1"]; v.Samples != 0 {
				t.Fatal("unrepresentable output blamed account", v)
			}
		})
	}
}

// A native body keeps its unknown fields, its model metadata and the caller's
// task instructions exactly; the only body change is Claude Code's fixed first
// system line, which Factory refuses for any client but droid. That line is
// consumed, not preserved: the plugin's Claude Code section documents the same
// replacement, and the instructions after it stay byte-identical.
func TestFactoryNativeUnknownFieldsAndPromptsStayUntouched(t *testing.T) {
	e := newFactoryEnv(t)
	body := `{ "model":"claude-opus-5-5", "max_tokens":64, "system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.0; cc_entrypoint=cli;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},{"type":"text","text":"The exact user instructions"}], "messages":[{"role":"user","content":"The exact user instructions"}], "thinking":{"type":"adaptive"},"metadata":{"id":"caller-unchanged"} }`
	rsp, out := e.do(t, "/v1/messages", body, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode, out)
	}
	sent := e.snapshot()[0].body
	var doc struct {
		System []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
		Metadata    map[string]any `json:"metadata"`
		Thinking    map[string]any `json:"thinking"`
		MaxTokens   json.Number    `json:"max_tokens"`
		Messages    []any          `json:"messages"`
		Temperature any            `json:"temperature"`
	}
	if err := json.Unmarshal([]byte(sent), &doc); err != nil {
		t.Fatal(err, sent)
	}
	if len(doc.System) != 3 || doc.System[0].Text != factory.DroidLine || doc.System[2].Text != "The exact user instructions" {
		t.Fatalf("system blocks not opened as droid's: %s", sent)
	}
	for _, b := range doc.System {
		if strings.HasPrefix(b.Text, "x-anthropic-billing-header:") {
			t.Fatal("billing header forwarded", sent)
		}
	}
	if doc.Metadata["id"] != "caller-unchanged" || doc.Thinking["type"] != "adaptive" || doc.MaxTokens.String() != "64" || len(doc.Messages) != 1 || doc.Temperature != nil {
		t.Fatalf("caller fields rewritten: %s", sent)
	}
	if strings.Count(sent, "The exact user instructions") != 2 {
		t.Fatal("instruction text duplicated or lost", sent)
	}
}

// A body that is already droid's is sent byte for byte: no second identity
// line, and no re-ordering of a caller's JSON.
func TestFactoryAlreadyOpenedBodyIsByteIdentical(t *testing.T) {
	e := newFactoryEnv(t)
	body := `{"model":"gpt-5.4","instructions":"` + factory.DroidLine + `\nOriginal","input":"hi"}`
	rsp, _ := e.do(t, "/v1/responses", body, nil)
	if rsp.StatusCode != 200 || e.snapshot()[0].body != body {
		t.Fatal("already-opened body was rewritten", e.snapshot()[0].body)
	}
	chat := `{"model":"deepseek-v4.1-flash","messages":[{"role":"system","content":"` + factory.DroidLine + `"},{"role":"user","content":"hi"}]}`
	rsp, _ = e.do(t, "/v1/chat/completions", chat, nil)
	if rsp.StatusCode != 200 || e.snapshot()[1].body != chat {
		t.Fatal("already-opened chat body was rewritten", e.snapshot()[1].body)
	}
}

// droid's session and message ids are opaque, UUID-shaped and derived from the
// conversation, so one conversation reuses one session across turns while two
// conversations never collude.
func TestFactorySessionIdentityIsPerConversation(t *testing.T) {
	// The gateway's conversation fingerprint opens with the first user turn
	// (stableConversation), so two conversations differ from their first turn;
	// later turns of one conversation keep the session and renew the message id.
	e := newFactoryEnv(t)
	call := func(first, last string) (session, message string) {
		body := fmt.Sprintf(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":%q}]}`, first)
		if last != first {
			body = fmt.Sprintf(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":%q},{"role":"assistant","content":"ok"},{"role":"user","content":%q}]}`, first, last)
		}
		headers := map[string]string{"X-Client-Session-Hint": "downstream-controlled"}
		if rsp, out := e.do(t, "/v1/chat/completions", body, headers); rsp.StatusCode != 200 {
			t.Fatal(rsp.StatusCode, out)
		}
		c := e.snapshot()
		return c[len(c)-1].header.Get("X-Session-Id"), c[len(c)-1].header.Get("X-Assistant-Message-Id")
	}
	uuid := func(v string) bool {
		return len(v) == 36 && v[8] == '-' && v[13] == '-' && v[18] == '-' && v[23] == '-' && v[14] == '4'
	}
	a1, m1 := call("first turn", "first turn")
	a2, m2 := call("first turn", "second turn")
	b1, _ := call("another conversation", "another conversation")
	if !uuid(a1) || !uuid(m1) || strings.Contains(a1, "downstream") {
		t.Fatal("ids are not droid-shaped UUIDs", a1, m1)
	}
	if a1 != a2 {
		t.Fatal("one conversation changed session", a1, a2)
	}
	if m1 == m2 {
		t.Fatal("distinct messages shared a message id")
	}
	if a1 == b1 {
		t.Fatal("separate conversations shared a session")
	}
}
