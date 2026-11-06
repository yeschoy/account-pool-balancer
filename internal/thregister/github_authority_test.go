package thregister

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubUnknownPageReportsSafeContext(t *testing.T) {
	transport := githubMockTransport(func(r *http.Request) (*http.Response, error) {
		response := githubMockResponse(r, 200, "")
		switch r.URL.Host {
		case "tokenharbor.ai":
			response.Body = io.NopCloser(strings.NewReader(`{"url":"https://auth.tokenharbor.ai/auth/v1/authorize?provider=github"}`))
		case "auth.tokenharbor.ai":
			response.StatusCode = 302
			response.Header.Set("Location", "https://github.com/login?state=private-state")
		case "github.com":
			if r.Method != "GET" {
				t.Fatal("submitted an unknown form")
			}
			response.Body = io.NopCloser(strings.NewReader(`<form method="get" action="/search"><input name="q" value="private-body"></form>`))
		default:
			t.Fatal("unexpected authority")
		}
		return response, nil
	})
	flow, err := New(Config{oauthRoundTripper: transport})
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	_, err = flow.RunGitHub(context.Background(), GitHubCredentials{Login: "mock-user", Password: "mock-password", TOTPSecret: "GEZDGNBVGY3TQOJQ"}, Result{})
	if err == nil || !strings.Contains(err.Error(), "GitHub 登录页") || !strings.Contains(err.Error(), "HTTP 200") {
		t.Fatalf("missing page context: %v", err)
	}
	for _, secret := range []string{"private-state", "private-body", "mock-password"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("page diagnostic leaked sensitive values")
		}
	}
}

func TestGitHubAuthAuthorityRemainsBounded(t *testing.T) {
	for _, target := range []string{
		"https://auth.tokenharbor.ai.evil.invalid/auth/v1/authorize?provider=github",
		"https://other.tokenharbor.ai/auth/v1/authorize?provider=github",
		"http://auth.tokenharbor.ai/auth/v1/authorize?provider=github",
		"https://auth.tokenharbor.ai:444/auth/v1/authorize?provider=github",
		"https://user:password@auth.tokenharbor.ai/auth/v1/authorize?provider=github",
		"https://auth.tokenharbor.ai/auth/v1/authorize?provider=github#token",
	} {
		t.Run(target, func(t *testing.T) {
			calls := 0
			transport := githubMockTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "POST" || r.URL.Host != "tokenharbor.ai" || r.URL.Path != "/api/auth/oauth-url" {
					t.Fatal("followed an untrusted authority")
				}
				response := githubMockResponse(r, 200, "")
				response.Body = io.NopCloser(strings.NewReader(`{"url":"` + target + `"}`))
				return response, nil
			})
			flow, err := New(Config{oauthRoundTripper: transport})
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			_, err = flow.RunGitHub(context.Background(), GitHubCredentials{Login: "mock-user", Password: "mock-password", TOTPSecret: "GEZDGNBVGY3TQOJQ"}, Result{})
			if err == nil || calls != 1 {
				t.Fatal("untrusted entry accepted or replayed")
			}
		})
	}
}
