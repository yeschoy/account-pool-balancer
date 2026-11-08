package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func enableMistralWireDetails(t *testing.T, e *mistralEnv) {
	t.Helper()
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		on := true
		c.Mistral.DetailLog = &on
		c.Routing.SessionHeaders = []string{"X-Private-Session"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mistralWireRecord(t *testing.T, e *mistralEnv) detailLog {
	t.Helper()
	logs, _ := e.s.logs.Query("details", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
	var d detailLog
	if len(logs.Items) != 1 || json.Unmarshal(logs.Items[0], &d) != nil || d.WireVersion != 1 {
		t.Fatal("missing correlated Mistral wire detail")
	}
	return d
}

func TestMistralWireDetailsSeparateNativeBoundaries(t *testing.T) {
	e := newMistralEnv(t)
	enableMistralWireDetails(t, e)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Proof", "native-header")
		w.Header().Set("Set-Cookie", "sid=upstream-cookie-private; HttpOnly")
		fmt.Fprint(w, `{"choices":[{"message":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"thought"}]},{"type":"text","text":"answer"}]}}],"opaque":9007199254740993,"echo":"upstream-cookie-private","echoHeader":"client-custom-token-private","password":"arbitrary-body-private","usage":{"completion_tokens":5}}`)
	})
	body := `{"model":"deepseek-v4-1-flash-1017","reasoning":{"effort":"high"},"store":false,"messages":[{"role":"user","content":"wire-proof"}]}`
	st, _ := e.post(t, "/v1/chat/completions", body, map[string]string{"X-Private-Session": "private-client-session", "X-Client-Proof": "client-header", "Cookie": "session=client-cookie-private", "User-Agent": "", "X-Access-Token": "client-custom-token-private"})
	d := mistralWireRecord(t, e)
	if st != 200 || len(d.UpstreamAttempts) != 1 || !strings.Contains(d.RequestBody, `"reasoning":{"effort":"high"}`) || !strings.Contains(d.RequestBody, `"store":false`) {
		t.Fatal("original client request was replaced by adapted request")
	}
	a := d.UpstreamAttempts[0]
	if a.AttemptIndex != 0 || !a.Sent || a.Status != 200 || a.Response == nil || !strings.Contains(a.Request.Body, `"reasoning_effort":"high"`) || strings.Contains(a.Request.Body, `"store":`) || strings.Contains(a.Request.Body, `"reasoning":`) {
		t.Fatal("actual prepared upstream request/status missing")
	}
	h := http.Header(a.Request.Headers)
	if h.Get("Content-Length") != fmt.Sprint(len(e.call(0).body)) || h.Get("Host") == "" || h.Get("User-Agent") == "" || h.Get("Accept-Encoding") != e.call(0).header.Get("Accept-Encoding") {
		t.Fatal("transport header snapshot differs from the actual upstream request")
	}
	if !strings.Contains(a.Response.Body, `"content":[`) || !strings.Contains(a.Response.Body, "9007199254740993") || !strings.Contains(d.ResponseBody, `"reasoning_content":"thought"`) || http.Header(a.Response.Headers).Get("X-Upstream-Proof") != "native-header" {
		t.Fatal("native response was replaced by adapted output or lost numeric precision")
	}
	raw, _ := json.Marshal(d)
	for _, secret := range []string{"mistral-client", "mstrl_mock_secret", "private-client-session", "client-cookie-private", "upstream-cookie-private", "client-custom-token-private", "arbitrary-body-private", "apb-mistral-"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("private value leaked into wire details: %s", secret)
		}
	}
	if h.Get("Authorization") != "[REDACTED]" || h.Get("X-Affinity") != "[REDACTED]" || http.Header(d.RequestHeaders).Get("Cookie") != "[REDACTED]" {
		t.Fatal("credentials/affinity/cookies not masked")
	}
}

func TestMistralWireDetailsKeepEveryRetryAndBounds(t *testing.T) {
	for _, bounded := range []bool{false, true} {
		e := newMistralEnv(t)
		enableMistralWireDetails(t, e)
		var calls atomic.Int32
		e.setHandler(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if !bounded && calls.Add(1) == 1 {
				w.Header().Set("Set-Cookie", "sid=retry-cookie-private; HttpOnly")
				w.WriteHeader(429)
				fmt.Fprint(w, `{"message":"first attempt limited retry-cookie-private"}`)
				return
			}
			text := "answer"
			if bounded {
				text = strings.Repeat("x", 80<<10)
			}
			fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}],"usage":{"completion_tokens":5}}`, text)
		})
		text := "question"
		if bounded {
			text = strings.Repeat("y", 80<<10)
		}
		st, _ := e.post(t, "/v1/chat/completions", fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":%q}]}`, text), nil)
		d := mistralWireRecord(t, e)
		raw, _ := json.Marshal(d)
		errors, _ := e.s.logs.Query("errors", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
		errorRaw, _ := json.Marshal(errors.Items)
		if strings.Contains(string(raw)+string(errorRaw), "retry-cookie-private") {
			t.Fatal("new upstream cookie leaked into retry details or error summary")
		}
		if st != 200 {
			t.Fatal(st)
		}
		if bounded {
			a := d.UpstreamAttempts[0]
			if !d.RequestBodyTruncated || !a.Request.BodyTruncated || !a.Response.BodyTruncated || a.Response.BodyBytes <= 64<<10 || len(a.Request.Body) > (64<<10)+256 || len(a.Response.Body) > (64<<10)+256 {
				t.Fatal("capture retained an unbounded body or hid truncation")
			}
		} else if len(d.UpstreamAttempts) != 2 || d.UpstreamAttempts[0].Status != 429 || d.UpstreamAttempts[1].Status != 200 || d.UpstreamAttempts[1].AttemptIndex != 1 || d.UpstreamAttempts[0].AccountID == d.UpstreamAttempts[1].AccountID {
			t.Fatal("retried exchanges mixed up or omitted")
		}
	}
}

func TestMistralWireDetailsNoInventedResponse(t *testing.T) {
	cfg := config.Default()
	cfg.Logs.Detail = true
	req, _ := http.NewRequest(http.MethodPost, "http://upstream.invalid/v1/chat/completions", strings.NewReader(`{}`))
	capture := newMistralUpstreamDetail(cfg, req, &requestLog{Provider: config.ProviderMistral}, []byte(`{}`), 1, nil)
	if capture == nil {
		t.Fatal("missing enabled capture")
	}
	wire, _ := capture.finish(&attemptResult{kind: outFailed, terminal: "network"})
	if wire.Response != nil || wire.Status != 0 || wire.Sent {
		t.Fatal("fabricated an HTTP response for a network failure")
	}
}

func TestMistralWireDetailsOff(t *testing.T) {
	e := newMistralEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		off := false
		c.Logs.Detail = true
		c.Mistral.DetailLog = &off
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil)
	logs, _ := e.s.logs.Query("details", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
	if len(logs.Items) != 0 {
		t.Fatal("disabled independent detail switch still captured data")
	}
}
