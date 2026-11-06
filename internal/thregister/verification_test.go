package thregister

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSendVerificationEmailOnceAndNoSecretReflection(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           int
		body             string
		already, failure bool
	}{
		{"sent-is-not-verified", 200, `{"ok":true}`, false, false},
		{"already-verified", 200, `{"ok":true,"alreadyVerified":true}`, true, false},
		{"business-error", 200, `{"ok":false,"error":"private-secret"}`, false, true},
		{"redirect", 302, "private-secret", false, true},
		{"rate-limit", 429, "private-secret", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			f.SeedCookies([]NamedCookie{{Name: "sb-test-auth-token", Value: "private-session"}})
			calls := 0
			f.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/me/send-verification-email" || r.URL.RawQuery != "" || r.Header.Get("Origin") != DefaultBase || r.Header.Get("Referer") != DefaultBase+"/dashboard" {
					t.Fatal("wrong send request")
				}
				resp := githubMockResponse(r, tc.status, tc.body)
				resp.Header.Set("Location", "https://evil.test/private-secret")
				return resp, nil
			})
			already, err := f.SendVerificationEmail(context.Background())
			if calls != 1 || already != tc.already || (err != nil) != tc.failure {
				t.Fatalf("incorrect result: already=%v err=%v calls=%d", already, err, calls)
			}
			if err != nil && strings.Contains(err.Error(), "private-") {
				t.Fatal("send error leaked upstream secret")
			}
		})
	}
	f, _ := New(Config{})
	defer f.Close()
	f.client.Transport = githubMockTransport(func(*http.Request) (*http.Response, error) { t.Fatal("sent without session"); return nil, io.EOF })
	if _, err := f.SendVerificationEmail(context.Background()); !errors.Is(err, ErrSessionRejected) {
		t.Fatal("missing session accepted")
	}
}

func TestGitHubGrantHTTP200CheckNeverReposts(t *testing.T) {
	gets, posts := 0, 0
	tr := githubMockTransport(func(r *http.Request) (*http.Response, error) {
		resp := githubMockResponse(r, 200, "")
		switch r.Method + " " + r.URL.Host + r.URL.Path {
		case "POST tokenharbor.ai/api/auth/oauth-url":
			resp.Body = io.NopCloser(strings.NewReader(`{"url":"https://auth.tokenharbor.ai/auth/v1/authorize?provider=github"}`))
		case "GET auth.tokenharbor.ai/auth/v1/authorize":
			resp.StatusCode = 302
			resp.Header.Set("Location", "https://github.com/login/oauth/authorize?scope=user%3Aemail")
		case "GET github.com/login/oauth/authorize":
			gets++
			resp.Body = io.NopCloser(strings.NewReader(`<form method="post" action="/login/oauth/authorize"><input name="authenticity_token" value="mock"><button name="authorize" value="1">Authorize</button></form>`))
		case "POST github.com/login/oauth/authorize":
			posts++
		default:
			t.Fatal("unexpected request")
		}
		return resp, nil
	})
	f, err := New(Config{oauthRoundTripper: tr})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = f.RunGitHub(context.Background(), GitHubCredentials{Login: "mock-user", Password: "mock-password", TOTPSecret: "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"}, Result{})
	if err == nil || gets != 2 || posts != 1 || strings.Contains(err.Error(), "未接受") {
		t.Fatalf("unknown grant was replayed or misclassified: gets=%d posts=%d err=%v", gets, posts, err)
	}
}
