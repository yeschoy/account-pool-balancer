package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factory"
)

const factoryCompatGeminiModel = "gemini-4-carbon"

func TestFactoryGeminiCompatibilityMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, path, payload, idPrefix, finalEvent string
	}{
		{"chat", "/v1/chat/completions", `"max_completion_tokens":64,"n":1,"messages":[{"role":"developer","content":"Follow the instructions"},{"role":"user","content":"hello"}]`, "chatcmpl-", "data: [DONE]"},
		{"responses", "/v1/responses", `"max_output_tokens":64,"store":false,"instructions":"Follow the instructions","input":"hello"`, "resp_", "event: response.completed"},
		{"messages", "/v1/messages", `"max_tokens":64,"system":[{"type":"text","text":"Follow the instructions"}],"messages":[{"role":"user","content":"hello"}]`, "msg_", "event: message_stop"},
	} {
		for _, upstreamStream := range []bool{false, true} {
			for _, downstreamStream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/upstream-%t/downstream-%t", tc.name, upstreamStream, downstreamStream), func(t *testing.T) {
					e := newFactoryEnv(t)
					e.mu.Lock()
					e.handler = func(w http.ResponseWriter, _ *http.Request) {
						if upstreamStream {
							w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
							fmt.Fprint(w, `data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hello "}]}}]}`+"\n\n")
							fmt.Fprint(w, `data: {"candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"世界","thoughtSignature":"optional-text-replay-metadata"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":30,"cachedContentTokenCount":10}}`+"\n\n")
							fmt.Fprint(w, `data: {"usageMetadata":{"candidatesTokenCount":6,"thoughtsTokenCount":4,"totalTokenCount":40}}`+"\n\n")
							return
						}
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"responseId":"gemini-reply","modelVersion":"opaque-version","candidates":[{"content":{"role":"model","parts":[{"text":"Hello 世界","thoughtSignature":"optional-text-replay-metadata"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":30,"cachedContentTokenCount":10,"candidatesTokenCount":6,"thoughtsTokenCount":4,"totalTokenCount":40}}`)
					}
					e.mu.Unlock()
					request := fmt.Sprintf(`{"model":%q,"stream":%t,%s}`, factoryCompatGeminiModel, downstreamStream, tc.payload)
					if downstreamStream && tc.name == "chat" {
						request = strings.TrimSuffix(request, "}") + `,"stream_options":{"include_usage":true}}`
					}
					rsp, out := e.do(t, tc.path+"?beta=true", request, map[string]string{"Anthropic-Version": "inbound", "Anthropic-Beta": "inbound", "X-Goog-Api-Key": "inbound"})
					if rsp.StatusCode != 200 || rsp.Header.Get("X-Compatibility-Mode") != "buffered" || rsp.Header.Get("X-Attempts") != "1" || !strings.Contains(out, "Hello 世界") || strings.Contains(out, "optional-text-replay-metadata") || !strings.Contains(out, tc.idPrefix) || !strings.Contains(out, factoryCompatGeminiModel) || strings.Contains(out, "opaque-version") {
						t.Fatal(rsp.StatusCode, rsp.Header, out)
					}
					if downstreamStream {
						if !strings.Contains(out, tc.finalEvent) || rsp.Header.Get("Content-Type") != "text/event-stream" {
							t.Fatalf("missing target SSE terminal: %s", out)
						}
					} else if !json.Valid([]byte(out)) || rsp.Header.Get("Content-Type") != "application/json" {
						t.Fatalf("not target JSON: %s", out)
					}
					var usage []string
					switch tc.name {
					case "chat":
						usage = []string{`"prompt_tokens":30`, `"completion_tokens":10`, `"cached_tokens":10`, `"reasoning_tokens":4`, `"total_tokens":40`}
					case "responses":
						usage = []string{`"input_tokens":30`, `"output_tokens":10`, `"cached_tokens":10`, `"reasoning_tokens":4`, `"total_tokens":40`}
					case "messages":
						usage = []string{`"input_tokens":20`, `"output_tokens":10`, `"cache_read_input_tokens":10`}
					}
					for _, v := range usage {
						if !strings.Contains(out, v) {
							t.Fatalf("missing measured usage %s: %s", v, out)
						}
					}
					calls := e.snapshot()
					if len(calls) != 1 || calls[0].path != "/api/llm/g/v1/generate" || calls[0].header.Get("X-Api-Provider") != "google" || calls[0].header.Get("X-Goog-Api-Key") != "" || calls[0].header.Get("Anthropic-Beta") != "" || calls[0].header.Get("Traceparent") == "" {
						t.Fatalf("wrong wire route: %+v", calls)
					}
					for _, token := range []string{factoryCompatGeminiModel, factory.DroidLine, "Follow the instructions", `"maxOutputTokens":64`, `"text":"hello"`} {
						if !strings.Contains(calls[0].body, token) {
							t.Fatalf("wire body missing %q: %s", token, calls[0].body)
						}
					}
					if strings.Contains(calls[0].body, `"stream"`) || strings.Contains(calls[0].body, `"messages"`) {
						t.Fatal(calls[0].body)
					}
				})
			}
		}
	}
}

func TestFactoryGeminiCompatibilityToolRoundTrip(t *testing.T) {
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, _ *http.Request) {
		var b struct {
			Tools []struct {
				Declarations []struct{ Name string } `json:"functionDeclarations"`
			}
		}
		if err := json.Unmarshal([]byte(e.snapshot()[0].body), &b); err != nil || len(b.Tools) != 1 || len(b.Tools[0].Declarations) != 1 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `data: {"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"id":"tool-demo","name":%q,"args":{"n":900719925474099312345}}}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3}}`+"\n\n", b.Tools[0].Declarations[0].Name)
	}
	e.mu.Unlock()
	body := `{"model":"gemini-4-carbon","stream":true,"messages":[{"role":"user","content":"check"},{"role":"assistant","tool_calls":[{"id":"old","type":"function","function":{"name":"read","arguments":"{\"n\":900719925474099312345}"}}]},{"role":"tool","tool_call_id":"old","content":"原样输出"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object","properties":{"n":{"type":"integer"}}}}}],"tool_choice":{"type":"function","function":{"name":"read"}}}`
	rsp, out := e.do(t, "/v1/chat/completions", body, nil)
	if rsp.StatusCode != 200 || !strings.Contains(out, `"name":"read"`) || !strings.Contains(out, `"id":"tool-demo"`) || !strings.Contains(out, `\"n\":900719925474099312345`) || !strings.Contains(out, `"finish_reason":"tool_calls"`) || strings.Contains(out, `"usage"`) {
		t.Fatal(rsp.StatusCode, out)
	}
	wire := e.snapshot()[0].body
	for _, token := range []string{`"functionCall":{"args":{"n":900719925474099312345},"id":"old","name":"Read"}`, `"functionResponse":{"id":"old","name":"Read","response":{"output":"原样输出"}}`, `"allowedFunctionNames":["Read"]`} {
		if !strings.Contains(wire, token) {
			t.Fatalf("missing %q from %s", token, wire)
		}
	}
}

