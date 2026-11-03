package perch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTicketServer answers /api/perch-terminal/turn-ticket the way Perch does,
// and records what it was asked.
type fakeTicketServer struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []map[string]any
	headers  []http.Header

	// status / body answer a refusal when status >= 300.
	status int
	body   string
	// expiresAt is advertised on a successful ticket ("" = five minutes).
	expiresAt string
	// ticketID lets a test give each issue a distinct id.
	ticketID string
	// payload overrides the success body verbatim when set.
	payload string
	// enforced marks the refusal as turn-stopping.
	enforced bool
	// calls counts requests.
	calls atomic.Int32
}

func newFakeTicketServer(t *testing.T) *fakeTicketServer {
	f := &fakeTicketServer{ticketID: "tt_1"}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != TurnTicketPath {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.requests = append(f.requests, body)
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if f.status >= 300 {
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, f.body)
			return
		}
		if f.payload != "" {
			_, _ = io.WriteString(w, f.payload)
			return
		}
		exp := f.expiresAt
		if exp == "" {
			exp = time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339)
		}
		raw, _ := json.Marshal(map[string]any{
			"ok": true, "ticket": "tk_" + f.ticketID, "ticketId": f.ticketID,
			"runId":     "cli-turn-1700000000000-" + f.ticketID,
			"expiresAt": exp, "profile": "standard", "enforced": f.enforced,
		})
		_, _ = w.Write(raw)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// req builds a ticket request against the fake.
func (f *fakeTicketServer) req(key string) TicketRequest {
	return TicketRequest{Key: key, Base: f.srv.URL, Token: "fixture-access"}
}

func (f *fakeTicketServer) lastRequest(t *testing.T) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no ticket request was made")
	}
	return f.requests[len(f.requests)-1]
}

// A ticket is requested with {"surface":"cli","profile":"standard"} and the
// CLI's identity headers, before any model call.
func TestTicketIssueShape(t *testing.T) {
	f := newFakeTicketServer(t)
	c := NewClient(nil)

	tk, err := c.TicketFor(context.Background(), f.req("acc1"))
	if err != nil {
		t.Fatalf("TicketFor: %v", err)
	}
	if tk == nil {
		t.Fatal("no ticket")
	}
	if tk.Token != "tk_tt_1" || tk.TicketID != "tt_1" || tk.RunID != "cli-turn-1700000000000-tt_1" {
		t.Fatalf("ticket = %+v", tk)
	}
	if tk.Profile != "standard" || tk.Enforced {
		t.Fatalf("ticket profile/enforced = %q/%v", tk.Profile, tk.Enforced)
	}

	body := f.lastRequest(t)
	if body["surface"] != "cli" || body["profile"] != "standard" {
		t.Fatalf("issue body = %v, want {surface:cli, profile:standard}", body)
	}
	f.mu.Lock()
	hdr := f.headers[len(f.headers)-1]
	f.mu.Unlock()
	if got := hdr.Get("Authorization"); got != "Bearer fixture-access" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := hdr.Get("User-Agent"); got != UserAgent {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := hdr.Get(LocaleHeader); got != DefaultLocale {
		t.Fatalf("%s = %q", LocaleHeader, got)
	}
	if got := hdr.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
}

// A live ticket is reused rather than re-requested: the budget is per ticket,
// so obtaining a new one on every call would defeat the whole point.
func TestTicketIsReusedWhileFresh(t *testing.T) {
	f := newFakeTicketServer(t)
	c := NewClient(nil)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		tk, err := c.TicketFor(ctx, f.req("acc1"))
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if tk == nil || tk.Token != "tk_tt_1" {
			t.Fatalf("call %d: ticket = %+v", i, tk)
		}
	}
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("issued %d tickets for 5 calls, want 1", got)
	}
}

// Two accounts never share a ticket: each has its own budget and its own run.
func TestTicketIsPerAccount(t *testing.T) {
	f := newFakeTicketServer(t)
	c := NewClient(nil)
	ctx := context.Background()

	a, err := c.TicketFor(ctx, f.req("accA"))
	if err != nil || a == nil {
		t.Fatalf("accA: %v %+v", err, a)
	}
	f.mu.Lock()
	f.ticketID = "tt_2"
	f.mu.Unlock()
	b, err := c.TicketFor(ctx, f.req("accB"))
	if err != nil || b == nil {
		t.Fatalf("accB: %v %+v", err, b)
	}
	if a.Token == b.Token {
		t.Fatalf("two accounts got the same ticket %q", a.Token)
	}
	if got := f.calls.Load(); got != 2 {
		t.Fatalf("issued %d tickets, want 2 (one per account)", got)
	}
}

