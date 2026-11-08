package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestMistralChatResponseContent(t *testing.T) {
	for _, tc := range []struct {
		name, content, answer, reasoning string
	}{
		{"thinking", `[{"type":"thinking","thinking":[{"type":"text","text":"考"},{"type":"text","text":"虑"}],"closed":false,"signature":null}]`, "", "考虑"},
		{"transition", `[{"type":"thinking","thinking":[{"type":"text","text":"完成"}],"closed":true},{"type":"text","text":"答案"}]`, "答案", "完成"},
		{"texts", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "ab", ""},
		{"empty", `[]`, "", ""},
		{"closing-marker", `[{"type":"thinking","thinking":[],"closed":true},{"type":"text","text":"ok"}]`, "ok", ""},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				field := "message"
				if stream {
					field = "delta"
				}
				tools := `[{"index":0,"id":"call-id","type":"function","function":{"name":"read","arguments":"{\"id\":9007199254740993}"}}]`
				body := fmt.Sprintf(`{"id":"reply","opaque":9007199254740993,"usage":{"prompt_tokens":17,"completion_tokens":5},"choices":[{"index":0,"finish_reason":"tool_calls","%s":{"role":"assistant","content":%s,"tool_calls":%s,"other":{"n":9007199254740993}}}]}`, field, tc.content, tools)
				out, err := mistralChatResponse([]byte(body), stream)
				if err != nil {
					t.Fatal(err)
				}
				// Match New API's string-valued Chat delta/message contract.
				var response struct {
					Choices []struct {
						Delta, Message struct {
							Content   string
							Reasoning string `json:"reasoning_content"`
						}
					}
				}
				if err := json.Unmarshal(out, &response); err != nil {
					t.Fatal(err)
				}
				message := response.Choices[0].Message
				if stream {
					message = response.Choices[0].Delta
				}
				if message.Content != tc.answer || message.Reasoning != tc.reasoning {
					t.Fatalf("lost answer/reasoning: %s", out)
				}
				if !bytes.Contains(out, []byte(`"tool_calls":`+tools)) || !bytes.Contains(out, []byte(`"opaque":9007199254740993`)) || !bytes.Contains(out, []byte(`"n":9007199254740993`)) || !bytes.Contains(out, []byte(`"finish_reason":"tool_calls"`)) {
					t.Fatalf("unrelated fields changed: %s", out)
				}
			})
		}
	}
}

func TestMistralChatResponseUnchangedAndUnsupported(t *testing.T) {
	merged, err := mistralChatResponse([]byte(`{"choices":[{"delta":{"reasoning_content":"prior-","content":[{"type":"thinking","thinking":[{"type":"text","text":"next"}]}]}},{"delta":{"content":[{"type":"text","text":"second-choice"}]}}]}`), true)
	if err != nil || !bytes.Contains(merged, []byte(`"reasoning_content":"prior-next"`)) || !bytes.Contains(merged, []byte(`"content":"second-choice"`)) {
		t.Fatalf("existing reasoning or another choice lost: %s %v", merged, err)
	}
	for _, body := range []string{
		`{ "choices":[{"delta":{"content":"native","reasoning_content":"existing"}}] }`,
		`{"choices":[{"delta":{"content":null,"tool_calls":[{"id":"tool"}]}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":123}}`,
		`{"error":{"message":"native error"}}`, "[DONE]", ": PING", `not-json`,
	} {
		out, err := mistralChatResponse([]byte(body), true)
		if err != nil || string(out) != body {
			t.Fatalf("native bytes changed: %q %v", out, err)
		}
	}
	for _, content := range []string{
		`[{"type":"image_url","image_url":"never-drop"}]`,
		`[{"type":"text","text":null}]`, `[{"type":"text","text":7}]`,
		`[{"type":"text","text":"answer","citations":[1]}]`,
		`[{"type":"thinking","thinking":null}]`,
		`[{"type":"thinking","thinking":[{"type":"reference","reference_ids":[1]}]}]`,
		`[{"type":"thinking","thinking":[],"signature":"must-preserve"}]`,
		`[null]`, `["text"]`,
	} {
		body := []byte(`{"choices":[{"delta":{"content":` + content + `}}]}`)
		if _, err := mistralChatResponse(body, true); err == nil {
			t.Fatalf("unrepresentable content silently discarded: %s", content)
		}
	}
}

func TestMistralResponseFastPathKeepsArrayAdaptation(t *testing.T) {
	for _, body := range []string{
		`{"choices":[{"delta":{"co\u006etent":[{"type":"text","text":"ok"}]}}]}`,
		"{\"choices\":[{\"delta\":{\"content\" \n : \t [{\"type\":\"text\",\"text\":\"ok\"}]}}]}",
	} {
		out, err := mistralChatResponse([]byte(body), true)
		if err != nil || !bytes.Contains(out, []byte(`"content":"ok"`)) {
			t.Fatalf("fast path skipped escaped/whitespace array content: %s %v", out, err)
		}
	}
}

