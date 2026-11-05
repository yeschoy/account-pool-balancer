package thregister

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const signupBusyFixture = "We're receiving an unusually high number of sign-ups right now, so this one couldn't go through. Your details are fine — please wait a minute and try again."

func TestRegistrationRateLimitHonorsFullRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		status  int
		header  string
		message string
		want    time.Duration
	}{
		{429, "2592000", "", 30 * 24 * time.Hour},
		{429, "999999999999999999999", "", time.Duration(1<<63 - 1)},
		{503, "60", "", time.Minute},
		{429, "", "", 5 * time.Minute},
		{429, "invalid", "", 5 * time.Minute},
		{200, "", signupBusyFixture, 5 * time.Minute},
	} {
		t.Run(fmt.Sprint(tc.status, tc.header), func(t *testing.T) {
			response := &http.Response{StatusCode: tc.status, Header: make(http.Header)}
			response.Header.Set("Retry-After", tc.header)
			err := registrationRateLimit(response, tc.message)
			var limited *RateLimitError
			if !errors.As(err, &limited) || !errors.Is(err, ErrSessionRejected) || limited.RetryAfter != tc.want {
				t.Fatalf("rate limit = %v, want complete wait %s", err, tc.want)
			}
		})
	}
	response := &http.Response{StatusCode: 503, Header: make(http.Header)}
	response.Header.Set("Retry-After", time.Now().Add(2*time.Hour).UTC().Format(http.TimeFormat))
	var limited *RateLimitError
	if !errors.As(registrationRateLimit(response, ""), &limited) || limited.RetryAfter < 2*time.Hour-2*time.Second || limited.RetryAfter > 2*time.Hour {
		t.Fatal("HTTP-date Retry-After must not be shortened")
	}
	if registrationRateLimit(&http.Response{StatusCode: 200, Header: make(http.Header)}, "That email and password don't match.") != nil {
		t.Fatal("an invalid login must not be invented into a registration rate limit")
	}
}

func TestRegistrationRateLimitStopsAtEachStepWithoutReplay(t *testing.T) {
	for _, stage := range []string{"page", "precheck", "precheck-business", "signup"} {
		t.Run(stage, func(t *testing.T) {
			posts, keys := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/login":
					if r.Method == http.MethodGet {
						if stage == "page" {
							w.WriteHeader(429)
						} else {
							fmt.Fprint(w, authFormFixture)
						}
					} else {
						posts++
						fmt.Fprintf(w, "1:{\"error\":%q}\n", signupBusyFixture)
					}
				case "/api/auth/signup-precheck":
					if stage == "precheck" {
						w.Header().Set("Retry-After", "7200")
						w.WriteHeader(429)
					} else if stage == "precheck-business" {
						fmt.Fprintf(w, `{"error":%q}`, signupBusyFixture)
					} else {
						fmt.Fprint(w, `{"needCaptcha":false}`)
					}
				case "/api/keys":
					keys++
				default:
					t.Error("unexpected captcha/privacy/inference endpoint", r.URL.Path)
				}
			}))
			defer srv.Close()
			flow, _ := New(Config{BaseURL: srv.URL})
			defer flow.Close()
			_, err := flow.Run(context.Background(), Credentials{Email: "mock@example.com", Password: "pw"})
			var limited *RateLimitError
			wantPosts := 0
			if stage == "signup" {
				wantPosts = 1
			}
			if !errors.As(err, &limited) || posts != wantPosts || keys != 0 {
				t.Fatalf("must stop with no replay/key creation: %v, posts=%d, keys=%d", err, posts, keys)
			}
		})
	}
}

func TestBeforeSignupCanStopBeforeIrreversibleSubmission(t *testing.T) {
	posts, callbacks := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			posts++
		}
		if r.URL.Path == "/login" {
			fmt.Fprint(w, authFormFixture)
		} else {
			fmt.Fprint(w, `{"needCaptcha":false}`)
		}
	}))
	defer srv.Close()
	flow, _ := New(Config{BaseURL: srv.URL, BeforeSignup: func(context.Context) error { callbacks++; return context.Canceled }})
	defer flow.Close()
	_, err := flow.Run(context.Background(), Credentials{Email: "mock@example.com", Password: "pw"})
	if !errors.Is(err, context.Canceled) || callbacks != 1 || posts != 0 {
		t.Fatal("submission pacing must be checked before a POST, with zero replay")
	}
}
