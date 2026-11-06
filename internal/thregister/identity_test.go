package thregister

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"account-pool-balancer/internal/egressid"
)

// ollamaStyleConfig points a flow at a local server so the identity it
// presents can be read off the request the server actually received.
func testFlow(t *testing.T, seed string) (*Flow, *[]http.Header) {
	t.Helper()
	var seen []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Clone())
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	f, err := New(Config{BaseURL: srv.URL, Seed: seed, RequestTimeout: 5e9})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(f.Close)
	return f, &seen
}

// The pool defect was one process-wide browser string. Two accounts must not
// present the same User-Agent, or an upstream can cluster them as one client.
func TestRegisterIdentitiesDifferBetweenAccounts(t *testing.T) {
	seen := map[string]bool{}
	for _, seed := range []string{"acct-1", "acct-2", "acct-3", "acct-4", "acct-5", "acct-6", "acct-7", "acct-8"} {
		f, _ := testFlow(t, seed)
		seen[f.UserAgent()] = true
	}
	if len(seen) < 2 {
		t.Fatalf("all accounts share one User-Agent: %v", seen)
	}
}

// The same account must keep its identity across runs, including restarts, or
// the upstream sees it change browser between sessions.
func TestRegisterIdentityStableForOneAccount(t *testing.T) {
	a, _ := testFlow(t, "acct-stable")
	b, _ := testFlow(t, "acct-stable")
	if a.UserAgent() != b.UserAgent() {
		t.Fatalf("identity changed between flows: %q vs %q", a.UserAgent(), b.UserAgent())
	}
}

// KeyUsable reads /v1/models with the new key. It used to send the Go default
// User-Agent; it now has to carry the run's identity like every other step.
func TestKeyUsablePresentsTheRunIdentity(t *testing.T) {
	f, seen := testFlow(t, "acct-key")
	if err := f.KeyUsable(context.Background(), "thk_test"); err != nil {
		t.Fatalf("KeyUsable: %v", err)
	}
	if len(*seen) == 0 {
		t.Fatal("no request recorded")
	}
	h := (*seen)[0]
	if got := h.Get("User-Agent"); got != f.UserAgent() {
		t.Fatalf("KeyUsable User-Agent = %q, want %q", got, f.UserAgent())
	}
	if strings.HasPrefix(h.Get("User-Agent"), "Go-http-client") {
		t.Fatal("KeyUsable still sends the Go default User-Agent")
	}
}

// A browser User-Agent without its hint set is itself a bot signal, so every
// stamped request must carry hints that agree with the UA version.
func TestRegisterRequestCarriesMatchingHints(t *testing.T) {
	f, seen := testFlow(t, "acct-hints")
	if _, err := f.loadSignupPage(context.Background()); err != nil {
		// The stub answers everything with JSON; the identity is stamped before
		// the body is parsed, so a parse failure is irrelevant here.
		t.Logf("loadSignupPage: %v", err)
	}
	if len(*seen) == 0 {
		t.Fatal("no request recorded")
	}
	h := (*seen)[0]
	ua := h.Get("User-Agent")
	major := egressid.ChromeMajor(ua)
	if major == "" {
		t.Fatalf("register UA is not a browser build: %q", ua)
	}
	if got := h.Get("sec-ch-ua"); !strings.Contains(got, "v=\""+major+"\"") {
		t.Fatalf("sec-ch-ua %q does not state version %q", got, major)
	}
	if h.Get("sec-ch-ua-platform") == "" {
		t.Fatal("missing sec-ch-ua-platform beside a browser User-Agent")
	}
	if h.Get("accept-language") == "" {
		t.Fatal("missing accept-language beside a browser User-Agent")
	}
}

// The flow used to hardcode a version far ahead of any shipped Chrome.
func TestRegisterUserAgentIsNotAFutureBuild(t *testing.T) {
	f, _ := testFlow(t, "acct-version")
	major := egressid.ChromeMajor(f.UserAgent())
	if major == "" {
		t.Fatalf("not a Chrome UA: %q", f.UserAgent())
	}
	if major >= "156" {
		t.Fatalf("User-Agent advertises Chrome %s, which no build ships: %q", major, f.UserAgent())
	}
}
