package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// Chat callers whose history begins at the assistant (a trimmed window, a
// prefill, or a Gemini request converted by new-api with its first user turn
// dropped) used to be refused here with "Gemini history must start with user
// content", although Factory's generate route serves contents that open with a
// model turn. The conversion must forward the history unchanged instead.
func TestFactoryGeminiChatLeadingAssistantHistory(t *testing.T) {
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"continued"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`)
	}
	e.mu.Unlock()
	body := `{"model":"gemini-4-carbon","messages":[{"role":"assistant","content":"here is the summary"},{"role":"user","content":"continue"}]}`
	rsp, out := e.do(t, "/v1/chat/completions", body, nil)
	if rsp.StatusCode != 200 || !strings.Contains(out, "continued") {
		t.Fatalf("leading assistant history refused: %d %s", rsp.StatusCode, out)
	}
	wire := e.snapshot()[0].body
	if !strings.Contains(wire, `"contents":[{"parts":[{"text":"here is the summary"}],"role":"model"},{"parts":[{"text":"continue"}],"role":"user"}]`) {
		t.Fatalf("history rewritten on the wire: %s", wire)
	}
	// The order the upstream enforces is the other one: a history that ends at
	// the assistant is refused by Factory ("Requests ending with a model turn
	// are not supported"), so it stays a local error instead of a wasted call.
	before := len(e.snapshot())
	rsp, out = e.do(t, "/v1/chat/completions", `{"model":"gemini-4-carbon","messages":[{"role":"user","content":"write"},{"role":"assistant","content":"Sure, "}]}`, nil)
	if rsp.StatusCode != 400 || len(e.snapshot()) != before || !strings.Contains(out, "must end with user content") {
		t.Fatalf("trailing assistant accepted: %d %s", rsp.StatusCode, out)
	}
	// A history that opens with a tool result has no call to match and is still
	// rejected before any account is leased.
	before = len(e.snapshot())
	rsp, out = e.do(t, "/v1/chat/completions", `{"model":"gemini-4-carbon","messages":[{"role":"tool","tool_call_id":"c1","content":"done"},{"role":"user","content":"go on"}]}`, nil)
	if rsp.StatusCode != 400 || len(e.snapshot()) != before {
		t.Fatalf("leading tool result accepted: %d %s", rsp.StatusCode, out)
	}
}
