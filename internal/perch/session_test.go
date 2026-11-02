package perch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The real GoTrue shapes this program has to classify correctly. A wrong
// verdict here is either an account wrongly marked dead or a dead account
// never marked at all, so each one is pinned.
func TestRenewClassifiesRealErrorShapes(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		header  map[string]string
		body    string
		want    ErrorKind
		needLgn bool
	}{
		{
			// what Supabase actually answers for a spent or unknown token
			name: "supabase validation_failed", status: 400,
			header: map[string]string{"x-sb-error-code": "validation_failed"},
			body:   `{"code":400,"error_code":"validation_failed","msg":"Refresh token is not valid"}`,
			want:   KindAuth, needLgn: true,
		},
		{
			name: "gotrue invalid_grant", status: 400,
			body: `{"error":"invalid_grant","error_description":"Invalid Refresh Token: Already Used"}`,
			want: KindAuth, needLgn: true,
		},
		{
			name: "gotrue refresh_token_not_found", status: 400,
			body: `{"error_code":"refresh_token_not_found","msg":"Invalid Refresh Token"}`,
			want: KindAuth, needLgn: true,
		},
		{
			// a malformed request is our bug, not the account's
			name: "bad json", status: 400,
			body: `{"msg":"JSON parse error","code":400}`,
			want: KindRejected,
		},
		{
			name: "invalid api key", status: 401,
			body: `{"msg":"Invalid API key"}`,
			want: KindUnknown,
		},
		{name: "bare unauthorized", status: 401, want: KindUnknown},
		{name: "proxy forbidden html", status: 403, body: `<html>proxy denied: invalid refresh token</html>`, want: KindUnknown},
		{name: "waf forbidden", status: 403, body: `{"error":"access_denied","message":"Forbidden"}`, want: KindUnknown},
		{name: "explicit used 401", status: 401, body: `{"error_code":"refresh_token_already_used"}`, want: KindAuth, needLgn: true},
		{name: "explicit missing 403", status: 403, body: `{"error_code":"refresh_token_not_found"}`, want: KindAuth, needLgn: true},
		{name: "unrelated already used", status: 400, body: `{"error_code":"coupon_already_used"}`, want: KindRejected},
		{name: "rate limit takes precedence", status: 429, body: `{"error":"invalid_grant"}`, want: KindRateLimited},
		{name: "server error takes precedence", status: 503, body: `{"error":"invalid_grant"}`, want: KindTransient},
		{name: "rate limited", status: 429, body: `{"msg":"slow down"}`, want: KindRateLimited},
		{name: "server error", status: 503, body: `{"msg":"down"}`, want: KindTransient},
		{name: "gateway timeout", status: 504, body: ``, want: KindTransient},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &Client{HTTP: srv.Client(), Now: time.Now}
			_, err := c.Renew(context.Background(), Creds{SupabaseURL: srv.URL, SupabaseKey: "k"}, "some-token")
			if err == nil {
				t.Fatal("want an error")
			}
			if got := KindOf(err); got != tc.want {
				t.Fatalf("kind = %s, want %s (%v)", got, tc.want, err)
			}
			var pe *Error
			if !asError(err, &pe) {
				t.Fatalf("not a perch.Error: %v", err)
			}
			if tc.needLgn && !pe.Authy() {
				t.Fatal("a refused credential must report Authy")
			}
			if !tc.needLgn && pe.Authy() {
				t.Fatal("an ambiguous failure must never look like a refused credential")
			}
		})
	}
}

func asError(err error, target **Error) bool {
	if e, ok := err.(*Error); ok {
		*target = e
		return true
	}
	return false
}

// A transport failure is transient and never auth.
func TestRenewTransportFailureIsTransient(t *testing.T) {
	c := NewClient(&http.Client{Timeout: 300 * time.Millisecond})
	// a port nothing listens on
	_, err := c.Renew(context.Background(), Creds{SupabaseURL: "http://127.0.0.1:1"}, "tok")
	if KindOf(err) != KindTransient {
		t.Fatalf("kind = %s (%v), want transient", KindOf(err), err)
	}
}

// A refresh token that says it is spent is recognised even when the status is
// not 401 and the code is only in the message.
func TestIsWeakRefreshError(t *testing.T) {
	err := &Error{Kind: KindAuth, Status: 400, Message: "Refresh token is not valid"}
	if !IsWeakRefreshError(err) {
		t.Fatal("a spent token message must be recognised")
	}
	other := &Error{Kind: KindAuth, Status: 403, Message: "forbidden"}
	if IsWeakRefreshError(other) {
		t.Fatal("an unrelated refusal must not be treated as a rotated token")
	}
	if IsWeakRefreshError(&Error{Kind: KindTransient, Message: "timeout"}) {
		t.Fatal("a transient error is not a weak refresh error")
	}
}

func TestExpiryOf(t *testing.T) {
	// {"exp":1800000000}
	tok := "h.eyJleHAiOjE4MDAwMDAwMDB9.s"
	exp, ok := ExpiryOf(tok)
	if !ok || exp.Unix() != 1800000000 {
		t.Fatalf("expiry = %v %v", exp, ok)
	}
	if _, ok := ExpiryOf("not-a-jwt"); ok {
		t.Fatal("a non-JWT has no expiry")
	}
}

func TestTrimBase(t *testing.T) {
	for in, want := range map[string]string{
		"https://x.supabase.co/":                "https://x.supabase.co",
		"https://x.supabase.co/auth/v1":         "https://x.supabase.co",
		"https://x.supabase.co/rest/v1/":        "https://x.supabase.co",
		" https://x.supabase.co/auth/v1/token ": "https://x.supabase.co/auth/v1/token",
		"https://x.supabase.co":                 "https://x.supabase.co",
	} {
		if got := TrimBase(in); got != want {
			t.Fatalf("TrimBase(%q) = %q, want %q", in, got, want)
		}
	}
	if u := (Creds{}).url(); u != DefaultSupabaseURL {
		t.Fatalf("an empty creds falls back to the public project, got %s", u)
	}
	if k := (Creds{}).key(); !strings.HasPrefix(k, "sb_publishable_") {
		t.Fatalf("an empty creds falls back to the public key, got %s", k)
	}
}

// The usage meter's figures are converted to Perch's PT unit, rounding up.
func TestPtOfAndMeterReset(t *testing.T) {
	cases := map[float64]int64{0: 0, 0.0001: 1, 1: 1000, 3.25: 3250, 20: 20000}
	for usd, want := range cases {
		if got := PtOf(usd); got != want {
			t.Fatalf("PtOf(%v) = %d, want %d", usd, got, want)
		}
	}
	m := Meter{PeriodStart: "2026-10-15T00:00:00Z"}
	if got := m.ResetAt().Format("2006-01-02"); got != "2026-11-15" {
		t.Fatalf("reset = %s, want a month after the period start", got)
	}
	if !(Meter{}).ResetAt().IsZero() {
		t.Fatal("an absent period start yields no reset date")
	}
}
