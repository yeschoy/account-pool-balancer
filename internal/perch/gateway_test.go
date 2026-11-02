package perch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeModelCall is a Perch model-call proxy: it answers with Perch's event
// stream, counting calls so retry behaviour is observable.
type fakeModelCall struct {
	srv *httptest.Server
	// script is written as the stream body.
	script string
	// contentType overrides the reply's type (JSON = a non-streaming proxy).
	contentType string
	status      int
	body        string
	calls       atomic.Int32
	// lastBody is the request envelope received.
	lastBody atomic.Value
	lastHdr  atomic.Value
}

func newFakeModelCall(t *testing.T) *fakeModelCall {
	f := &fakeModelCall{script: sseDone("hello")}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != ModelCallPath {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.lastBody.Store(body)
		hdr := map[string]string{}
		for k := range r.Header {
			hdr[k] = r.Header.Get(k)
		}
		f.lastHdr.Store(hdr)
		if f.status != 0 && f.status >= 300 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))
			return
		}
		ct := f.contentType
		if ct == "" {
			ct = "text/event-stream; charset=utf-8"
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write([]byte(f.script))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// sseDone builds a minimal stream that answers text.
func sseDone(text string) string {
	return ": keepalive\n\n" +
		`data: {"type":"reasoning_delta","text":"think"}` + "\n\n" +
		`data: {"type":"answer_delta","text":"` + text + `"}` + "\n\n" +
		`data: {"type":"done","ok":true,"text":"` + text + `","finishReason":"stop",` +
		`"provider":"perch","model":"deepseek-ai/DeepSeek-V4.1-Flash",` +
		`"usage":{"inputTokens":12,"outputTokens":2,"cacheReadInputTokens":4,"totalTokens":14}}` + "\n\n"
}

func TestCallModelStreamsEvents(t *testing.T) {
	f := newFakeModelCall(t)
	f.script = ": keepalive\n\n" +
		`data: {"type":"reasoning_delta","text":"th"}` + "\n\n" +
		`data: {"type":"answer_delta","text":"Hel"}` + "\n\n" +
		`data: {"type":"answer_delta","text":"lo"}` + "\n\n" +
		`data: {"type":"done","ok":true,"text":"Hello","finishReason":"stop","usage":{"inputTokens":5,"outputTokens":2}}` + "\n\n"
	c := NewClient(f.srv.Client())
	var got []string
	res, err := c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: f.srv.URL, Token: "tok", Body: []byte(`{}`)}, 5*time.Second, func(ev Event) error {
		got = append(got, ev.Type+":"+ev.Text)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "Hello" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(got) != 4 {
		t.Fatalf("events = %v", got)
	}
	if got[1] != "answer_delta:Hel" {
		t.Fatalf("events = %v", got)
	}
	// the CLI headers the gate and the delta mode depend on
	hdr := f.lastHdr.Load().(map[string]string)
	if hdr["X-Perch-Call-Delta"] != "1" {
		t.Fatal("x-perch-call-delta must be sent")
	}
	if hdr["Accept"] != "text/event-stream" {
		t.Fatal("Accept must be text/event-stream")
	}
	if !strings.HasPrefix(hdr["Authorization"], "Bearer tok") {
		t.Fatal("the account's own token must be the bearer")
	}
	if !strings.HasPrefix(hdr["User-Agent"], "perchai-cli/") {
		t.Fatalf("User-Agent = %q", hdr["User-Agent"])
	}
}

// A 401 is the credential talking: reported as Auth, never retried here (the
// caller renews the session and calls again).
func TestCallModelAuthIsNotRetried(t *testing.T) {
	f := newFakeModelCall(t)
	f.status, f.body = 401, `{"ok":false,"error":"Unauthorized"}`
	c := NewClient(f.srv.Client())
	_, err := c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: f.srv.URL, Token: "bad", Body: []byte(`{}`)}, 5*time.Second, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	me, ok := err.(*ModelError)
	if !ok || !me.Auth {
		t.Fatalf("err = %#v, want Auth", err)
	}
	if f.calls.Load() != 1 {
		t.Fatalf("a 401 was retried %d times", f.calls.Load())
	}
}

