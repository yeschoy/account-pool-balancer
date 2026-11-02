package perch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeWebChat is Perch's web lane: a thread endpoint and a chat endpoint.
type fakeWebChat struct {
	srv       *httptest.Server
	threads   atomic.Int32
	messages  atomic.Int32
	lastBody  atomic.Value
	lastHdr   atomic.Value
	threadSta int
	chatBody  string
	chatCode  int
}

func newFakeWebChat(t *testing.T) *fakeWebChat {
	f := &fakeWebChat{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case WebThreadsPath:
			f.threads.Add(1)
			if f.threadSta >= 300 {
				w.WriteHeader(f.threadSta)
				_, _ = io.WriteString(w, `{"ok":false,"error":"not_authenticated"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"thread":{"id":"thread-abc","title":"New chat"}}`)
		case WebChatPath:
			f.messages.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastBody.Store(body)
			hdr := map[string]string{}
			for k := range r.Header {
				hdr[k] = r.Header.Get(k)
			}
			f.lastHdr.Store(hdr)
			if f.chatCode >= 300 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(f.chatCode)
				_, _ = io.WriteString(w, f.chatBody)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			script := f.chatBody
			if script == "" {
				// the web lane's own done frame shape: modelLabel, tier
				script = `data: {"type":"citations","citations":[],"webUsed":false}` + "\n\n" +
					`data: {"type":"reasoning_delta","text":"thinking"}` + "\n\n" +
					`data: {"type":"answer_delta","text":"pong"}` + "\n\n" +
					`data: {"type":"done","ok":true,"text":"pong","modelLabel":"MiniMax M3",` +
					`"tier":"standard","durationMs":2355,"messageId":"m-1"}` + "\n\n"
			}
			_, _ = io.WriteString(w, script)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// The web surface is the default: a call posted with no surface set must not
// carry a persona preamble (which would be a lie about where the call came
// from), and must report the model Perch actually served.
func TestWebCallNeedsNoPreambleAndReportsModelLabel(t *testing.T) {
	if got := SurfaceFor(""); got != SurfaceWeb {
		t.Fatalf("default surface = %q, want web", got)
	}
	if got := SurfaceFor("  Cli "); got != SurfaceCLI {
		t.Fatalf("SurfaceFor(cli) = %q", got)
	}
	f := newFakeWebChat(t)
	body, _ := json.Marshal(WebMessage{Message: "say pong", ClientMessageID: "c1"})
	c := NewClient(nil)
	res, err := c.CallModel(context.Background(), ModelCall{
		Base: f.srv.URL, WebBase: f.srv.URL, Token: "tok", Body: body, Surface: SurfaceWeb,
	}, 10*time.Second, nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if res.Text != "pong" {
		t.Fatalf("text = %q", res.Text)
	}
	if res.Model != "MiniMax M3" {
		t.Fatalf("model = %q, want the done frame's modelLabel", res.Model)
	}
	if res.ThreadID != "thread-abc" {
		t.Fatalf("thread = %q, want the thread it opened", res.ThreadID)
	}
	if f.threads.Load() != 1 || f.messages.Load() != 1 {
		t.Fatalf("threads=%d messages=%d", f.threads.Load(), f.messages.Load())
	}
	hdr, _ := f.lastHdr.Load().(map[string]string)
	if hdr["x-perch-call-delta"] != "" {
		t.Fatal("the web lane must not claim the CLI's held-context mode")
	}
	if hdr["Authorization"] != "Bearer tok" || hdr["Origin"] == "" {
		t.Fatalf("headers = %v", hdr)
	}
	sent, _ := f.lastBody.Load().(map[string]any)
	if sent["threadId"] != "thread-abc" {
		t.Fatalf("threadId = %v", sent["threadId"])
	}
	if _, has := sent["prompt"]; has {
		t.Fatal("the web lane takes `message`, not `prompt`")
	}
}

// A caller that already names a conversation should join it rather than open
// one thread per request.
func TestWebCallReusesGivenThread(t *testing.T) {
	f := newFakeWebChat(t)
	body, _ := json.Marshal(WebMessage{Message: "again", ClientMessageID: "c2"})
	c := NewClient(nil)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Base: f.srv.URL, WebBase: f.srv.URL, Token: "tok", Body: body,
		Surface: SurfaceWeb, ThreadID: "thread-keep",
	}, 10*time.Second, nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	if f.threads.Load() != 0 {
		t.Fatal("a given thread must not be replaced by a new one")
	}
	sent, _ := f.lastBody.Load().(map[string]any)
	if sent["threadId"] != "thread-keep" {
		t.Fatalf("threadId = %v", sent["threadId"])
	}
}

// The thread endpoint answers 401 with a dead token: that is the account's
// credential, not a network flake, and the caller must be able to tell.
func TestWebThreadRefusalIsClassified(t *testing.T) {
	f := newFakeWebChat(t)
	f.threadSta = http.StatusUnauthorized
	body, _ := json.Marshal(WebMessage{Message: "hi", ClientMessageID: "c3"})
	c := NewClient(nil)
	_, err := c.CallModel(context.Background(), ModelCall{
		Base: f.srv.URL, WebBase: f.srv.URL, Token: "tok", Body: body, Surface: SurfaceWeb,
	}, 5*time.Second, nil)
	var me *ModelError
	if err == nil || !asModelError(err, &me) {
		t.Fatalf("err = %v, want a ModelError", err)
	}
	if !me.Auth {
		t.Fatalf("a refused thread is an auth failure: %+v", me)
	}
}

func asModelError(err error, out **ModelError) bool {
	me, ok := err.(*ModelError)
	if ok {
		*out = me
	}
	return ok
}

// BuildWebMessage folds what the lane cannot carry into the one message it
// accepts, in an order that keeps the caller's instructions effective.
func TestBuildWebMessageFoldsSystemAndHistory(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "you are terse"},
		map[string]any{"role": "user", "content": "first question"},
		map[string]any{"role": "assistant", "content": "first answer"},
		map[string]any{"role": "user", "content": "second question"},
	}}
	msg, ok := BuildWebMessage(body, "t1")
	if !ok {
		t.Fatal("no message built")
	}
	if msg.ThreadID != "t1" || !strings.HasPrefix(msg.ClientMessageID, "odb-") {
		t.Fatalf("envelope = %+v", msg)
	}
	if !strings.HasPrefix(msg.Message, "you are terse\n\n") {
		t.Fatalf("the system turn must lead: %q", msg.Message)
	}
	for _, want := range []string{"earlier turns", "user: first question", "assistant: first answer"} {
		if !strings.Contains(msg.Message, want) {
			t.Fatalf("message lacks %q: %q", want, msg.Message)
		}
	}
	if !strings.HasSuffix(msg.Message, "second question") {
		t.Fatalf("the last user turn must be the prompt: %q", msg.Message)
	}
	if msg.ManualModelOptionID != nil {
		t.Fatal("no model named: no pin should be invented")
	}
}

