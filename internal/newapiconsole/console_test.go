package newapiconsole

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The uid header is the single most error-prone part of this API. Measured on
// altapi.ai 2026-11-15: absent → 401 "未提供 New-Api-User", "-1" → 401
// "与登录用户不匹配", real uid → 200. Anything that is not a positive integer
// must never reach the wire.
func TestNormalizeUIDRejectsNonIDs(t *testing.T) {
	ok := map[string]string{
		"241":  "241",
		"0241": "241",
	}
	for in, want := range ok {
		if got := NormalizeUID(in); got != want {
			t.Fatalf("NormalizeUID(%q) = %q, want %q", in, got, want)
		}
	}
	// Whitespace anywhere is refused, never trimmed: a pasted value often
	// carries a newline, and a newline in a header value is header injection.
	bad := []string{"", "  ", "-1", "0", "000", "abc", "24a", "2.41",
		"241\n", "241 ", " 241", "2\n41", "2\r41", "2\t41"}
	for _, in := range bad {
		if got := NormalizeUID(in); got != "" {
			t.Fatalf("NormalizeUID(%q) = %q, want empty", in, got)
		}
	}
}

// Self must refuse locally rather than send a request that will 401: the
// distinction between "no session" and "no uid" is what the UI reports.
func TestSelfRefusesWithoutCredentials(t *testing.T) {
	c := New(Request{Origin: "https://example.invalid"})
	if _, err := c.Self(context.Background()); !errors.Is(err, ErrNoSession) {
		t.Fatalf("no session: %v, want ErrNoSession", err)
	}
	c = New(Request{Origin: "https://example.invalid", Session: "s"})
	if _, err := c.Self(context.Background()); !errors.Is(err, ErrNoUID) {
		t.Fatalf("no uid: %v, want ErrNoUID", err)
	}
}

// The whole point of the package: every console read carries BOTH the session
// cookie and a matching New-Api-User, and the status read carries neither.
func TestRequestsCarrySessionAndUIDTogether(t *testing.T) {
	var gotCookie, gotUID, gotBearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/status":
			gotCookie, gotUID = r.Header.Get("Cookie"), r.Header.Get("New-Api-User")
			w.Write([]byte(`{"data":{"quota_per_unit":500000,"system_name":"TokenPower"}}`))
		case "/api/user/self":
			gotCookie, gotUID = r.Header.Get("Cookie"), r.Header.Get("New-Api-User")
			w.Write([]byte(`{"data":{"id":241,"quota":5000000,"used_quota":0,"request_count":2,"group":"default"},"success":true}`))
		case "/dashboard/billing/usage":
			gotBearer, gotUID = r.Header.Get("Authorization"), r.Header.Get("New-Api-User")
			w.Write([]byte(`{"total_usage":1.58}`))
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	c := New(Request{Origin: srv.URL, Session: "SECRET", UID: "241", Bearer: "KEY"})

	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if gotCookie != "" || gotUID != "" {
		t.Fatalf("status leaked credentials: cookie=%q uid=%q", gotCookie, gotUID)
	}

	self, err := c.Self(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if gotCookie != "session=SECRET" {
		t.Fatalf("self cookie %q", gotCookie)
	}
	if gotUID != "241" {
		t.Fatalf("self uid %q, want the logged-in id (not -1)", gotUID)
	}
	if self.ID != 241 || self.Quota != 5000000 {
		t.Fatalf("self parsed wrong: %+v", self)
	}

	if _, err := c.SpentUSD(ctx); err != nil {
		t.Fatal(err)
	}
	if gotBearer != "Bearer KEY" {
		t.Fatalf("usage bearer %q", gotBearer)
	}
	if gotUID != "" {
		t.Fatalf("usage sent a uid it does not need: %q", gotUID)
	}
}

// The spent read reports cents as dollars and never invents a balance.
func TestSpentUSDCents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"object":"list","total_usage":1.5796}`))
	}))
	defer srv.Close()
	c := New(Request{Origin: srv.URL, Bearer: "K"})
	got, err := c.SpentUSD(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if Round4(got) != 0.0158 {
		t.Fatalf("spent %v, want 0.0158", got)
	}
}

func TestOriginDerivation(t *testing.T) {
	for in, want := range map[string]string{
		"https://altapi.ai/v1":       "https://altapi.ai",
		"https://altapi.ai/v1/":      "https://altapi.ai",
		"http://127.0.0.1:18790/v1":  "http://127.0.0.1:18790",
		"":                           "https://altapi.ai",
		"not a url":                  "https://altapi.ai",
	} {
		if got := Origin(in); got != want {
			t.Fatalf("Origin(%q) = %q, want %q", in, got, want)
		}
	}
}

// A console 429 is shared per-IP throttling: retrying only lengthens it, so
// callers need to recognize it.
func TestIsThrottled(t *testing.T) {
	if !IsThrottled(&HTTPError{Status: http.StatusTooManyRequests}) {
		t.Fatal("429 not recognized as throttling")
	}
	if IsThrottled(&HTTPError{Status: http.StatusUnauthorized}) {
		t.Fatal("401 misreported as throttling")
	}
	if IsThrottled(errors.New("boom")) {
		t.Fatal("arbitrary error misreported as throttling")
	}
}
