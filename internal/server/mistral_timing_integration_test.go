package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func latestMistralTimingLog(t *testing.T, e *mistralEnv) requestLog {
	t.Helper()
	page, err := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 1, 0)
	var rec requestLog
	if err != nil || len(page.Items) != 1 || json.Unmarshal(page.Items[0], &rec) != nil {
		t.Fatal("missing request summary", err)
	}
	return rec
}

func TestMistralTimingRealHTTP(t *testing.T) {
	for _, tc := range []struct {
		path, frames, terminal, wantText string
	}{
		{"/v1/chat/completions", `data: {"choices":[{"delta":{"role":"assistant"}}]}

data: {"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"private-thought"}]}]}}]}

data: {"choices":[{"delta":{"content":" \t"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"tool"}}]}}]}

data: {"choices":[{"delta":{"content":"visible-answer"}}]}

data: [DONE]

`, "done", `"content":"visible-answer"`},
		{"/v1/responses", `event: response.created
data: {"type":"response.created","response":{"id":"id"}}

data: {"type":"response.reasoning_summary_text.delta","delta":"private-thought"}

data: {"type":"response.output_item.added","item":{"type":"function_call","name":"tool"}}

event: response.output_text.delta
data: {"delta":"visible-answer"}

data: {"type":"response.completed"}

`, "responses_terminal", `"delta":"visible-answer"`},
	} {
		t.Run(tc.path, func(t *testing.T) {
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				for _, frame := range strings.Split(strings.TrimSuffix(tc.frames, "\n\n"), "\n\n") {
					fmt.Fprint(w, frame+"\n\n")
					w.(http.Flusher).Flush()
				}
			})
			body := `{"model":"m","stream":true,"input":"private-input","messages":[{"role":"user","content":"private-input"}]}`
			for i := 0; i < 2; i++ {
				// ServeHTTP returns after finish(), unlike waiting on a real front
				// body whose terminal can reach the client ahead of log append.
				w := httptest.NewRecorder()
				r := httptest.NewRequest("POST", tc.path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer mistral-client")
				e.s.Handler().ServeHTTP(w, r)
				if w.Code != 200 || !strings.Contains(w.Body.String(), tc.wantText) || e.count() != i+1 {
					t.Fatalf("relay changed: %d %q", w.Code, w.Body.String())
				}
				if tc.terminal == "responses_terminal" && w.Body.String() != tc.frames {
					t.Fatal("native Responses bytes changed")
				}
				rec := latestMistralTimingLog(t, e)
				a := rec.Attempts[0].MistralTiming
				if rec.TTFTMs == nil || rec.MistralTiming == nil || rec.MistralTiming.InputBytes == nil || *rec.MistralTiming.InputBytes != len(body) || rec.MistralTiming.UploadUs == nil || rec.MistralTiming.PrepareUs == nil || rec.MistralTiming.AdmissionUs == nil || a == nil || a.ConnectionReused == nil || (i == 0 && *a.ConnectionReused) || a.ConnectionWaitUs == nil || a.RequestWrittenUs == nil || a.ResponseHeadersUs == nil || a.MaxWriteFlushUs == nil || a.OutboundBytes == nil || *a.OutboundBytes != len(e.call(i).body) || a.ProtocolTerminal != tc.terminal {
					t.Fatalf("missing or incorrect measurements: %+v / %+v", rec.MistralTiming, a)
				}
				var prior int64 = -1
				for _, field := range []*int64{a.HTTPStartUs, a.FirstEventReadUs, a.FirstEventFlushUs, a.FirstThinkingUs, a.FirstToolUs, a.FirstTextUs, a.TerminalFlushUs, a.LastEventFlushUs} {
					if field == nil || *field < prior {
						t.Fatalf("missing or out-of-order milestones: %+v", a)
					}
					prior = *field
				}
				if a.LastEventReadUs == nil || *a.LastEventReadUs < *a.FirstEventReadUs || *a.LastEventReadUs > *a.LastEventFlushUs {
					t.Fatal("last event read and downstream flush were not independently measured")
				}
				data, _ := json.Marshal(a)
				for _, private := range []string{"private-", "visible-answer", "mstrl_", "127.0.0.1"} {
					if strings.Contains(string(data), private) {
						t.Fatal("timing logged raw request metadata")
					}
				}
			}
		})
	}
}

