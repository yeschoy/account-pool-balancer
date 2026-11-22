package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTavilyRequestAndBoundedResults(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/search" || r.Header.Get("Authorization") != "Bearer fake-search-credential" {
			t.Error("wrong search boundary")
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["query"] != "public documentation" || body["search_depth"] != "basic" || body["include_answer"] != false || body["max_results"] != float64(5) {
			t.Errorf("unexpected body: %v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []Source{{Title: "Docs", URL: "https://example.com/docs", Content: strings.Repeat("中", 5000)}}, "usage": map[string]any{"credits": 1}})
	}))
	defer server.Close()
	backend := Tavily{Endpoint: server.URL + "/search", APIKey: "fake-search-credential"}
	result, err := backend.Search(context.Background(), Request{Query: "public documentation"})
	if err != nil || len(result.Sources) != 1 || len(result.Sources[0].Content) != 12000 || result.Credits == nil || *result.Credits != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if calls.Load() != 1 {
		t.Fatal("unexpected retry")
	}
}
func TestTavilyFailurePrivacyNoRedirectNoRetry(t *testing.T) {
	var followed atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1) }))
	defer trap.Close()
	cases := []struct {
		name                 string
		status               int
		body, location, code string
	}{
		{"auth", 401, `secret-query credential-echo`, "", "search_upstream_error"},
		{"rate", 429, `secret-query credential-echo`, "", "search_upstream_error"},
		{"redirect", 302, "", trap.URL, "search_upstream_error"},
		{"malformed", 200, `{"results":`, "", "invalid_search_response"},
		{"missing", 200, `{}`, "", "invalid_search_response"},
		{"unsafe-source", 200, `{"results":[{"url":"file:///etc/passwd"}]}`, "", "invalid_search_source"},
		{"oversized", 200, strings.Repeat("x", (1<<20)+1), "", "search_response_limit"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if tt.location != "" {
					w.Header().Set("Location", tt.location)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			b := Tavily{Endpoint: srv.URL, APIKey: "fake-search-credential"}
			_, err := b.Search(context.Background(), Request{Query: "secret-query"})
			if err == nil || err.Error() != tt.code || calls.Load() != 1 {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
		})
	}
	if followed.Load() != 0 {
		t.Fatal("credential-bearing redirect was followed")
	}
}
func TestTavilyTimeoutAndCancellation(t *testing.T) {
	canceled := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer srv.Close()
	b := Tavily{Endpoint: srv.URL, APIKey: "fake-search-credential", Timeout: 30 * time.Millisecond}
	_, err := b.Search(context.Background(), Request{Query: "public"})
	if err == nil || err.Error() != "search_timeout" {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("timeout did not cancel backend")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = b.Search(ctx, Request{Query: "public"})
	if err == nil {
		t.Fatal("cancel ignored")
	}
}
