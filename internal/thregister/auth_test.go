package thregister

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const authFormFixture = `<input name="$ACTION_1:0" value="1234567890123456789012345678901234567890"><input name="$ACTION_KEY" value="form-secret">`

func TestHasSessionExcludesIntermediateCookies(t *testing.T) {
	for _, name := range []string{"sb-test-auth-token-code-verifier", "th_geo", "th_who", "sb-test-auth-token.bad", "auth-token"} {
		if HasSession([]NamedCookie{{Name: name, Value: "saved"}}) {
			t.Fatalf("intermediate/unrelated cookie %q is not a login session", name)
		}
	}
	for _, name := range []string{"sb-test-auth-token", "sb-test-auth-token.0", "sb-test-auth-token.1"} {
		if !HasSession([]NamedCookie{{Name: name, Value: "saved"}}) {
			t.Fatalf("missing auth cookie %q", name)
		}
	}
	if HasSession([]NamedCookie{{Name: "sb-test-auth-token", Value: ""}}) {
		t.Fatal("cleared auth cookie is not a session")
	}
}

func TestSessionIdentityIgnoresPKCEBesideRealSession(t *testing.T) {
	cookies := siteSession(t, "user-1", 1793819110)
	cookies = append(cookies, NamedCookie{Name: "sb-auth-auth-token-code-verifier", Value: "not-session-json"})
	if uid, _ := SessionIdentity(cookies); uid != "user-1" {
		t.Fatal("PKCE must not corrupt the real session identity")
	}
}

func TestSignupHTTP200BusinessErrorsStopBeforeKeyRequests(t *testing.T) {
	cases := []struct {
		name, body, want string
		kind             error
	}{
		{"flight", "0:{\"a\":\"$@1\"}\n1:{\"error\":\"Account registration refused\"}\n", "Account registration refused", ErrSessionRejected},
		{"json", `{"error":{"message":"Already registered"}}`, "Already registered", ErrSessionRejected},
		{"captcha", "1:{\"error\":\"Complete the challenge\",\"needCaptcha\":true}\n", "", ErrCaptchaRequired},
		{"intermediate only", "0:{\"a\":\"$@1\"}\n1:{}\n", "未发放登录会话", ErrSessionRejected},
		{"credential reflection", "1:{\"error\":\"rejected password-SECRET form-secret\"}\n", "[REDACTED]", ErrSessionRejected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/login":
					if r.Method == http.MethodGet {
						fmt.Fprint(w, authFormFixture)
					} else {
						http.SetCookie(w, &http.Cookie{Name: "sb-test-auth-token-code-verifier", Value: "intermediate-only", Path: "/"})
						fmt.Fprint(w, tc.body)
					}
				case "/api/auth/signup-precheck":
					fmt.Fprint(w, `{"needCaptcha":false}`)
				case "/api/keys":
					keys++
					w.WriteHeader(http.StatusInternalServerError)
				default:
					t.Error("unexpected request", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			var saved Result
			flow, _ := New(Config{BaseURL: srv.URL, Checkpoint: func(res Result) error { saved = res; return nil }})
			defer flow.Close()
			_, err := flow.Run(context.Background(), Credentials{Email: "test@example.com", Password: "password-SECRET"})
			if !errors.Is(err, tc.kind) || !strings.Contains(err.Error(), tc.want) || keys != 0 {
				t.Fatalf("want structured rejection before key request: %v, key requests %d", err, keys)
			}
			if strings.Contains(err.Error(), "password-SECRET") || strings.Contains(err.Error(), "form-secret") {
				t.Fatal("action error reflected a submitted secret")
			}
			if len(saved.Cookies) != 1 || HasSession(saved.Cookies) || saved.APIKey != "" {
				t.Fatal("preserve partial state without calling it a login or issuing a key")
			}
		})
	}
}

func TestResumeWithOnlyPKCEDoesNotAccessSiteOrSignup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid session must stop before any HTTP request", r.URL.Path)
	}))
	defer srv.Close()
	flow, _ := New(Config{BaseURL: srv.URL})
	defer flow.Close()
	_, err := flow.Resume(context.Background(), Result{Cookies: []NamedCookie{{Name: "sb-test-auth-token-code-verifier", Value: "saved"}}})
	if !errors.Is(err, ErrSessionRejected) {
		t.Fatal("resume must reject an intermediate-only cookie")
	}
}

func TestLoginUsesSigninActionAndReportsBusinessError(t *testing.T) {
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login" {
			t.Error("rejected login must not precheck, signup or request keys", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if r.Method == http.MethodGet {
			fmt.Fprint(w, authFormFixture)
			return
		}
		posts++
		if r.URL.Query().Get("mode") != "signin" || !strings.HasSuffix(r.Referer(), "mode=signin") {
			t.Error("login must target the sign-in page, not signup")
		}
		fmt.Fprint(w, "0:{\"a\":\"$@1\"}\n1:{\"error\":\"That email and password don't match. Give it another try.\"}\n")
	}))
	defer srv.Close()
	flow, _ := New(Config{BaseURL: srv.URL})
	defer flow.Close()
	err := flow.Login(context.Background(), "test@example.com", "pw")
	if !errors.Is(err, ErrSessionRejected) || !strings.Contains(err.Error(), "don't match") || posts != 1 {
		t.Fatal("a failed sign-in must expose the real structured error, once")
	}
}
