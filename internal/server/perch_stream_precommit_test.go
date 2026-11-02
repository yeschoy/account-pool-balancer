package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// readFirstSSEFrame returns the first JSON frame of an SSE response.
func readFirstSSEFrame(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			continue
		}
		return m
	}
	t.Fatalf("no JSON SSE frame in the response")
	return nil
}

// A streaming request that fails before Perch produced a usable frame must be
// answered with the real HTTP status, not a committed 200 followed by an
// in-band error. Committing only after a usable frame is what keeps the
// pool's switch/cooldown rules applicable to the failure.
func TestPerchStreamPrecommitFailureKeepsHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{{
		name:   "auth",
		status: http.StatusUnauthorized,
		body:   `{"ok":false,"error":"access token refused","errorCode":"unauthorized"}`,
	}, {
		name:   "rate",
		status: http.StatusTooManyRequests,
		body:   `{"ok":false,"error":"usage_limit_reached","errorCode":"usage_limit_reached"}`,
	}, {
		name:   "upstream",
		status: http.StatusServiceUnavailable,
		body:   `{"ok":false,"error":"temporarily unavailable"}`,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newPerchModelFake(t)
			fake.status = tc.status
			fake.body = tc.body
			e := withPerchPool(t, fake)
			resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
				"model": "auto", "stream": true,
				"messages": []any{map[string]any{"role": "user", "content": "hi"}},
			})
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				t.Fatalf("a pre-commit upstream failure was reported as HTTP 200")
			}
			if ct := resp.Header.Get("Content-Type"); ct == "text/event-stream" {
				t.Fatalf("pre-commit failure opened an SSE stream (content-type %q)", ct)
			}
		})
	}
}

// The empty assistant frame and the first real frame are written together, so
// a stream that produced content still starts with the role frame.
func TestPerchStreamStartsWithRoleFrame(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	first := readFirstSSEFrame(t, resp)
	choices, _ := first["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("first frame has no choices: %v", first)
	}
	ch, _ := choices[0].(map[string]any)
	delta, _ := ch["delta"].(map[string]any)
	if delta["role"] != "assistant" {
		t.Fatalf("first frame role = %v, want assistant", delta["role"])
	}
}

// A stream whose upstream ends without a usable frame is a failure, and the
// status line has not been committed.
func TestPerchStreamEmptyUpstreamFails(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.script = `data: {"type":"stream_restart","reason":"mid_tool_call_failover"}` + "\n\n"
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatalf("an upstream stream with no done frame answered 200")
	}
}