// A plan refusal keeps Perch's own code and status so the pool's error rules
// can match it (starter_model_blocked / perch_surface_required).
func TestCallModelPlanRefusalCarriesCode(t *testing.T) {
	f := newFakeModelCall(t)
	f.status = 403
	f.body = `{"ok":false,"error":"Your plan includes Perch-hosted models for Web, Desktop and CLI only.","errorCode":"perch_surface_required","limit":{"plan":"starter"}}`
	c := NewClient(f.srv.Client())
	_, err := c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: f.srv.URL, Token: "tok", Body: []byte(`{}`)}, 5*time.Second, nil)
	me, ok := err.(*ModelError)
	if !ok || me.Code != "perch_surface_required" || me.Status != 403 {
		t.Fatalf("err = %#v", err)
	}
	if me.Retryable {
		t.Fatal("a plan refusal must not be retried")
	}
	if CodeOf(err) != "perch_surface_required" {
		t.Fatal("CodeOf lost the code")
	}
}

// A 5xx before any byte was delivered is retried.
func TestCallModelRetriesTransientBeforeFirstEvent(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":"upstream hiccup"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sseDone("ok")))
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	res, err := c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: srv.URL, Token: "tok", Body: []byte(`{}`)}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("should have retried and succeeded: %v", err)
	}
	if res.Text != "ok" {
		t.Fatalf("text = %q", res.Text)
	}
	if n.Load() != 2 {
		t.Fatalf("calls = %d, want 2", n.Load())
	}
}

// Once text has gone downstream, a restart would duplicate it: no retry.
func TestCallModelDoesNotRetryAfterEvents(t *testing.T) {
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		// an answer, then a broken stream with no done frame
		_, _ = w.Write([]byte(`data: {"type":"answer_delta","text":"partial"}` + "\n\n"))
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	_, err := c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: srv.URL, Token: "tok", Body: []byte(`{}`)}, 5*time.Second, func(Event) error { return nil })
	if err == nil {
		t.Fatal("a stream without done must fail")
	}
	if n.Load() != 1 {
		t.Fatalf("called %d times; a delivered event must stop retries", n.Load())
	}
	me := err.(*ModelError)
	if me.Code != "incomplete_stream" {
		t.Fatalf("code = %s", me.Code)
	}
}

// A proxy that answers one JSON object instead of streaming is accepted.
func TestCallModelAcceptsJSONAnswer(t *testing.T) {
	f := newFakeModelCall(t)
	f.contentType = "application/json"
	f.script = `{"type":"done","ok":true,"text":"single shot","finishReason":"stop","usage":{"inputTokens":3,"outputTokens":1}}`
	c := NewClient(f.srv.Client())
	res, err := c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: f.srv.URL, Token: "tok", Body: []byte(`{}`)}, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "single shot" {
		t.Fatalf("text = %q", res.Text)
	}
}

// done.ok=false is a failure even though the HTTP status was 200.
func TestCallModelDoneNotOK(t *testing.T) {
	f := newFakeModelCall(t)
	f.script = `data: {"type":"done","ok":false,"error":"provider failed","errorCode":"model_call_failed"}` + "\n\n"
	c := NewClient(f.srv.Client())
	_, err := c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: f.srv.URL, Token: "tok", Body: []byte(`{}`)}, 5*time.Second, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if CodeOf(err) != "model_call_failed" {
		t.Fatalf("code = %s", CodeOf(err))
	}
}

func TestCallModelTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	start := time.Now()
	_, err := c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: srv.URL, Token: "tok", Body: []byte(`{}`)}, 150*time.Millisecond, nil)
	if err == nil {
		t.Fatal("want a timeout")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the timeout was not honoured")
	}
	if me, ok := err.(*ModelError); ok && me.Auth {
		t.Fatal("a timeout is never an auth failure")
	}
}

// Two accounts calling at once must not share a token: the client sends what
// each call carries and nothing else.
func TestCallModelKeepsTokensApart(t *testing.T) {
	var seen []string
	var mu atomic.Value
	mu.Store(map[string]bool{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sseDone("token="+tok))
		m := mu.Load().(map[string]bool)
		_ = m
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	done := make(chan string, 2)
	for _, tok := range []string{"tok-A", "tok-B"} {
		go func(tok string) {
			res, err := c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: srv.URL, Token: tok, Body: []byte(`{}`)}, 5*time.Second, nil)
			if err != nil {
				done <- "ERR " + err.Error()
				return
			}
			done <- res.Text
		}(tok)
	}
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		got[<-done] = true
	}
	_ = seen
	if !got["token=tok-A"] || !got["token=tok-B"] {
		t.Fatalf("answers = %v", got)
	}
}