// Inside the renewal window the ticket is renewed with {renew:true, ticketId},
// and the renewal replaces it.
func TestTicketRenewsNearExpiry(t *testing.T) {
	f := newFakeTicketServer(t)
	// Expire just inside the 30 s lead, so the first TicketFor renews.
	f.expiresAt = time.Now().Add(TicketRenewLead - time.Second).UTC().Format(time.RFC3339)
	c := NewClient(nil)
	ctx := context.Background()

	first, err := c.TicketFor(ctx, f.req("acc1"))
	if err != nil || first == nil {
		t.Fatalf("issue: %v %+v", err, first)
	}

	f.mu.Lock()
	f.ticketID = "tt_2"
	f.expiresAt = ""
	f.mu.Unlock()

	second, err := c.TicketFor(ctx, f.req("acc1"))
	if err != nil || second == nil {
		t.Fatalf("renew: %v %+v", err, second)
	}
	if second.TicketID != "tt_2" {
		t.Fatalf("ticket was not renewed, id = %q", second.TicketID)
	}
	body := f.lastRequest(t)
	if body["renew"] != true || body["ticketId"] != "tt_1" {
		t.Fatalf("renew body = %v, want {renew:true, ticketId:tt_1}", body)
	}
	if _, ok := body["surface"]; ok {
		t.Fatalf("renewal must not carry surface: %v", body)
	}
}

// A renewal that fails (non-enforced) keeps the old ticket while it is still
// valid — LQt's behaviour.
func TestTicketRenewalFailureKeepsUnexpiredTicket(t *testing.T) {
	f := newFakeTicketServer(t)
	f.expiresAt = time.Now().Add(TicketRenewLead - time.Second).UTC().Format(time.RFC3339)
	c := NewClient(nil)
	ctx := context.Background()

	first, err := c.TicketFor(ctx, f.req("acc1"))
	if err != nil || first == nil {
		t.Fatalf("issue: %v %+v", err, first)
	}

	f.status, f.body = http.StatusInternalServerError, `{"ok":false,"error":"boom"}`
	tk, err := c.TicketFor(ctx, f.req("acc1"))
	if err != nil {
		t.Fatalf("an unenforced renewal failure must not fail the turn: %v", err)
	}
	if tk == nil || tk.Token != first.Token {
		t.Fatalf("expected the unexpired ticket to be reused, got %+v", tk)
	}
}

// An enforced 429 turn_rate_limited is a TicketError and names the code.
func TestTicketRateLimitIsEnforced(t *testing.T) {
	f := newFakeTicketServer(t)
	f.status = http.StatusTooManyRequests
	f.body = `{"ok":false,"enforced":true,"errorCode":"turn_rate_limited","error":"your plan's turn rate limit"}`
	c := NewClient(nil)

	tk, err := c.TicketFor(context.Background(), f.req("acc1"))
	if err == nil {
		t.Fatalf("expected a rate-limit error, got ticket %+v", tk)
	}
	var te *TicketError
	if !asTicketError(err, &te) {
		t.Fatalf("error is %T, want *TicketError", err)
	}
	if !te.IsRateLimited() || te.Code != TicketCodeRateLimited || !te.Enforced {
		t.Fatalf("ticket error = %+v", te)
	}
}

// An enforced refusal of any other kind is an unavailable ticket.
func TestTicketUnavailableIsEnforced(t *testing.T) {
	f := newFakeTicketServer(t)
	f.status = http.StatusServiceUnavailable
	f.body = `{"ok":false,"enforced":true,"error":"gate down"}`
	c := NewClient(nil)

	_, err := c.TicketFor(context.Background(), f.req("acc1"))
	var te *TicketError
	if !asTicketError(err, &te) {
		t.Fatalf("error is %v (%T), want *TicketError", err, err)
	}
	if te.Code != TicketCodeUnavailable || te.Status != http.StatusServiceUnavailable {
		t.Fatalf("ticket error = %+v", te)
	}
}

