package thregister

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoginMismatchOnlyRecognizesKnownBusinessRefusal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		match  bool
	}{
		{"flight200", 200, `1:{"error":"That email and password don't match. Give it another try."}`, true},
		{"json400", 400, `{"error":"That email and password don't match."}`, true},
		{"json401", 401, `{"error":"That email and password don't match."}`, true},
		{"forbidden", 403, `{"error":"That email and password don't match."}`, false},
		{"server-failure", 500, `{"error":"That email and password don't match."}`, false},
		{"rate-limit", 429, `{"error":"That email and password don't match."}`, false},
		{"captcha", 200, `{"error":"That email and password don't match.","needCaptcha":true}`, false},
		{"additional-refusal", 200, `{"error":"That email and password don't match. Account disabled."}`, false},
		{"unstructured-html", 200, `<div>That email and password don't match.</div>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts := 0
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/login" {
					t.Error("unexpected auth side effect")
					w.WriteHeader(500)
					return
				}
				if r.Method == http.MethodGet {
					fmt.Fprint(w, authFormFixture)
					return
				}
				posts++
				if r.URL.Query().Get("mode") != "signin" {
					t.Error("unexpected signup")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer site.Close()
			flow, _ := New(Config{BaseURL: site.URL})
			defer flow.Close()
			err := flow.Login(context.Background(), "test@example.com", "original-password")
			if err == nil || errors.Is(err, ErrLoginCredentialsMismatch) != tc.match || posts != 1 {
				t.Fatalf("wrong mismatch classification or replay: %v, posts=%d", err, posts)
			}
		})
	}
}
