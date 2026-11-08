package server

import (
	"net/http"
	"strings"
	"testing"
)

func TestEnsureMistralXAffinityPreservesClientHeader(t *testing.T) {
	h := http.Header{}
	h.Set("X-Affinity", "client-affinity")
	ensureMistralXAffinity(h, []byte(`{"model":"m","prompt_cache_key":"body-cache","messages":[]}`))
	if got := h.Get("X-Affinity"); got != "client-affinity" {
		t.Fatalf("client affinity overwritten: %q", got)
	}
}

func TestEnsureMistralXAffinityDerivesFromFinalCacheKey(t *testing.T) {
	h := http.Header{}
	ensureMistralXAffinity(h, []byte(`{"model":"m","prompt_cache_key":"session-42","messages":[]}`))
	if got := h.Get("X-Affinity"); got != "session-42" {
		t.Fatalf("expected derived affinity, got %q", got)
	}
}

func TestEnsureMistralXAffinitySkipsEmptyOrMissingKey(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","messages":[]}`,
		`{"model":"m","prompt_cache_key":null,"messages":[]}`,
		`{"model":"m","prompt_cache_key":"","messages":[]}`,
		`{"model":"m","prompt_cache_key":"   ","messages":[]}`,
		`not-json`,
	} {
		h := http.Header{}
		ensureMistralXAffinity(h, []byte(body))
		if got := h.Get("X-Affinity"); got != "" {
			t.Fatalf("invented affinity %q for body %s", got, body)
		}
	}
}

func TestMistralOutboundXAffinityFromGeneratedAndCallerCache(t *testing.T) {
	e := newMistralEnv(t)

	st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"hello-affinity"}]}`, map[string]string{"X-Session-Id": "sticky-session-a"})
	if st != 200 {
		t.Fatal(st)
	}
	c0 := e.call(0)
	var generated string
	if !strings.Contains(c0.body, `"prompt_cache_key":"`) {
		t.Fatalf("generated cache key missing: %s", c0.body)
	}
	start := strings.Index(c0.body, `"prompt_cache_key":"`) + len(`"prompt_cache_key":"`)
	end := strings.Index(c0.body[start:], `"`)
	generated = c0.body[start : start+end]
	if !strings.HasPrefix(generated, "apb-mistral-") {
		t.Fatalf("unexpected generated key %q", generated)
	}
	if got := c0.header.Get("X-Affinity"); got != generated {
		t.Fatalf("generated key not mirrored to x-affinity: body=%q header=%q", generated, got)
	}

	st, _ = e.post(t, "/v1/chat/completions", `{"model":"m","prompt_cache_key":"caller-cache-key","messages":[{"role":"user","content":"hello-caller"}]}`, nil)
	if st != 200 {
		t.Fatal(st)
	}
	c1 := e.call(1)
	if got := c1.header.Get("X-Affinity"); got != "caller-cache-key" || !strings.Contains(c1.body, `"prompt_cache_key":"caller-cache-key"`) {
		t.Fatalf("caller cache not mirrored: body=%s header=%q", c1.body, got)
	}

	st, _ = e.post(t, "/v1/chat/completions", `{"model":"m","prompt_cache_key":"body-should-not-win","messages":[{"role":"user","content":"keep-header"}]}`, map[string]string{"X-Affinity": "explicit-client-affinity"})
	if st != 200 {
		t.Fatal(st)
	}
	c2 := e.call(2)
	if got := c2.header.Get("X-Affinity"); got != "explicit-client-affinity" {
		t.Fatalf("client x-affinity not preserved: %q", got)
	}
}