// A tool result survives as labelled text, and a request with no user turn is
// refused rather than posted as an empty message.
func TestBuildWebMessageToolTurnsAndEmptyRequest(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "user", "content": "what is the weather"},
		map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{
			"id": "c1", "type": "function",
			"function": map[string]any{"name": "get_weather", "arguments": `{"city":"Oslo"}`},
		}}},
		map[string]any{"role": "tool", "tool_call_id": "c1", "content": "rain"},
		map[string]any{"role": "user", "content": "and tomorrow?"},
	}}
	msg, ok := BuildWebMessage(body, "")
	if !ok {
		t.Fatal("no message built")
	}
	for _, want := range []string{"get_weather", "Oslo", "tool result: rain"} {
		if !strings.Contains(msg.Message, want) {
			t.Fatalf("message lacks %q: %q", want, msg.Message)
		}
	}
	if _, ok := BuildWebMessage(map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "only a system turn"},
	}}, ""); ok {
		t.Fatal("a request without a user turn cannot be sent")
	}
}

// A pin is only sent when the caller named an option id: a bare model name
// cannot be mapped onto one, and a substituted pin is invisible upstream.
func TestWebOptionID(t *testing.T) {
	for _, id := range []string{"openrouter-qwen-qwen3-8-max-0902", "wandb-kimi-k2-6", "azure-gpt-5.1"} {
		if got := WebOptionID(id); got != id {
			t.Fatalf("WebOptionID(%q) = %q", id, got)
		}
	}
	for _, name := range []string{"", "auto", "gpt-4o", "standard", "pro_max", "MiniMax M3"} {
		if got := WebOptionID(name); got != "" {
			t.Fatalf("WebOptionID(%q) = %q, want no pin", name, got)
		}
	}
	msg := WebMessage{Message: "x"}.WithModelOption("wandb-kimi-k2-6")
	if msg.ManualModelOptionID == nil || *msg.ManualModelOptionID != "wandb-kimi-k2-6" {
		t.Fatalf("pin = %v", msg.ManualModelOptionID)
	}
}
