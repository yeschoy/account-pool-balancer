package thregister

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestGitHubRejectsUntrustedRedirectAndExtraScopes(t *testing.T) {
	for _, target := range []string{"https://untrusted.invalid/collect", "https://github.com/login/oauth/authorize?scope=repo"} {
		t.Run(target, func(t *testing.T) {
			posts, gets := 0, 0
			transport := githubMockTransport(func(r *http.Request) (*http.Response, error) {
				response := githubMockResponse(r, 200, "")
				if r.Method == "POST" && r.URL.Path == "/api/auth/oauth-url" {
					posts++
					response.Body = io.NopCloser(strings.NewReader(`{"url":"https://test.supabase.co/auth/v1/authorize?provider=github"}`))
					return response, nil
				}
				if r.Method == "GET" && r.URL.Host == "test.supabase.co" {
					gets++
					response.StatusCode = 302
					response.Header.Set("Location", target)
					return response, nil
				}
				t.Fatal("followed unsafe redirect or submitted credentials")
				return nil, nil
			})
			flow, err := New(Config{oauthRoundTripper: transport})
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			_, err = flow.RunGitHub(context.Background(), GitHubCredentials{Login: "mock-user", Password: "mock-password", TOTPSecret: "GEZDGNBVGY3TQOJQ"}, Result{})
			if err == nil || posts != 1 || gets != 1 {
				t.Fatal("unsafe OAuth flow accepted/replayed")
			}
		})
	}
}