func TestMistralChatEventPreservesSSEFraming(t *testing.T) {
	wire := ": comment\r\nid: frame-1\r\nevent: message\r\nretry: 1000\r\ndata: {\"choices\":[{\"delta\":\r\ndata: {\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}]}\r\n\r\n"
	out, err := mistralChatEvent([]byte(wire))
	if err != nil || !bytes.HasPrefix(out, []byte(": comment\r\nid: frame-1\r\nevent: message\r\nretry: 1000\r\ndata: ")) || !bytes.HasSuffix(out, []byte("\r\n\r\n")) || bytes.Count(out, []byte("data:")) != 1 {
		t.Fatalf("framing changed: %q %v", out, err)
	}
	for _, wire := range []string{"data: [DONE]\n\n", ": PING\n\n", "event: error\ndata: {\"error\":{\"message\":\"no\"}}\n\n"} {
		out, err := mistralChatEvent([]byte(wire))
		if err != nil || string(out) != wire {
			t.Fatal("terminal/comment/error event changed")
		}
	}
}

func TestMistralContentStreamFlushesBeforeUpstreamCompletes(t *testing.T) {
	e := newMistralEnv(t)
	release := make(chan struct{})
	defer close(release)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"thinking\",\"thinking\":[{\"type\":\"text\",\"text\":\"thought\"}]}]}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"answer\"}]},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.front.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer mistral-client")
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal("first event buffered until upstream completion:", err)
	}
	defer resp.Body.Close()
	rd := bufio.NewReader(resp.Body)
	first, err := readEvent(rd)
	if err != nil || resp.StatusCode != 200 || !bytes.Contains(first, []byte(`"reasoning_content":"thought"`)) || !bytes.Contains(first, []byte(`"content":""`)) {
		t.Fatalf("missing flushed reasoning: %s %v", first, err)
	}
	release <- struct{}{}
	rest, err := io.ReadAll(rd)
	if err != nil || !bytes.Contains(rest, []byte(`"content":"answer"`)) || !bytes.Contains(rest, []byte("data: [DONE]")) || e.count() != 1 {
		t.Fatalf("lost answer or done: %s %v", rest, err)
	}
	logs, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
	var rec requestLog
	if len(logs.Items) != 1 || json.Unmarshal(logs.Items[0], &rec) != nil || rec.InputTokens == nil || *rec.InputTokens != 17 || rec.OutputTokens == nil || *rec.OutputTokens != 5 || rec.Result != "success" || rec.UpstreamTPS == nil || *rec.UpstreamTPS <= 0 || rec.UpstreamTPSBasis != "post-first-event" {
		t.Fatal("lost measured usage or success outcome")
	}
}

func TestMistralContentResponseAndUnsupportedNoReplay(t *testing.T) {
	for _, tc := range []struct {
		name, path, wire string
		stream           bool
		status           int
	}{
		{"json", "/v1/chat/completions", `{"choices":[{"message":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"thought"}]},{"type":"text","text":"answer"}]}}]}`, false, 200},
		{"first-event", "/v1/chat/completions", "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"image_url\"}]}}]}\n\n", true, 502},
		{"after-commit", "/v1/chat/completions", "data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"image_url\"}]}}]}\n\n", true, 200},
		{"json-unsupported", "/v1/chat/completions", `{"choices":[{"message":{"content":[{"type":"image_url"}]}}]}`, false, 502},
		{"fim-untouched", "/v1/fim/completions", `{"choices":[{"message":{"content":[{"type":"image_url"}]}}]}`, false, 200},
		{"responses-untouched", "/v1/responses", `{"choices":[{"message":{"content":[{"type":"image_url"}]}}]}`, false, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tc.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				fmt.Fprint(w, tc.wire)
			})
			status, body := e.post(t, tc.path, fmt.Sprintf(`{"model":"m","stream":%v,"messages":[]}`, tc.stream), nil)
			if status != tc.status || e.count() != 1 {
				t.Fatalf("bad status or replay: %d %s calls=%d", status, body, e.count())
			}
			switch tc.name {
			case "json":
				if !strings.Contains(body, `"reasoning_content":"thought"`) || !strings.Contains(body, `"content":"answer"`) {
					t.Fatal("JSON content not normalized")
				}
			case "fim-untouched", "responses-untouched":
				if body != tc.wire {
					t.Fatal("non-Chat response rewritten")
				}
			default:
				if !strings.Contains(body, "mistral_content_unsupported") || strings.Contains(body, "[DONE]") {
					t.Fatal("conversion error hidden or fabricated completion")
				}
				logs, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
				var rec requestLog
				if len(logs.Items) != 1 || json.Unmarshal(logs.Items[0], &rec) != nil || rec.Result != "failed" || rec.ErrorCategory != "compatibility" {
					t.Fatal("compatibility failure recorded as success or account fault")
				}
				for _, view := range e.s.pool.Views(e.s.cfg.Get()) {
					if view.Samples != 0 || view.Status != "ok" || view.Active != 0 {
						t.Fatal("conversion failure penalized an account or leaked a lease")
					}
				}
			}
		})
	}
}

func TestMistralContentOtherProviderNative(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			e := newEnv(t, []config.Account{{ID: "native-other"}}, nil)
			wire := `{"choices":[{"message":{"content":[{"type":"text","text":"native"}]}}]}`
			if stream {
				wire = "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"native\"}]}}]}\n\ndata: [DONE]\n\n"
			}
			e.up.mu.Lock()
			e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
				w.Header().Set("Content-Type", "application/json")
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
				}
				fmt.Fprint(w, wire)
			}
			e.up.mu.Unlock()
			resp := e.post("/v1/chat/completions", map[string]any{"model": "m", "stream": stream, "messages": []any{}}, nil)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != 200 || string(body) != wire {
				t.Fatalf("another provider's native content changed: %d %s %v", resp.StatusCode, body, err)
			}
		})
	}
}
