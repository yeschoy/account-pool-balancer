package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, b)
	}
	return m
}

// Claude Code's identity line becomes droid's and the task instructions stay.
func TestMagpieAdaptChatIdentity(t *testing.T) {
	body := []byte(`{"model":"glm-5.3","messages":[` +
		`{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude.\nSolve the bug."},` +
		`{"role":"user","content":"fix it"}],` +
		`"tools":[{"type":"function","function":{"name":"grep","description":"Run the harness grep helper."}}]}`)
	out, changed := MagpieAdapt("chat", body)
	if !changed {
		t.Fatal("nothing adapted")
	}
	m := mustJSON(t, out)
	sys := m["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(sys, DroidLine) || !strings.Contains(sys, "Solve the bug.") {
		t.Fatalf("system = %q", sys)
	}
	if strings.Contains(sys, "You are Claude Code") {
		t.Fatalf("identity kept: %q", sys)
	}
	desc := m["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["description"]
	if desc != "Run the runtime grep helper." {
		t.Fatalf("tool description = %q", desc)
	}
}

// A body with nothing to adapt must come back byte for byte.
func TestMagpieAdaptNoChange(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)
	out, changed := MagpieAdapt("chat", body)
	if changed || string(out) != string(body) {
		t.Fatalf("innocent body changed: %s", out)
	}
}

// Tool output quoting another agent's prompt is wrapped so the model can
// decode the exact text; the phrase is escaped inside the wrapper.
func TestMagpieAdaptToolOutput(t *testing.T) {
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"tool","content":"see: You have been invoked in the following environment:"}]}`)
	out, changed := MagpieAdapt("chat", body)
	if !changed {
		t.Fatal("tool output left untouched")
	}
	got := mustJSON(t, out)["messages"].([]any)[1].(map[string]any)["content"].(string)
	if !strings.HasPrefix(got, QuotedToolPrefix) {
		t.Fatalf("not wrapped: %q", got)
	}
	if strings.Contains(got, "You have been invoked") {
		t.Fatalf("phrase kept literally: %q", got)
	}
	if !strings.Contains(got, `\u0059ou have been invoked`) {
		t.Fatalf("phrase not escaped: %q", got)
	}
}

func TestMagpieAdaptAnthropicSystem(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-5-5","max_tokens":16,"system":[` +
		`{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.0"},` +
		`{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK.\nDo the task."}],` +
		`"messages":[{"role":"user","content":"go"}]}`)
	out, changed := MagpieAdapt("messages", body)
	if !changed {
		t.Fatal("nothing adapted")
	}
	sys := mustJSON(t, out)["system"].([]any)
	joined := ""
	for _, raw := range sys {
		joined += raw.(map[string]any)["text"].(string) + "\n"
	}
	if !strings.HasPrefix(joined, DroidLine) || !strings.Contains(joined, "Do the task.") {
		t.Fatalf("system = %q", joined)
	}
	if strings.Contains(joined, "x-anthropic-billing-header") {
		t.Fatalf("billing block kept: %q", joined)
	}
}

func TestMagpieAdaptResponsesInstructions(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","instructions":"You are an expert coding assistant operating inside pi, a coding agent harness.\nBuild it.","input":[]}`)
	out, changed := MagpieAdapt("responses", body)
	if !changed {
		t.Fatal("nothing adapted")
	}
	ins := mustJSON(t, out)["instructions"].(string)
	if !strings.HasPrefix(ins, DroidLine) || !strings.Contains(ins, "Build it.") {
		t.Fatalf("instructions = %q", ins)
	}
	if strings.Contains(ins, "coding agent harness") {
		t.Fatalf("pi opening kept: %q", ins)
	}
}