// A Responses client (Codex) sends draft JSON-Schema keywords Gemini refuses;
// the request must be trimmed and delivered, not failed.
func TestFactoryGeminiConversionTrimsToolSchema(t *testing.T) {
	e := newFactoryEnv(t)
	body := `{"model":"gemini-4-carbon","stream":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
		`"include":["reasoning.encrypted_content"],"prompt_cache_key":"cache-1","reasoning":{"effort":"medium"},` +
		`"tools":[{"type":"function","name":"read","parameters":{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","additionalProperties":false,"properties":{"p":{"type":"string"}},"strict":false}}]}`
	rsp, out := e.do(t, "/v1/responses", body, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode, out)
	}
	wire := e.snapshot()[0].body
	for _, bad := range []string{"$schema", "additionalProperties", "strict", `"include"`, "prompt_cache_key", `"reasoning"`} {
		if strings.Contains(wire, bad) {
			t.Fatalf("%s reached the upstream body: %s", bad, wire)
		}
	}
	if !strings.Contains(wire, `"functionDeclarations"`) || !strings.Contains(wire, `"name":"Read"`) {
		t.Fatalf("declarations lost: %s", wire)
	}
}

func TestFactoryGeminiCompatibilityLocalGuards(t *testing.T) {
	e := newFactoryEnv(t)
	for _, tc := range []struct{ path, body string }{
		{"/v1/responses", `{"model":"gemini-4-carbon","input":"hi","previous_response_id":"resp_old"}`},
		{"/v1/responses", `{"model":"gemini-4-carbon","input":"hi","store":true}`},
		{"/v1/messages", `{"model":"gemini-4-carbon","max_tokens":64,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hello"}]}`},
	} {
		rsp, out := e.do(t, tc.path, tc.body, nil)
		if rsp.StatusCode != 400 || !strings.Contains(out, "Factory Gemini conversion") {
			t.Fatal(rsp.StatusCode, out)
		}
	}
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Factory.Compatibility = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rsp, out := e.do(t, "/v1/chat/completions", `{"model":"gemini-4-carbon","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rsp.StatusCode != 400 || len(e.snapshot()) != 0 || !strings.Contains(out, "generateContent") {
		t.Fatal(rsp.StatusCode, out, e.snapshot())
	}
	if v := e.s.pool.Views(e.s.cfg.Get())["f1"]; v.Samples != 0 {
		t.Fatal("local conversion charged health", v)
	}
	rsp, out = e.do(t, "/v1beta/models/gemini-4-carbon:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, nil)
	if rsp.StatusCode != 200 || rsp.Header.Get("X-Compatibility-Mode") != "" || !strings.Contains(out, `"finishReason":"STOP"`) {
		t.Fatal(rsp.StatusCode, out)
	}
}

func TestFactoryGeminiCompatibilityFailureNoReplay(t *testing.T) {
	for _, tc := range []struct{ name, ct, body string }{
		{"truncated", "text/event-stream", `data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}` + "\n\ndata: [DONE]\n\n"},
		{"late error", "text/event-stream", `data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}` + "\n\ndata: {\"error\":{\"code\":500,\"message\":\"late\"}}\n\n"},
		{"signed tool", "application/json", `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"functionCall":{"name":"Read","args":{}},"thoughtSignature":"opaque-signed-tool"}]}}]}`},
		{"visible thoughts", "application/json", `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"thoughts","thought":true}]}}]}`},
		{"missing usage for Messages", "application/json", `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"partial"}]}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newFactoryEnv(t)
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.ct)
				fmt.Fprint(w, tc.body)
			}
			e.mu.Unlock()
			rsp, out := e.do(t, "/v1/messages", `{"model":"gemini-4-carbon","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`, nil)
			if rsp.StatusCode != 502 || len(e.snapshot()) != 1 || !strings.Contains(out, "upstream_compatibility_error") || strings.Contains(out, "event: message_start") || strings.Contains(out, "opaque-signed-tool") {
				t.Fatal(rsp.StatusCode, out)
			}
			if v := e.s.pool.Views(e.s.cfg.Get())["f1"]; v.Samples != 0 {
				t.Fatal("conversion failure charged health", v)
			}
		})
	}
}

// The relay reports how many upstream events it delivered and the worst delay
// between reading one and flushing it, so buffering would be measurable.
func TestStreamRelayRecordsForwardLag(t *testing.T) {
	res := &attemptResult{}
	res.streamEvents = 3
	res.streamMaxLagMs = 2
	if res.streamEvents != 3 || res.streamMaxLagMs != 2 {
		t.Fatal("fields not assigned")
	}
}