func TestMistralTimingFailuresAndIsolation(t *testing.T) {
	e := newMistralEnv(t)
	path := "/v1/chat/completions"
	body := `{"model":"m","stream":true}`
	post := func(body string) (int, requestLog) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer mistral-client")
		e.s.Handler().ServeHTTP(w, r)
		return w.Code, latestMistralTimingLog(t, e)
	}
	if status, rec := post("{"); status != 400 || len(rec.Attempts) != 0 || rec.MistralTiming == nil || rec.MistralTiming.PrepareUs == nil || rec.MistralTiming.AdmissionUs != nil {
		t.Fatal("local validation must not fabricate admission or HTTP timing")
	}
	e.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		if e.count() == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"mock temporary"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n") // legacy Chat EOF
	})
	if status, rec := post(body); status != 200 || len(rec.Attempts) != 2 || rec.Attempts[0].MistralTiming == nil || rec.Attempts[0].MistralTiming.ResponseHeadersUs == nil || rec.Attempts[0].MistralTiming.FirstTextUs != nil || rec.Attempts[1].MistralTiming == nil || rec.Attempts[1].MistralTiming.ProtocolTerminal != "" || rec.Attempts[1].MistralTiming.FirstTextUs != nil {
		t.Fatal("retry or legacy EOF behavior changed", status, rec.Attempts)
	}
	e.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		h, _, _ := w.(http.Hijacker).Hijack()
		_ = h.Close() // request received, never returned headers
	})
	if status, rec := post(body); status != 502 || len(rec.Attempts) != 1 || rec.Attempts[0].MistralTiming == nil || rec.Attempts[0].MistralTiming.HTTPStartUs == nil || rec.Attempts[0].MistralTiming.ResponseHeadersUs != nil {
		t.Fatal("missing no-headers timing or unsafe replay", status, rec.Attempts)
	}
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.ClientKeys[0].Group, c.ClientKeys[0].Provider = "default", config.ProviderOpenDesign
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer mistral-client")
	e.s.Handler().ServeHTTP(w, r)
	page, _ := e.s.logs.Query("requests", map[string]string{"provider": "opendesign"}, time.Time{}, time.Time{}, 1, 0)
	if len(page.Items) != 1 || strings.Contains(string(page.Items[0]), `"mistralTiming"`) {
		t.Fatal("Mistral timing escaped its pool")
	}
}

type mistralFailedFlushWriter struct{ *httptest.ResponseRecorder }

func (mistralFailedFlushWriter) FlushError() error { return errors.New("disconnected") }

func TestMistralTimingDoesNotCountFailedFlush(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"visible\"}}]}\n\ndata: [DONE]\n\n")
	})
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
	r.Header.Set("Authorization", "Bearer mistral-client")
	e.s.Handler().ServeHTTP(mistralFailedFlushWriter{httptest.NewRecorder()}, r)
	rec := latestMistralTimingLog(t, e)
	a := rec.Attempts[0].MistralTiming
	if e.count() != 1 || rec.Status != 499 || a == nil || a.FirstEventReadUs == nil || a.MaxWriteFlushUs == nil || a.FirstEventFlushUs != nil || a.FirstTextUs != nil || a.TerminalFlushUs != nil {
		t.Fatal("failed Flush was counted as client-visible or replayed")
	}
}

func TestMistralTimingProbeAndSnapshot(t *testing.T) {
	path := "/v1/chat/completions"
	start := time.Now()
	timing := newMistralAttemptTiming(start, path)
	writes := 0
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { writes++ }}
	timing.attach(trace)
	timing.begin(start, 0)
	trace.GetConn("private-proxy:443")
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	first := timing.snapshot()
	trace.GetConn("other-proxy:443")
	trace.GotConn(httptrace.GotConnInfo{Reused: true})
	if writes != 1 || first.RequestWrittenUs == nil || first.ConnectionReused == nil || *first.ConnectionReused || timing.snapshot().RequestWrittenUs != nil {
		t.Fatal("trace hooks overwrote earlier attempt snapshot or write callback")
	}
	large := make([]byte, mistralProbeLimit+1)
	timing.flushed(start, "", large, large, large, path, false, false)
	data := []byte(`{"choices":[{"delta":{"content":"visible"}}]}`)
	timing.flushed(start, "", data, data, data, path, false, false)
	if got := timing.snapshot(); !got.OutputProbeSkipped || got.FirstTextUs != nil || got.FirstEventFlushUs == nil || *got.FirstEventFlushUs != 0 {
		t.Fatal("oversized frame was ignored when claiming a first, or measured zero was lost")
	}
	const all = mistralThinking | mistralText | mistralTool
	if got := mistralOutputKind(path, "", []byte(`{"choices":[{"delta":{"cont\u0065nt":"ok","reasoning_content":"  ","tool_calls":[{"id":"only-id"}]}}]}`), all); got != mistralText {
		t.Fatalf("mistook whitespace/empty tool data for output: %d", got)
	}
	if mistralOutputKind("/v1/responses", "response.created", []byte(`{"type":"response.output_text.delta","delta":"ignored incompatible event type"}`), all) != 0 {
		t.Fatal("conflicting event/type must not create a first text")
	}
}
