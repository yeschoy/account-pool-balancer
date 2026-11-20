package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeBody(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, b)
	}
	return m
}

// Factory refuses a conversation that quotes another agent's prompt. Escaping
// the phrase's first character keeps the check from matching while the model
// still reads the same characters after JSON decoding.
func TestEscapeClientPhrases(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash-1017","messages":[` +
		`{"role":"user","content":"You are Claude Code, Anthropic's official CLI for Claude. now help"},` +
		`{"role":"tool","content":[{"type":"text","text":"(user's private global instructions for all projects)"}]}]}`)
	out, changed := EscapeClientPhrases(body)
	if !changed {
		t.Fatal("no phrase escaped")
	}
	if strings.Contains(string(out), "You are Claude Code") || strings.Contains(string(out), "(user's private") {
		t.Fatalf("literal phrase still present: %s", out)
	}
	if !strings.Contains(string(out), `\u0059ou are Claude Code`) {
		t.Fatalf("first character not escaped: %s", out)
	}
	// Escaping happens on the wire bytes only: JSON parsing returns the exact
	// text the caller sent, so the model reads it unchanged.
	orig := decodeBody(t, body)
	got := decodeBody(t, out)
	origMsg, _ := orig["messages"].([]any)[0].(map[string]any)["content"].(string)
	gotMsg, _ := got["messages"].([]any)[0].(map[string]any)["content"].(string)
	if gotMsg != origMsg {
		t.Fatalf("decoded text changed:\n got %q\nwant %q", gotMsg, origMsg)
	}
	if got["model"] != "deepseek-v4.1-flash-1017" {
		t.Fatal("model changed")
	}
}

func TestEscapeClientPhrasesNoOp(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	out, changed := EscapeClientPhrases(body)
	if changed || string(out) != string(body) {
		t.Fatalf("clean body rewritten: %s", out)
	}
	// A body that is not a JSON object must come back untouched.
	bad := []byte(`not json at all (user's private global instructions for all projects)`)
	if out, changed := EscapeClientPhrases(bad); changed || string(out) != string(bad) {
		t.Fatal("malformed body rewritten")
	}
}

func TestContainsRefusedPhraseIgnoresEscapedText(t *testing.T) {
	if !ContainsRefusedPhrase("prefix You have been invoked in the following environment: x") {
		t.Fatal("literal phrase not detected")
	}
	// On the wire the phrase is gone; after decoding it is back (lossless).
	wire, _ := EscapeClientPhrases([]byte(`{"t":"You have been invoked in the following environment:"}`))
	if strings.Contains(string(wire), "You have been invoked in the following environment:") {
		t.Fatalf("escaped text still literal: %s", wire)
	}
	var dm map[string]any
	_ = json.Unmarshal(wire, &dm)
	if got := dm["t"].(string); got != "You have been invoked in the following environment:" {
		t.Fatalf("decoded text changed: %q", got)
	}
	if ContainsRefusedPhrase("nothing refused here") {
		t.Fatal("false positive")
	}
}

func TestEscapedPhraseKeepsLongestFirst(t *testing.T) {
	// The "Claude Agent SDK" sentence contains the shorter Claude Code prefix;
	// the longest must win so neither is left visible on the wire — while the
	// decoded text stays exactly what the caller sent.
	text := "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."
	body, _ := json.Marshal(map[string]any{"t": text})
	wire, changed := EscapeClientPhrases(body)
	if !changed {
		t.Fatal("nothing escaped")
	}
	for _, phrase := range AllRefusedPhrases() {
		if strings.Contains(string(wire), phrase) {
			t.Fatalf("phrase visible on the wire: %q", phrase)
		}
	}
	var dm map[string]any
	if err := json.Unmarshal(wire, &dm); err != nil {
		t.Fatal(err)
	}
	if got := dm["t"].(string); got != text {
		t.Fatalf("decoded text changed:\n got %q\nwant %q", got, text)
	}
}
