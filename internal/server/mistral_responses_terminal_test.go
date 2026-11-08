package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMistralResponsesFirstTerminalDoesNotWaitForEOF(t *testing.T) {
	for _, tc := range []struct{ name, event, output string }{
		{"empty completed", "response.completed", `[]`},
		{"data-only completed", "", `[]`},
		{"incomplete", "response.incomplete", `[]`},
		{"tool only", "response.completed", `[{"type":"function_call","call_id":"call_patch","name":"apply_patch","arguments":"{\"patch\":\"line1\\nline2\"}"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newMistralEnv(t)
			typ := tc.event
			if typ == "" {
				typ = "response.completed"
			}
			wire := fmt.Sprintf("data: {\"type\":%q,\"response\":{\"id\":\"empty\",\"output\":%s,\"usage\":{\"input_tokens\":4,\"output_tokens\":0}}}\n\n", typ, tc.output)
			if tc.event != "" {
				wire = "event: " + tc.event + "\n" + wire
			}
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, wire)
				w.(http.Flusher).Flush()
				<-r.Context().Done() // A terminal event must not need upstream EOF.
			})
			client := e.front.Client()
			client.Timeout = 2 * time.Second
			req, _ := http.NewRequest(http.MethodPost, e.front.URL+"/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"mock"}`))
			req.Header.Set("Authorization", "Bearer mistral-client")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != 200 || string(body) != wire || e.count() != 1 {
				t.Fatalf("terminal required EOF or changed bytes: status=%d err=%v body=%q", resp.StatusCode, err, body)
			}
			rec := mistralTerminalRequestLog(t, e)
			if rec.Result != "success" || len(rec.Attempts) != 1 || rec.Attempts[0].Terminal != "success" || rec.OutputTokens == nil || *rec.OutputTokens != 0 {
				t.Fatalf("legitimate empty/tool response was not successful: %+v", rec)
			}
		})
	}
}

func TestMistralResponsesMissingTerminalIsFailureWithoutReplay(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		status     int
	}{
		{"empty stream", "", 502},
		{"one unterminated event", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}", 200},
		{"created only", "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n", 200},
		{"delta only", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", 200},
		{"read fault", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n", 200},
		{"DONE is not Responses completion", "data: [DONE]\n\n", 200},
		{"nested terminal text is not terminal", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"response.completed\"}\n\n", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newMistralEnv(t)
			enableMistralWireDetails(t, e)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.name == "read fault" {
					w.Header().Set("Content-Length", "100000") // force unexpected EOF
				}
				fmt.Fprint(w, tc.wire)
			})
			status, body := e.post(t, "/v1/responses", `{"model":"m","stream":true,"input":"mock"}`, nil)
			if status != tc.status || e.count() != 1 || !strings.Contains(body, "mistral_response_incomplete") {
				t.Fatalf("truncation hidden or replayed: %d %q calls=%d", status, body, e.count())
			}
			if strings.Count(body, "mistral_response_incomplete") != 1 || strings.Count(body, "[DONE]") > strings.Count(tc.wire, "[DONE]") {
				t.Fatal("duplicate error or fabricated completion")
			}
			rec := mistralTerminalRequestLog(t, e)
			if rec.Result != "failed" || rec.Status != tc.status || rec.ErrorCategory != "network" || rec.AverageTPS != nil || rec.UpstreamTPS != nil || rec.OutputTokens != nil || len(rec.Attempts) != 1 || rec.Attempts[0].Terminal != "incomplete_stream" {
				t.Fatalf("truncated stream recorded as successful/measured: %+v", rec)
			}
			for _, view := range e.s.pool.Views(e.s.cfg.Get()) {
				if view.Samples != 0 || view.Status != "ok" || view.Active != 0 {
					t.Fatal("incomplete stream penalized account or leaked admission")
				}
			}
			detail := mistralWireRecord(t, e)
			if len(detail.UpstreamAttempts) != 1 || detail.UpstreamAttempts[0].Terminal != "incomplete_stream" {
				t.Fatal("native attempt lost incomplete terminal")
			}
		})
	}
}

func TestMistralResponsesDataOnlyTerminalAfterOutput(t *testing.T) {
	for _, typ := range []string{"response.completed", "response.incomplete", "response.failed"} {
		t.Run(typ, func(t *testing.T) {
			e := newMistralEnv(t)
			wire := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n" + fmt.Sprintf("data: {\"type\":%q,\"response\":{\"id\":\"r\",\"output\":[]}}\n\n", typ)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, wire)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
			client := e.front.Client()
			client.Timeout = 2 * time.Second
			req, _ := http.NewRequest(http.MethodPost, e.front.URL+"/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"mock"}`))
			req.Header.Set("Authorization", "Bearer mistral-client")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || string(body) != wire || resp.StatusCode != 200 || e.count() != 1 {
				t.Fatalf("terminal not relayed promptly: %d %q %v", resp.StatusCode, body, err)
			}
			rec := mistralTerminalRequestLog(t, e)
			want := "success"
			if typ == "response.failed" {
				want = "failed"
				if rec.Attempts[0].Terminal != "upstream_envelope" || rec.ErrorCategory != "upstream" {
					t.Fatal("failed terminal was not classified")
				}
			}
			if rec.Result != want {
				t.Fatalf("wrong terminal result: %s", rec.Result)
			}
		})
	}
}

func TestMistralResponsesClientCancellationIsNotTruncation(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.front.URL+"/v1/responses", strings.NewReader(`{"model":"m","stream":true,"input":"mock"}`))
	req.Header.Set("Authorization", "Bearer mistral-client")
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := readEvent(bufio.NewReader(resp.Body)); err != nil {
		t.Fatal(err)
	}
	cancel()
	_, _ = io.ReadAll(resp.Body)
	rec := mistralTerminalRequestLog(t, e)
	if rec.Status != 499 || rec.Result != "client_cancelled" || e.count() != 1 {
		t.Fatalf("client cancellation misclassified/replayed: %+v", rec)
	}
	for _, view := range e.s.pool.Views(e.s.cfg.Get()) {
		if view.Samples != 0 || view.Active != 0 {
			t.Fatal("cancellation penalized account or leaked lease")
		}
	}
}

func mistralTerminalRequestLog(t *testing.T, e *mistralEnv) requestLog {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		page, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
		if len(page.Items) == 1 {
			var rec requestLog
			if err := json.Unmarshal(page.Items[0], &rec); err != nil {
				t.Fatal(err)
			}
			return rec
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("missing request log")
	return requestLog{}
}
