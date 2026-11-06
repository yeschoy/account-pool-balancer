package thregister

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func githubEmailCookie(email string) string {
	raw := fmt.Sprintf(`{"user":{"email":%q,"user_metadata":{"preferred_username":"mock-user"}},"provider_token":"gho_mock_grant"}`, email)
	return "base64-" + base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func TestGitHubBoundEmailMismatchKeepsCheckpointBeforeFurtherMutations(t *testing.T) {
	keyCalls, checkpoints := 0, 0
	var saved Result
	transport := githubMockTransport(func(r *http.Request) (*http.Response, error) {
		response := githubMockResponse(r, 200, "")
		switch r.URL.Host {
		case "tokenharbor.ai":
			if r.URL.Path == "/api/auth/oauth-url" {
				response.Body = io.NopCloser(strings.NewReader(`{"url":"https://auth.tokenharbor.ai/auth/v1/authorize?provider=github"}`))
			} else if r.URL.Path == "/dashboard" {
				response.Header.Add("Set-Cookie", "sb-auth-auth-token="+githubEmailCookie("different@mock.test")+"; Secure; Path=/")
			} else {
				t.Fatal("unexpected key/finish mutation")
			}
		case "auth.tokenharbor.ai":
			response.StatusCode = 302
			response.Header.Set("Location", "https://tokenharbor.ai/dashboard")
		default:
			t.Fatal("unexpected identity service")
		}
		return response, nil
	})
	flow, _ := New(Config{oauthRoundTripper: transport, Checkpoint: func(res Result) error { checkpoints++; saved = res; return nil }})
	defer flow.Close()
	flow.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
		keyCalls++
		t.Error("created/used key after email mismatch")
		return githubMockResponse(r, 500, ""), nil
	})
	_, err := flow.RunGitHub(context.Background(), GitHubCredentials{Login: "mock-user", Password: "mock-password", TOTPSecret: "GEZDGNBVGY3TQOJQ", Email: "expected@mock.test"}, Result{Email: "expected@mock.test"})
	if err == nil || !strings.Contains(err.Error(), "绑定邮箱不一致") || checkpoints == 0 || keyCalls != 0 || saved.Email != "expected@mock.test" || !HasSession(saved.Cookies) || saved.GitHubToken != "gho_mock_grant" || saved.OAuthComplete {
		t.Fatalf("email identity or checkpoint guard failed: %v", err)
	}
	if strings.Contains(err.Error(), "different@") || strings.Contains(err.Error(), "expected@") {
		t.Fatal("email mismatch reflected private identity")
	}
}

func TestGitHubProvidedEmailDoesNotReplaceMissingUpstreamProof(t *testing.T) {
	transport := githubMockTransport(func(r *http.Request) (*http.Response, error) {
		response := githubMockResponse(r, 200, "")
		switch r.URL.Host {
		case "tokenharbor.ai":
			if r.URL.Path == "/api/auth/oauth-url" {
				response.Body = io.NopCloser(strings.NewReader(`{"url":"https://auth.tokenharbor.ai/auth/v1/authorize?provider=github"}`))
			} else if r.URL.Path == "/dashboard" {
				response.Header.Add("Set-Cookie", "sb-auth-auth-token="+githubEmailCookie("")+"; Secure; Path=/")
			} else {
				t.Fatal("used supplied email as authentication proof")
			}
		case "auth.tokenharbor.ai":
			response.StatusCode = 302
			response.Header.Set("Location", "https://tokenharbor.ai/dashboard")
		}
		return response, nil
	})
	flow, _ := New(Config{oauthRoundTripper: transport})
	defer flow.Close()
	res, err := flow.RunGitHub(context.Background(), GitHubCredentials{Login: "mock-user", Password: "mock-password", TOTPSecret: "GEZDGNBVGY3TQOJQ", Email: "expected@mock.test"}, Result{Email: "expected@mock.test"})
	if !errors.Is(err, ErrSessionRejected) || res.OAuthComplete || res.APIKey != "" {
		t.Fatal("provided bound email became upstream authentication proof")
	}
}

func TestGitHubBoundEmailOnCompletedResumeRejectsMismatchWithoutNetwork(t *testing.T) {
	flow, _ := New(Config{})
	defer flow.Close()
	flow.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
		t.Error("resumed wrong mailbox identity")
		return githubMockResponse(r, 500, ""), nil
	})
	res := Result{Email: "expected@mock.test", OAuthComplete: true, Cookies: []NamedCookie{{Name: "sb-auth-auth-token", Value: githubEmailCookie("different@mock.test")}}}
	_, err := flow.RunGitHub(context.Background(), GitHubCredentials{Login: "mock-user", Password: "pw", TOTPSecret: "GEZDGNBVGY3TQOJQ", Email: "expected@mock.test"}, res)
	if err == nil {
		t.Fatal("completed session bypassed bound-email identity guard")
	}
}
