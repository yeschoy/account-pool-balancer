package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMistralElapsedDistinguishesUnknownFromObserved(t *testing.T) {
	now := time.Now()
	if mistralElapsed(time.Time{}, now) != nil {
		t.Fatal("missing start must be unknown, not zero")
	}
	if mistralElapsed(now, time.Time{}) != nil {
		t.Fatal("missing end must be unknown, not zero")
	}
	if mistralElapsed(now, now.Add(-time.Second)) != nil {
		t.Fatal("negative duration must be unknown, not zero")
	}
	got := mistralElapsed(now, now.Add(1500*time.Microsecond))
	if got == nil || *got != 1500 {
		t.Fatalf("observed duration was not kept: %v", got)
	}
	same := mistralElapsed(now, now)
	if same == nil || *same != 0 {
		t.Fatal("a sub-microsecond observation must stay distinct from unknown")
	}
}

func TestMistralAttemptTimingRequiresBegin(t *testing.T) {
	timing := newMistralAttemptTiming(time.Now(), "/v1/chat/completions")
	if timing.snapshot() != nil {
		t.Fatal("an attempt that never started must not report a timing record")
	}
	var nilTiming *mistralAttemptTiming
	if nilTiming.snapshot() != nil {
		t.Fatal("other pools have no Mistral timing")
	}
}

// requestLogRecord queries the stored request log for one request id.
func requestLogRecord(t *testing.T, e *mistralEnv, requestID string) map[string]any {
	t.Helper()
	page, err := e.s.logs.Query("requests", map[string]string{"requestId": requestID}, time.Time{}, time.Time{}, 10, 0)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("request log not available: %v items=%d", err, len(page.Items))
	}
	var rec map[string]any
	if err := json.Unmarshal(page.Items[0], &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func streamingMistralHandler(firstDelay time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		time.Sleep(firstDelay)
		for _, frame := range []string{
			`data: {"id":"r","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}],"usage":null}`,
			`data: {"id":"r","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2}}`,
			`data: [DONE]`,
		} {
			if _, err := w.Write([]byte(frame + "\n\n")); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func TestMistralRequestLogReportsFirstTokenSegments(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(streamingMistralHandler(15 * time.Millisecond))
	status, reply := e.post(t, "/v1/chat/completions", `{"model":"ministral-3b-latest","stream":true,"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"X-Session-Id": "timing-session"})
	if status != 200 || !strings.Contains(reply, `"hello"`) {
		t.Fatalf("stream failed: %d %s", status, reply)
	}
	rec := requestLogRecord(t, e, lastRequestID(t, e))

	timing, ok := rec["mistralTiming"].(map[string]any)
	if !ok {
		t.Fatal("request log has no Mistral timing record")
	}
	for _, key := range []string{"uploadUs", "prepareUs", "admissionUs", "inputBytes"} {
		if timing[key] == nil {
			t.Fatalf("request timing lacks %s: %v", key, timing)
		}
	}
	attempts, _ := rec["attempts"].([]any)
	if len(attempts) != 1 {
		t.Fatalf("expected one attempt, got %d", len(attempts))
	}
	att, _ := attempts[0].(map[string]any)
	seg, ok := att["mistralTiming"].(map[string]any)
	if !ok {
		t.Fatal("attempt log has no Mistral timing record")
	}
	num := func(key string) float64 {
		v, ok := seg[key].(float64)
		if !ok {
			t.Fatalf("attempt timing lacks %s: %v", key, seg)
		}
		return v
	}
	// Milestones are microseconds. Handler-relative milestones cannot precede
	// handler entry, and the first flush cannot precede the read it forwards.
	if num("httpStartUs") < 0 || num("firstEventReadUs") < num("httpStartUs") {
		t.Fatalf("milestones out of order: %v", seg)
	}
	if num("firstEventFlushUs") < num("firstEventReadUs") {
		t.Fatalf("first flush preceded the first event read: %v", seg)
	}
	if num("firstTextUs") < num("firstEventReadUs") {
		t.Fatalf("first text preceded the first event: %v", seg)
	}
	if num("terminalFlushUs") < num("lastEventFlushUs") {
		t.Fatalf("terminal flush went backwards: %v", seg)
	}
	if term, _ := seg["protocolTerminal"].(string); term != "done" {
		t.Fatalf("terminal kind not recorded: %v", seg)
	}
	if _, ok := seg["connectionReused"].(bool); !ok {
		t.Fatalf("connection reuse must be observed, not guessed: %v", seg)
	}
	if num("maxWriteFlushUs") < 0 {
		t.Fatalf("write/flush cost must be measured: %v", seg)
	}
	if !e.s.logs.Ready() {
		t.Fatal("log store not ready")
	}
}

func TestMistralNonStreamTimingHasNoFirstEvent(t *testing.T) {
	e := newMistralEnv(t)
	status, reply := e.post(t, "/v1/chat/completions", `{"model":"ministral-3b-latest","messages":[{"role":"user","content":"hi"}]}`, nil)
	if status != 200 || !strings.Contains(reply, `"ok"`) {
		t.Fatalf("non-stream failed: %d %s", status, reply)
	}
	rec := requestLogRecord(t, e, lastRequestID(t, e))
	attempts, _ := rec["attempts"].([]any)
	if len(attempts) != 1 {
		t.Fatalf("expected one attempt, got %d", len(attempts))
	}
	att, _ := attempts[0].(map[string]any)
	seg, ok := att["mistralTiming"].(map[string]any)
	if !ok {
		t.Fatal("attempt log has no Mistral timing record")
	}
	if seg["responseHeadersUs"] == nil {
		t.Fatalf("response header wait must be measured for a non-stream call: %v", seg)
	}
	for _, key := range []string{"firstEventReadUs", "firstEventFlushUs", "firstTextUs"} {
		if _, present := seg[key]; present {
			t.Fatalf("non-stream attempt cannot report %s: %v", key, seg)
		}
	}
}

// lastRequestID returns the newest request id the front server answered.
func lastRequestID(t *testing.T, e *mistralEnv) string {
	t.Helper()
	page, err := e.s.logs.Query("requests", nil, time.Time{}, time.Time{}, 1, 0)
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("no request log: %v", err)
	}
	var rec struct {
		RequestID string `json:"requestId"`
	}
	if json.Unmarshal(page.Items[0], &rec) != nil || rec.RequestID == "" {
		t.Fatal("request log has no request id")
	}
	return rec.RequestID
}

func TestMistralTimingTracksConnectionReuse(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(streamingMistralHandler(0))
	body := `{"model":"ministral-3b-latest","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	var seen []bool
	for i := 0; i < 3; i++ {
		if status, reply := e.post(t, "/v1/chat/completions", body, nil); status != 200 {
			t.Fatalf("request %d failed: %d %s", i, status, reply)
		}
		rec := requestLogRecord(t, e, lastRequestID(t, e))
		attempts, _ := rec["attempts"].([]any)
		if len(attempts) != 1 {
			t.Fatalf("request %d: expected one attempt", i)
		}
		seg, _ := attempts[0].(map[string]any)["mistralTiming"].(map[string]any)
		reused, ok := seg["connectionReused"].(bool)
		if !ok {
			t.Fatalf("request %d: reuse unknown: %v", i, seg)
		}
		seen = append(seen, reused)
	}
	if !seen[len(seen)-1] {
		t.Fatalf("a keep-alive connection was not reused: %v", seen)
	}
}
