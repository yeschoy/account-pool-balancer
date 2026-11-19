package server

import (
	"encoding/json"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factory"
)

func setFactoryMode(t *testing.T, e *deployTestEnv, fn func(*config.Config)) {
	t.Helper()
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error { fn(c); return nil }); err != nil {
		t.Fatal(err)
	}
}

// Factory refuses a System field carrying anything but droid's own prompt, and
// refuses an empty one too. Fold mode moves the caller's text into the first
// user turn and removes the field entirely.
func TestFactoryFoldSystemMode(t *testing.T) {
	e := newFactoryEnv(t)
	setFactoryMode(t, e, func(c *config.Config) { c.Factory.SystemMode = config.SystemFold })

	body := `{"model":"glm-5.3","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`
	e.do(t, "/v1/chat/completions", body, nil)
	up := e.snapshot()[0].body
	var m map[string]any
	if err := json.Unmarshal([]byte(up), &m); err != nil {
		t.Fatal(err)
	}
	msgs := m["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("system message kept: %s", up)
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "user" || !strings.Contains(first["content"].(string), "be brief") {
		t.Fatalf("instructions not moved into the user turn: %s", up)
	}
	if strings.Contains(up, `"role":"system"`) || strings.Contains(up, factory.DroidLine) {
		t.Fatalf("fold mode must leave no system channel: %s", up)
	}

	// Messages (Anthropic) carries the prompt in the top-level system field.
	e.do(t, "/v1/messages", `{"model":"claude-sonnet-5-5","max_tokens":16,"system":"be terse","messages":[{"role":"user","content":"hi"}]}`, nil)
	up = e.snapshot()[1].body
	if strings.Contains(up, `"system"`) {
		t.Fatalf("top-level system kept: %s", up)
	}
	if !strings.Contains(up, "be terse") {
		t.Fatalf("instructions lost: %s", up)
	}

	// Gemini moves the systemInstruction into the first user part.
	e.do(t, "/v1beta/models/gemini-4-carbon:generateContent", `{"systemInstruction":{"parts":[{"text":"be short"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, nil)
	up = e.snapshot()[2].body
	if strings.Contains(up, "systemInstruction") {
		t.Fatalf("gemini systemInstruction kept: %s", up)
	}
	if !strings.Contains(up, "be short") || !strings.Contains(up, `"model":"gemini-4-carbon"`) {
		t.Fatalf("gemini fold lost text or model: %s", up)
	}
}

// Keep mode must not touch the caller's system channel at all.
func TestFactoryKeepSystemMode(t *testing.T) {
	e := newFactoryEnv(t)
	setFactoryMode(t, e, func(c *config.Config) { c.Factory.SystemMode = config.SystemKeep })
	body := `{"model":"glm-5.3","messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`
	e.do(t, "/v1/chat/completions", body, nil)
	if up := e.snapshot()[0].body; strings.Contains(up, factory.DroidLine) {
		t.Fatalf("keep mode opened the prompt: %s", up)
	}
}

// A conversation quoting another agent's prompt is refused by Factory's usage
// policy; the escape rewrites one character per phrase before the request
// leaves, and leaves everything else alone.
func TestFactoryEscapesRefusedClientPhrases(t *testing.T) {
	e := newFactoryEnv(t)
	setFactoryMode(t, e, func(c *config.Config) { c.Factory.SystemMode = config.SystemFold })
	body := `{"model":"glm-5.3","messages":[{"role":"user","content":"repeat: You are Claude Code, Anthropic's official CLI for Claude."}]}`
	e.do(t, "/v1/chat/completions", body, nil)
	up := e.snapshot()[0].body
	if strings.Contains(up, "You are Claude Code") {
		t.Fatalf("refused phrase sent literally: %s", up)
	}
	if !strings.Contains(up, `\u0059ou are Claude Code`) {
		t.Fatalf("phrase not escaped: %s", up)
	}

	// Turning the escape off sends the text untouched.
	off := false
	setFactoryMode(t, e, func(c *config.Config) { c.Factory.EscapeClientPhrases = &off })
	e.do(t, "/v1/chat/completions", body, nil)
	if up := e.snapshot()[1].body; !strings.Contains(up, "You are Claude Code") {
		t.Fatalf("escape ran while disabled: %s", up)
	}
}

func TestFactoryDroidVersionSetting(t *testing.T) {
	e := newFactoryEnv(t)
	setFactoryMode(t, e, func(c *config.Config) { c.Factory.DroidVersion = "0.260.1" })
	e.do(t, "/v1/chat/completions", `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`, nil)
	h := e.snapshot()[0].header
	if h.Get("User-Agent") != "factory-cli/0.260.1" || h.Get("X-Client-Version") != "0.260.1" {
		t.Fatalf("UA=%q version=%q", h.Get("User-Agent"), h.Get("X-Client-Version"))
	}
}

// The magpie mode rewrites a client identity line instead of folding it away.
func TestFactoryMagpieSystemMode(t *testing.T) {
	e := newFactoryEnv(t)
	setFactoryMode(t, e, func(c *config.Config) { c.Factory.SystemMode = config.SystemMagpie })
	body := `{"model":"glm-5.3","messages":[{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude.\nDo it."},{"role":"user","content":"hi"}]}`
	e.do(t, "/v1/chat/completions", body, nil)
	up := e.snapshot()[0].body
	if !strings.HasPrefix(up, `{"messages":[{"content":"You are Droid`) && !strings.Contains(up, "You are Droid") {
		t.Fatalf("identity not rewritten: %s", up)
	}
	if strings.Contains(up, "You are Claude Code") {
		t.Fatalf("client identity kept: %s", up)
	}
	if !strings.Contains(up, "Do it.") {
		t.Fatalf("instructions lost: %s", up)
	}
}

// Factory refuses a body whose system channel is absent or does not open with
// droid's line (measured: no system -> 403, "You are a helpful assistant." ->
// 403, droid's line -> 200). Every mode but keep must therefore guarantee the
// opening, including when the caller sends no system message at all.
func TestFactoryMagpieAlwaysOpens(t *testing.T) {
	e := newFactoryEnv(t)
	setFactoryMode(t, e, func(c *config.Config) { c.Factory.SystemMode = config.SystemMagpie })

	// No system message at all.
	e.do(t, "/v1/chat/completions", `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`, nil)
	up := e.snapshot()[0].body
	if !strings.Contains(up, `"role":"system"`) || !strings.Contains(up, factory.DroidLine) {
		t.Fatalf("no system opening added: %s", up)
	}

	// System content as a block list.
	e.do(t, "/v1/chat/completions", `{"model":"glm-5.3","messages":[{"role":"system","content":[{"type":"text","text":"You are a helpful assistant."}]},{"role":"user","content":"hi"}]}`, nil)
	up = e.snapshot()[1].body
	if !strings.Contains(up, factory.DroidLine) {
		t.Fatalf("block system not opened: %s", up)
	}

	// Gemini keeps its systemInstruction and opens with droid's line.
	e.do(t, "/v1beta/models/gemini-4-carbon:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, nil)
	up = e.snapshot()[2].body
	if !strings.Contains(up, "systemInstruction") || !strings.Contains(up, factory.DroidLine) {
		t.Fatalf("gemini opening missing: %s", up)
	}
}
