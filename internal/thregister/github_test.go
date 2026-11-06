package thregister

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type githubMockTransport func(*http.Request) (*http.Response, error)

func (f githubMockTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type githubMockMailbox struct{}

func (githubMockMailbox) Messages(context.Context) ([]Message, error) {
	return []Message{{ID: "new-mail", ReceivedAt: time.Now(), Text: "https://tokenharbor.ai/verify-email?token=mock-mail-token"}}, nil
}
func githubMockResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}

func TestGitHubOAuthFinishAndIndependentSession(t *testing.T) {
	for _, tc := range []struct {
		host          string
		finishFailure bool
		grantHTTP200  bool
	}{
		{"test.supabase.co", false, false}, {"auth.tokenharbor.ai", false, false}, {"auth.tokenharbor.ai", true, false}, {"auth.tokenharbor.ai", false, true},
	} {
		host, finishFailure := tc.host, tc.finishFailure
		t.Run(fmt.Sprintf("host=%s/finishFailure=%v/grantHTTP200=%v", host, finishFailure, tc.grantHTTP200), func(t *testing.T) {
			const action = "1234567890abcdef1234567890abcdef12345678ab"
			const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
			authCalls, finishPosts, keyPosts, verifyCalls, checkpoints := 0, 0, 0, 0, 0
			grantPosts, grantGets, sendCalls := 0, 0, 0
			keyCheckpointed := false
			auth := githubMockTransport(func(r *http.Request) (*http.Response, error) {
				authCalls++
				if r.Header.Get("User-Agent") != OAuthUserAgent {
					t.Fatalf("dishonest/missing OAuth UA at %s", r.URL.Path)
				}
				if r.URL.Host == "github.com" && strings.Contains(r.Header.Get("Cookie"), "sb-auth") {
					t.Fatal("TH cookie leaked to GitHub")
				}
				if r.URL.Host == "tokenharbor.ai" && strings.Contains(r.Header.Get("Cookie"), "user_session") {
					t.Fatal("GitHub cookie leaked to TH")
				}
				response := githubMockResponse(r, 200, "")
				redirect := func(target string) (*http.Response, error) {
					response.StatusCode = 302
					response.Header.Set("Location", target)
					return response, nil
				}
				switch r.Method + " " + r.URL.Host + r.URL.Path {
				case "POST tokenharbor.ai/api/auth/oauth-url":
					response.Body = io.NopCloser(strings.NewReader(fmt.Sprintf(`{"url":"https://%s/auth/v1/authorize?provider=github"}`, host)))
				case "GET " + host + "/auth/v1/authorize":
					return redirect("https://github.com/login")
				case "GET github.com/login":
					response.Body = io.NopCloser(strings.NewReader(`<form method="post" action="/session"><input name="authenticity_token" value="mockcsrf"><input name="login"><input name="password" type="password"></form>`))
				case "POST github.com/session":
					_ = r.ParseForm()
					if r.Form.Get("login") != "mock-user" || r.Form.Get("password") != "mock-password" {
						t.Fatal("login fields missing")
					}
					response.Header.Add("Set-Cookie", "user_session=mock-gh-session; Secure; Path=/; HttpOnly")
					return redirect("/sessions/two-factor/app")
				case "GET github.com/sessions/two-factor/app":
					response.Body = io.NopCloser(strings.NewReader(`<form method="post" action="/sessions/two-factor"><input name="authenticity_token" value="mockcsrf"><input name="app_otp"></form>`))
				case "POST github.com/sessions/two-factor":
					_ = r.ParseForm()
					if len(r.Form.Get("app_otp")) != 6 {
						t.Fatal("TOTP missing")
					}
					return redirect("/login/oauth/authorize?scope=user%3Aemail")
				case "GET github.com/login/oauth/authorize":
					grantGets++
					if tc.grantHTTP200 && grantPosts > 0 {
						return redirect("https://" + host + "/auth/v1/callback?code=mock")
					}
					response.Body = io.NopCloser(strings.NewReader(`<form method="post" action="/login/oauth/authorize"><input name="authenticity_token" value="mockcsrf"><button name="authorize" value="1">Authorize</button><button name="deny" value="1">Cancel</button></form>`))
				case "POST github.com/login/oauth/authorize":
					grantPosts++
					_ = r.ParseForm()
					if r.Form.Get("authorize") != "1" || r.Form.Has("deny") {
						t.Fatal("wrong authorization button")
					}
					if tc.grantHTTP200 {
						response.Body = io.NopCloser(strings.NewReader("consent response without Location"))
						break
					}
					return redirect("https://" + host + "/auth/v1/callback?code=mock")
				case "GET " + host + "/auth/v1/callback":
					return redirect("https://tokenharbor.ai/auth/callback?code=mock")
				case "GET tokenharbor.ai/auth/callback":
					value := "base64-" + base64.RawURLEncoding.EncodeToString([]byte(`{"user":{"email":"mock@local.test","user_metadata":{"preferred_username":"mock-user"}},"expires_at":1999999999,"provider_token":"gho_mock_provider_token"}`))
					response.Header.Add("Set-Cookie", "sb-auth-auth-token="+value+"; Secure; Path=/; HttpOnly")
					return redirect("/oauth/finish?next=%2Fdashboard")
				case "GET tokenharbor.ai/oauth/finish":
					response.Body = io.NopCloser(strings.NewReader(`<input type="hidden" name="$ACTION_1:0" value="{&quot;id&quot;:&quot;` + action + `&quot;,&quot;bound&quot;:&quot;$@1&quot;}"><input type="hidden" name="$ACTION_KEY" value="mock-dynamic-key">`))
				case "POST tokenharbor.ai/oauth/finish":
					finishPosts++
					if r.Header.Get("Next-Action") != action || r.URL.RawQuery != "next=%2Fdashboard" {
						t.Fatal("dynamic finish action/query lost")
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					if r.FormValue("_1_$ACTION_KEY") != "mock-dynamic-key" || r.FormValue("_1_next") != "/dashboard" || r.FormValue("0") != `["$undefined","$K1"]` {
						t.Fatal("finish multipart mismatch")
					}
					if r.Header.Get("Sentry-Trace") != "" || r.Header.Get("Sec-CH-UA") != "" {
						t.Fatal("copied fingerprint/trace headers")
					}
					if finishFailure {
						response.StatusCode = 500
					} else {
						response.Header.Set("X-Action-Redirect", "/dashboard;push")
					}
				default:
					t.Fatalf("unexpected auth call %s %s", r.Method, r.URL.Path)
				}
				return response, nil
			})
			flow, err := New(Config{BaseURL: DefaultBase, UserAgent: OAuthUserAgent, Mailbox: githubMockMailbox{}, oauthRoundTripper: auth,
				Checkpoint: func(res Result) error {
					checkpoints++
					keyCheckpointed = keyCheckpointed || res.APIKey != ""
					return nil
				}})
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			flow.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.Header.Get("Cookie"), "mock-gh-session") {
					t.Fatal("GitHub cookie leaked to session transport")
				}
				switch r.Method + " " + r.URL.Path {
				case "GET /api/keys":
					return githubMockResponse(r, 200, `[]`), nil
				case "POST /api/keys":
					keyPosts++
					return githubMockResponse(r, 201, `{"key":"thk_live_mock_key_abcdefghijklmnopqrstuvwxyz0123456789"}`), nil
				case "POST /api/me/send-verification-email":
					sendCalls++
					if keyPosts != 1 || !keyCheckpointed || r.Header.Get("Origin") != DefaultBase || r.Header.Get("Referer") != DefaultBase+"/dashboard" {
						t.Fatal("verification send preceded the key checkpoint or missed origin")
					}
					return githubMockResponse(r, 200, `{"ok":true}`), nil
				case "GET /verify-email":
					verifyCalls++
					return githubMockResponse(r, 200, "ok"), nil
				default:
					t.Fatalf("unexpected post-OAuth call %s %s", r.Method, r.URL.Path)
					return nil, nil
				}
			})
			creds := GitHubCredentials{Login: "mock-user", Password: "mock-password", TOTPSecret: secret, ProxyURL: "http://127.0.0.1:5000"}
			res, err := flow.RunGitHub(context.Background(), creds, Result{Email: "github:mock-user"})
			if finishPosts != 1 || checkpoints < 2 {
				t.Fatal("missing checkpoint/finish")
			}
			if finishFailure {
				if err == nil || keyPosts != 0 || verifyCalls != 0 || !HasSession(res.Cookies) || len(res.GitHubCookies) == 0 {
					t.Fatal("failure replayed or discarded sessions")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !res.OAuthComplete || !res.VerifiedEmail || res.Email != "mock@local.test" || res.GitHubToken != "gho_mock_provider_token" || len(res.GitHubCookies) == 0 || keyPosts != 1 || verifyCalls != 1 || sendCalls != 1 || grantPosts != 1 || (tc.grantHTTP200 && grantGets != 2) || (!tc.grantHTTP200 && grantGets != 1) {
				t.Fatal("incomplete OAuth/mail result")
			}
			before := authCalls
			if _, err = flow.RunGitHub(context.Background(), creds, res); err != nil {
				t.Fatal(err)
			}
			if authCalls != before || finishPosts != 1 || keyPosts != 1 || sendCalls != 1 {
				t.Fatal("resume repeated OAuth/finish/key creation")
			}
		})
	}
}

func TestGitHubTOTPAndHonestUA(t *testing.T) {
	got, err := githubTOTP("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0))
	if err != nil || got != "287082" {
		t.Fatalf("RFC6238: %s %v", got, err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.UserAgent() != OAuthUserAgent {
			t.Error("UA not honest")
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()
	flow, err := New(Config{BaseURL: server.URL, UserAgent: OAuthUserAgent})
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if err = flow.probeAPIKey(context.Background()); err != nil {
		t.Fatal(err)
	}
}
