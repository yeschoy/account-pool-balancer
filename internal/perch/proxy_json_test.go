package perch

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type jsonTestTransport struct{ calls atomic.Int32 }

func (t *jsonTestTransport) RoundTrip(*http.Request) (*http.Response, error) {
	t.calls.Add(1)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"user"}`))}, nil
}

func TestJSONProxyUsesInjectedClientForEmptyAndDoesNotFallback(t *testing.T) {
	transport := &jsonTestTransport{}
	c := NewClient(&http.Client{Transport: transport})
	creds := Creds{SupabaseURL: "http://example.invalid"}
	if _, err := c.User(WithProxy(context.Background(), ""), creds, "token"); err != nil {
		t.Fatal(err)
	}
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("injected client calls = %d, want 1", got)
	}
	creds.ProxyURL = "://bad"
	if _, err := c.User(context.Background(), creds, "token"); KindOf(err) != KindTransient {
		t.Fatalf("invalid proxy error = %v, want transient", err)
	}
	if got := transport.calls.Load(); got != 1 {
		t.Fatalf("invalid proxy fell back to injected client: %d calls", got)
	}
}
