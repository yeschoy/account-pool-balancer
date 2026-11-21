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

// Factory refuses a conversation that quotes another agent's prompt. Each
// refused phrase is replaced with wording the gateway accepts, so the model
// reads ordinary language rather than an escape sequence.
func TestEscapeClientPhrases(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash-1017","messages":[` +
		`{"role":"user","content":"You are Claude Code, Anthropic's official CLI for Claude. now help"},` +
		`{"role":"tool","content":[{"type":"text","text":"(user's private global instructions for all projects)"}]}]}`)
	out, changed := EscapeClientPhrases(body)
	if !changed {
		t.Fatal("no phrase rewritten")
	}
	for _, phrase := range AllRefusedPhrases() {
		if strings.Contains(string(out), phrase) {
			t.Fatalf("refused phrase still present: %q in %s", phrase, out)
		}
	}
	if !strings.Contains(string(out), "You are a coding assistant, Anthropic's official CLI for Claude.") {
		t.Fatalf("identity not reworded: %s", out)
	}
	if !strings.Contains(string(out), "(user's global instructions for all projects)") {
		t.Fatalf("global instructions not reworded: %s", out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "deepseek-v4.1-flash-1017" {
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

func TestContainsRefusedPhraseIgnoresRewrittenText(t *testing.T) {
	if !ContainsRefusedPhrase("prefix You have been invoked in the following environment: x") {
		t.Fatal("literal phrase not detected")
	}
	rewritten := RewriteRefusedPhrases("You have been invoked in the following environment:")
	if ContainsRefusedPhrase(rewritten) {
		t.Fatalf("rewritten text reported as refused: %q", rewritten)
	}
	if rewritten != "The session environment is:" {
		t.Fatalf("rewording = %q", rewritten)
	}
	if ContainsRefusedPhrase("nothing refused here") {
		t.Fatal("false positive")
	}
}

func TestRewordPrefersLongestPhrase(t *testing.T) {
	// The "Claude Agent SDK" sentence contains the shorter Claude Code prefix;
	// the longest rewording must win so neither literal remains.
	text := "You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK."
	body, _ := json.Marshal(map[string]any{"t": text})
	out, changed := EscapeClientPhrases(body)
	if !changed {
		t.Fatal("nothing rewritten")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	got := m["t"].(string)
	if got != "You are a coding assistant running within the Claude Agent SDK." {
		t.Fatalf("got %q", got)
	}
	for _, phrase := range AllRefusedPhrases() {
		if strings.Contains(got, phrase) {
			t.Fatalf("phrase remains: %q", phrase)
		}
	}
}

// A conversation that quotes a client self-description is refused; escaping its
// first character is lossless and defeats the check.
func TestEscapeSelfDescription(t *testing.T) {
	body, err := json.Marshal(map[string]any{"a": "--- /tmp/x.json --- {\"text\": \"You are an AI agent powered by DeepSeek Harness.\\n\\nYou are a coding agent.\"}"})
	if err != nil {
		t.Fatal(err)
	}
	out, changed := EscapeClientPhrases(body)
	if !changed {
		t.Fatalf("self-description not escaped: %s", body)
	}
	if strings.Contains(string(out), "You are an AI agent powered by DeepSeek Harness.") {
		t.Fatalf("sentence still literal: %s", out)
	}
	// A product name without the sentinel sentence passes and must stay as is.
	plain := []byte(`{"a":"DeepSeek Harness is a runtime."}`)
	if got, changed := EscapeClientPhrases(plain); changed || string(got) != string(plain) {
		t.Fatalf("innocent text rewritten: %s", got)
	}
}

// The harness self-description is reworded rather than escaped: escaping leaves
// a literal \uXXXX in the text the model reads, while "Runtime" is accepted by
// the gateway and reads naturally.
func TestRewordSelfDescription(t *testing.T) {
	in := "看到：You are an AI agent powered by DeepSeek Harness.\n然后继续"
	body, _ := json.Marshal(map[string]any{"t": in})
	out, changed := EscapeClientPhrases(body)
	if !changed {
		t.Fatal("nothing changed")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	got := m["t"].(string)
	want := "看到：You are an AI agent powered by DeepSeek Runtime.\n然后继续"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

// Codex's opening sentence is refused by the gateway; dropping the product name
// leaves the rest of the prompt acceptable.
func TestRewordAgentIdentity(t *testing.T) {
	in := "You are Codex, a coding agent based on GPT-5. You and the user share one workspace."
	got := RewordAgentIdentity(in)
	want := "You are an assistant, a coding agent based on GPT-5. You and the user share one workspace."
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	if strings.Contains(got, "You are Codex") {
		t.Fatal("product name kept")
	}
	// Unrelated identity wording is left alone.
	plain := "You are a helpful assistant."
	if got := RewordAgentIdentity(plain); got != plain {
		t.Fatalf("rewrote unrelated text: %q", got)
	}
	// And the body pre-check must notice the sentence.
	if !containsPhrase([]byte(`{"a":"` + in + `"}`)) {
		t.Fatal("identity sentence not detected")
	}
}