// A refusal that is NOT enforced degrades to "no ticket" — (nil, nil), which
// means "call the model unticketed", not "the turn failed".
func TestTicketUnenforcedFailureDegrades(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", http.StatusUnauthorized, `{"ok":false,"error":"no"}`},
		{"server", http.StatusInternalServerError, `{"ok":false}`},
		{"rate-limit-unenforced", http.StatusTooManyRequests, `{"ok":false,"errorCode":"turn_rate_limited","enforced":false}`},
		{"not-json", http.StatusBadGateway, `nonsense`},
		{"missing-fields", http.StatusOK, `{"ok":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeTicketServer(t)
			f.status, f.body = tc.status, tc.body
			if tc.name == "missing-fields" {
				// A 200 that does not carry a usable ticket (WQt needs all of
				// ok/ticket/ticketId/runId).
				f.status, f.payload = 0, `{"ok":true}`
			}
			c := NewClient(nil)
			tk, err := c.TicketFor(context.Background(), f.req("acc1"))
			if err != nil {
				t.Fatalf("an unenforced failure must not fail the turn: %v", err)
			}
			if tk != nil {
				t.Fatalf("expected no ticket, got %+v", tk)
			}
		})
	}
}

// Uses are counted against the profile budget, and the count is advisory: the
// client never refuses a call on reaching it (the server does).
func TestTicketCountsUsesAgainstBudget(t *testing.T) {
	f := newFakeTicketServer(t)
	c := NewClient(nil)
	ctx := context.Background()

	if _, err := c.TicketFor(ctx, f.req("acc1")); err != nil {
		t.Fatal(err)
	}
	if got := c.UseTicket("acc1"); got != "tk_tt_1" {
		t.Fatalf("UseTicket = %q", got)
	}
	if got := c.UseTicket("acc1"); got != "tk_tt_1" {
		t.Fatalf("UseTicket = %q", got)
	}
	tk, ok := c.TicketStatus("acc1")
	if !ok {
		t.Fatal("ticket vanished")
	}
	if tk.Uses() != 2 || tk.Remaining() != ticketBudgetStandard-2 {
		t.Fatalf("uses/remaining = %d/%d", tk.Uses(), tk.Remaining())
	}
}

// Concurrent first calls collapse onto one ticket request.
func TestTicketIssueIsSingleFlight(t *testing.T) {
	f := newFakeTicketServer(t)
	c := NewClient(nil)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tk, err := c.TicketFor(ctx, f.req("acc1")); err != nil || tk == nil {
				t.Errorf("TicketFor: %v %+v", err, tk)
			}
		}()
	}
	wg.Wait()
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("8 concurrent calls issued %d tickets, want 1", got)
	}
}

// A ticket that has expired and cannot be renewed is not handed to a model
// call.
func TestTicketExpiredIsNotReused(t *testing.T) {
	f := newFakeTicketServer(t)
	c := NewClient(nil)
	ctx := context.Background()

	if _, err := c.TicketFor(ctx, f.req("acc1")); err != nil {
		t.Fatal(err)
	}
	// Run out the clock.
	now := time.Now().Add(10 * time.Minute)
	c.Now = func() time.Time { return now }

	f.status = http.StatusInternalServerError
	f.body = `{"ok":false}`
	tk, err := c.TicketFor(ctx, f.req("acc1"))
	if err != nil {
		t.Fatalf("unenforced failure: %v", err)
	}
	if tk != nil {
		t.Fatalf("an expired ticket must not be reused: %+v", tk)
	}
}

// A ticketed model call presents the ticket, and a complete envelope does not
// claim delta mode.
func TestCallModelPresentsTicketWithoutDeltaClaim(t *testing.T) {
	f := newFakeModelCall(t)
	c := NewClient(nil)
	tk := &Ticket{Token: "tk_fixture", TicketID: "tt_1", RunID: "cli-turn-1-abc",
		ExpiresAt: time.Now().Add(time.Minute), Profile: TicketProfileStandard}

	_, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "fixture", Body: []byte(`{"request":{}}`), Ticket: tk,
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("CallModel: %v", err)
	}
	hdr := f.lastHdr.Load().(map[string]string)
	if got := hdr[http.CanonicalHeaderKey(TicketHeader)]; got != "tk_fixture" {
		t.Fatalf("%s = %q", http.CanonicalHeaderKey(TicketHeader), got)
	}
	if got := hdr["X-Perch-Call-Delta"]; got != "" {
		t.Fatalf("x-perch-call-delta must not accompany a complete envelope, got %q", got)
	}
}

// Without a ticket the model call proceeds, which is the client's own
// degradation for an unenforced failure.
func TestCallModelWithoutTicketStillCalls(t *testing.T) {
	f := newFakeModelCall(t)
	c := NewClient(nil)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "fixture", Body: []byte(`{"request":{}}`),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("CallModel: %v", err)
	}
	hdr := f.lastHdr.Load().(map[string]string)
	if got := hdr[http.CanonicalHeaderKey(TicketHeader)]; got != "" {
		t.Fatalf("%s = %q, want none", http.CanonicalHeaderKey(TicketHeader), got)
	}
}

// TicketURL accepts a base that already carries the path, as bqr does.
func TestTicketURL(t *testing.T) {
	if got := TicketURL("https://app.perchai.app/"); got != "https://app.perchai.app/api/perch-terminal/turn-ticket" {
		t.Fatalf("TicketURL = %q", got)
	}
	full := "https://app.perchai.app/api/perch-terminal/turn-ticket"
	if got := TicketURL(full); got != full {
		t.Fatalf("TicketURL doubled the path: %q", got)
	}
	if got := TicketURL("  "); got != "" {
		t.Fatalf("TicketURL(empty) = %q", got)
	}
}

// A turn-rate-limit code is classified as terminal, never retryable: rotating
// accounts after the quota gate is exactly the behaviour this avoids.
func TestTurnLimitCodeIsTerminal(t *testing.T) {
	e := refusal(http.StatusTooManyRequests, []byte(`{"errorCode":"turn_rate_limited","error":"limit"}`))
	if e.Retryable {
		t.Fatal("turn_rate_limited must not be retryable")
	}
	if e.Code != "turn_rate_limited" {
		t.Fatalf("code = %q", e.Code)
	}
}

// asTicketError is errors.As for *TicketError, kept local so the test does not
// depend on the error's wrapping depth.
func asTicketError(err error, target **TicketError) bool {
	for err != nil {
		if te, ok := err.(*TicketError); ok {
			*target = te
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

var _ = strings.TrimSpace
