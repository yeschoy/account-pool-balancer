package server

import (
	"net/http"
	"testing"

	"account-pool-balancer/internal/config"
)

// A request that bound by conversation fingerprint must record which session
// headers the client sent (none, for DSH's Gemini client), so the console can
// show why no header identity was used.
func TestSessionHeaderCandidates(t *testing.T) {
	cfg := config.Default()
	h := http.Header{}
	h.Set("X-Session-Id", "abc")
	h.Set("Session-Id", "  ")
	h.Set("User-Agent", "google-genai-sdk/2.21.0 gl-node/v24.21.0, deepseek")
	got := sessionHeaderCandidates(cfg, h)
	if got["X-Session-Id"] != "valid" {
		t.Fatalf("X-Session-Id = %q", got["X-Session-Id"])
	}
	if got["Session-Id"] != "empty" {
		t.Fatalf("Session-Id = %q", got["Session-Id"])
	}
	if len(got) != 2 {
		t.Fatalf("unexpected candidates: %v", got)
	}
	// A request with no session header records nothing.
	if got := sessionHeaderCandidates(cfg, http.Header{"User-Agent": []string{"x"}}); len(got) != 0 {
		t.Fatalf("candidates = %v", got)
	}
}
