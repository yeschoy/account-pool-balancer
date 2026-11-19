package server

import (
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factory"
)

// Turning the opening off must keep the caller's body byte-for-byte apart from
// the model placement the captured route needs.
func TestFactoryDroidBodySwitch(t *testing.T) {
	e := newFactoryEnv(t)
	body := `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`
	e.do(t, "/v1/chat/completions", body, nil)
	if h := e.snapshot()[0]; !strings.Contains(h.body, factory.DroidLine) {
		t.Fatal("default should inject droid's line")
	}

	off := false
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Factory.DroidBody = &off
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.do(t, "/v1/chat/completions", body, nil)
	if h := e.snapshot()[1]; strings.Contains(h.body, factory.DroidLine) {
		t.Fatalf("opening injected while disabled: %s", h.body)
	}

	// Gemini keeps the model placement either way, because the captured route
	// reads it from the body.
	e.do(t, "/v1beta/models/gemini-4-carbon:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, nil)
	call := e.snapshot()[2]
	if !strings.Contains(call.body, `"model":"gemini-4-carbon"`) {
		t.Fatalf("model lost from body: %s", call.body)
	}
	if strings.Contains(call.body, factory.DroidLine) {
		t.Fatalf("gemini opening injected while disabled: %s", call.body)
	}
}
