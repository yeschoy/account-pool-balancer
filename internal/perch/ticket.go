package perch

// Turn tickets: the per-turn admission ticket perchai-cli obtains before its
// first model call.
//
// The published client does not simply POST a model call. `perch run` first
// asks Perch for a ticket (Q1t -> ZQe -> wqr -> qQt), keeps it in process-wide
// state, and then attaches it to every model call of that turn as
// `x-perch-turn-ticket`. The ticket is metered: the client counts each use
// (`callsSent`) against a per-profile budget, expires it, and renews it shortly
// before expiry. See .analysis/perch-turn-ticket-flow.md for the byte-level
// trace this file reproduces.
//
// Three properties of the real client shape this implementation:
//
//   - A ticket is bound to a run id. The CLI takes its runId from the ticket
//     response and reuses it for every model call of the turn; it never mints
//     an unrelated run id next to a ticket. Sending a fresh random runId with a
//     ticket is a combination the real client never produces.
//   - Failure is graded by `enforced`. When the server marks the answer
//     enforced, a missing ticket fails the turn. When it does not, the client
//     silently degrades to "no ticket" and calls the model anyway. A client
//     that only implements the happy path breaks the day the server flips that
//     flag, so both outcomes are modelled here.
//   - The ticket budget is a server-side gate, not a client-side one. The real
//     client counts callsSent but never refuses a call on reaching the budget;
//     the server answers 429 turn_rate_limited. This package does the same: it
//     counts, and lets the server decide.
//
// What a ticket does NOT do is authorise a use. It reproduces the sequence the
// published client performs so that a pool request is not distinguishable by
// its shape alone; it does not turn a pooled call into a permitted one.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TurnTicketPath is the ticket endpoint (bqr's suffix).
const TurnTicketPath = "/api/perch-terminal/turn-ticket"

// TicketSurfaceCLI is the surface a CLI turn asks its ticket for (wqr passes
// e.surface straight through; the CLI client is constructed with "cli").
const TicketSurfaceCLI = "cli"

// Ticket profiles and their per-ticket call budgets (YQe). The published
// client picks `flock` for prompts that match its heavyweight grammar and
// `standard` otherwise; this package only ever asks for `standard`, because it
// has no equivalent of that grammar and must not claim a budget it does not
// need.
const (
	TicketProfileStandard = "standard"
	TicketProfileFlock    = "flock"

	ticketBudgetStandard = 40
	ticketBudgetFlock    = 250
)

// TicketTiming are the published client's two ticket clocks: the issue request
// timeout (vqr) and the renewal lead time ($Qt).
const (
	TicketIssueTimeout = 8 * time.Second
	TicketRenewLead    = 30 * time.Second
	// ticketDefaultTTL is what WQt assumes when the response carries no
	// usable expiresAt: five minutes.
	ticketDefaultTTL = 5 * time.Minute
)

// Ticket error codes the client reserves for ticket trouble ($ke / g2).
const (
	TicketCodeRateLimited = "turn_rate_limited"
	TicketCodeUnavailable = "turn_ticket_unavailable"
)

// TicketError is a refused or unavailable ticket. It is separate from
// ModelError because it is decided before any model call is made, and because
// its two codes mean different things to the caller: a rate limit is a
// statement about this account's allowance, an unavailable ticket is a
// statement about whether the turn may start at all.
type TicketError struct {
	// Code is TicketCodeRateLimited or TicketCodeUnavailable.
	Code string
	// Message is the server's own text when it sent one.
	Message string
	// Status is the HTTP status of the ticket request, when there was one.
	Status int
	// RetryAfter is the server's Retry-After, when it sent one.
	RetryAfter time.Duration
	// Enforced records whether the server said this failure must stop the
	// turn. Only an enforced failure is an error; see the package comment.
	Enforced bool
}

func (e *TicketError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		switch e.Code {
		case TicketCodeRateLimited:
			msg = "your plan's Perch turn rate limit has been reached"
		case TicketCodeUnavailable:
			msg = "Perch could not start a secure hosted-model turn"
		default:
			msg = "the turn ticket was refused"
		}
	}
	if e.Status > 0 {
		return fmt.Sprintf("%s (%d)", msg, e.Status)
	}
	return msg
}

// IsRateLimited reports an enforced turn-rate-limit verdict.
func (e *TicketError) IsRateLimited() bool { return e.Code == TicketCodeRateLimited }

// Ticket is one issued admission ticket.
//
// The fields mirror the client's ticket object exactly, including callsSent,
// because the budget is consumed by using the ticket, not by renewing it.
type Ticket struct {
	// Token is the x-perch-turn-ticket header value.
	Token string
	// TicketID identifies the ticket when renewing (ticketId).
	TicketID string
	// RunID is the run this ticket admits. Every model call of the turn uses
	// it; the CLI takes its runId from here rather than minting one.
	RunID string
	// ExpiresAt is when the ticket stops being usable.
	ExpiresAt time.Time
	// Profile is the budget profile the server resolved ("standard"/"flock").
	// The response's own profile wins over the requested one, as in WQt.
	Profile string
	// Enforced means an unusable ticket must fail the turn.
	Enforced bool

	// callsSent is how many model calls this ticket has already been attached
	// to. The real client increments it on every use and never checks it.
	callsSent int
}

// Budget is the ticket's call allowance for its profile.
func (t Ticket) Budget() int {
	switch t.Profile {
	case TicketProfileFlock:
		return ticketBudgetFlock
	default:
		return ticketBudgetStandard
	}
}

// Remaining is how many calls the ticket claims to have left. It is what
// perchai-cli's QQt reports, and like QQt it is advisory: the server is the
// authority on whether a call is admitted.
func (t Ticket) Remaining() int {
	if n := t.Budget() - t.callsSent; n > 0 {
		return n
	}
	return 0
}

// Uses is how many model calls the ticket has been attached to.
func (t Ticket) Uses() int { return t.callsSent }

// ticketJSON is the ticket endpoint's answer. ok/ticket/ticketId/runId are all
// required for a usable ticket (WQt returns null when any is missing).
type ticketJSON struct {
	OK        bool   `json:"ok"`
	Ticket    string `json:"ticket"`
	TicketID  string `json:"ticketId"`
	RunID     string `json:"runId"`
	ExpiresAt string `json:"expiresAt"`
	Profile   string `json:"profile"`
	Enforced  bool   `json:"enforced"`
	Error     string `json:"error"`
	ErrorCode string `json:"errorCode"`
}

// ticketIssueBody is the first request: {surface, profile}.
type ticketIssueBody struct {
	Surface string `json:"surface"`
	Profile string `json:"profile"`
}

// ticketRenewBody is the renewal: {renew:true, ticketId}. It carries no
// surface or profile — the real client's GQt sends exactly these two fields.
type ticketRenewBody struct {
	Renew    bool   `json:"renew"`
	TicketID string `json:"ticketId"`
}

// TicketRequest is what a caller must supply to obtain a ticket. It is
// per account: the token is that account's, and the ticket is cached against
// Key so two accounts never share one budget.
type TicketRequest struct {
	// Key scopes the cache. Use the account id.
	Key string
	// Base is the app host the ticket is requested from (AppURL).
	Base string
	// Token is the account's Supabase access token.
	Token string
	// Profile is the budget profile to ask for; empty means standard.
	Profile string
	// ProxyURL routes the request like the model call it precedes ("" = the
	// client's default transport).
	ProxyURL string
	// UserAgent and Locale are the CLI identity headers this request
	// presents, resolved the same way ModelCall resolves them.
	UserAgent string
	Locale    string
}

// TicketURL resolves the ticket endpoint for an app URL (bqr).
func TicketURL(appURL string) string {
	b := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if b == "" {
		return ""
	}
	if strings.HasSuffix(b, TurnTicketPath) {
		return b
	}
	return b + TurnTicketPath
}

// ticketCache holds one issued ticket per key (account). It is deliberately
// small: a ticket is cheap to re-obtain and worthless once expired, so there
// is no reason to keep history.
type ticketCache struct {
	mu   sync.Mutex
	byK  map[string]*Ticket
	inst map[string]*ticketFlight // in-flight issue/renew per key
}

// ticketFlight collapses concurrent ticket work for one key onto a single
// request. Two model calls starting at once must not spend two tickets, in the
// same way the refresh path must not spend a refresh token twice.
type ticketFlight struct {
	done chan struct{}
	tk   *Ticket
	err  error
}

func (c *ticketCache) lock() *ticketCache {
	if c.byK == nil {
		c.byK = map[string]*Ticket{}
	}
	if c.inst == nil {
		c.inst = map[string]*ticketFlight{}
	}
	return c
}

// peek returns the cached ticket without contacting the server.
func (c *ticketCache) peek(key string) (Ticket, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tk, ok := c.byK[key]
	if !ok {
		return Ticket{}, false
	}
	return *tk, true
}

// drop forgets a key's ticket, used when the server rejects it outright.
func (c *ticketCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.byK, key)
}

// TicketFor returns a usable ticket for one account, issuing or renewing it as
// the published client would.
//
// The result follows the client's grading exactly:
//
//   - a live ticket with more than TicketRenewLead of life left is returned
//     as-is;
//   - a ticket inside the lead window is renewed, and the renewal replaces it
//     only when the server returned the same ticket id (LQt's ticketId check);
//   - a ticket that could not be renewed is still returned while it has not
//     expired, because that is what LQt does;
//   - a ticket that is gone, could not be issued, and was enforced, is a
//     *TicketError; the same failure un-enforced is (nil, nil) — "continue
//     without a ticket", which is the client's own degradation.
//
// A nil ticket with a nil error therefore means "make the call unticketed",
// not "something broke".
func (c *Client) TicketFor(ctx context.Context, req TicketRequest) (*Ticket, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		// Without a key there is nowhere to keep a ticket, and a fresh one per
		// call would exhaust the server's budget on every request. Callers in
		// this package always pass the account id.
		return nil, nil
	}

	// Fast path: a ticket that is neither close to expiry nor spent needs no
	// server contact at all.
	if tk, ok := c.peekTicket(key); ok && c.now().Before(tk.ExpiresAt.Add(-TicketRenewLead)) {
		return &tk, nil
	}

	// Slow path: collapse concurrent work for this account onto one request, so
	// two model calls starting together do not spend two tickets.
	c.tickets.mu.Lock()
	c.tickets.lock()
	if fl, busy := c.tickets.inst[key]; busy {
		c.tickets.mu.Unlock()
		select {
		case <-fl.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if fl.err != nil {
			return nil, fl.err
		}
		if fl.tk == nil {
			return nil, nil
		}
		out := *fl.tk
		return &out, nil
	}
	fl := &ticketFlight{done: make(chan struct{})}
	c.tickets.inst[key] = fl
	c.tickets.mu.Unlock()

	tk, err := c.acquireTicket(ctx, key, req)
	c.tickets.mu.Lock()
	delete(c.tickets.inst, key)
	if tk != nil {
		c.tickets.byK[key] = tk
	} else {
		delete(c.tickets.byK, key)
	}
	c.tickets.mu.Unlock()

	fl.tk, fl.err = tk, err
	close(fl.done)

	if err != nil {
		return nil, err
	}
	if tk == nil {
		return nil, nil
	}
	out := *tk
	return &out, nil
}

// acquireTicket gets a ticket from the server, renewing the cached one when it
// is renewable and still unexpired. It returns (nil, nil) for a failure the
// published client degrades to "no ticket", and a *TicketError only for an
// enforced one.
func (c *Client) acquireTicket(ctx context.Context, key string, req TicketRequest) (*Ticket, error) {
	prev, had := c.peekTicket(key)
	now := c.now()

	if had && prev.TicketID != "" && now.Before(prev.ExpiresAt) {
		tk, err := c.renewTicket(ctx, req, prev)
		if err == nil && tk != nil {
			return tk, nil
		}
		if err != nil && !errors.Is(err, errTicketSoft) {
			// Enforced: the turn cannot start. A rate limit is about this
			// account's allowance; an unavailable ticket is about the gate.
			return nil, err
		}
		// The renewal did not produce a ticket. LQt keeps using the old one
		// while it has not expired, and only falls through to a fresh issue
		// once it is spent.
		if now.Before(prev.ExpiresAt) {
			old := prev
			return &old, nil
		}
	}
	tk, err := c.issueTicket(ctx, req)
	if errors.Is(err, errTicketSoft) {
		// Not enforced: the client's own answer is to continue without a
		// ticket, not to fail the turn.
		return nil, nil
	}
	return tk, err
}

// peekTicket reads one account's cached ticket under the cache lock.
func (c *Client) peekTicket(key string) (Ticket, bool) {
	c.tickets.mu.Lock()
	defer c.tickets.mu.Unlock()
	c.tickets.lock()
	tk, ok := c.tickets.byK[key]
	if !ok {
		return Ticket{}, false
	}
	return *tk, true
}

// errTicketSoft marks a failure that the client treats as "no ticket" rather
// than "the turn cannot start".
var errTicketSoft = errors.New("ticket unavailable without enforcement")

// issueTicket performs the first request: {surface, profile}.
func (c *Client) issueTicket(ctx context.Context, req TicketRequest) (*Ticket, error) {
	profile := strings.TrimSpace(req.Profile)
	if profile == "" {
		profile = TicketProfileStandard
	}
	return c.exchangeTicket(ctx, req, ticketIssueBody{Surface: TicketSurfaceCLI, Profile: profile})
}

// renewTicket performs the renewal: {renew:true, ticketId}.
func (c *Client) renewTicket(ctx context.Context, req TicketRequest, prev Ticket) (*Ticket, error) {
	body := ticketRenewBody{Renew: true, TicketID: prev.TicketID}
	return c.exchangeTicket(ctx, req, body)
}

// exchangeTicket posts one ticket request and grades the answer exactly as
// wqr does.
//
// The grading is the part that matters: only `enforced` turns a ticket failure
// into a turn failure. Everything else — a timeout, a network error, a
// malformed body, a non-enforced refusal — degrades to "no ticket", and the
// model call proceeds unticketed. Returning an error for those cases would make
// the pool stricter than the client it imitates, which is its own tell.
func (c *Client) exchangeTicket(ctx context.Context, req TicketRequest, body any) (*Ticket, error) {
	url := TicketURL(req.Base)
	if url == "" {
		return nil, errTicketSoft
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, errTicketSoft
	}

	// The issue request has its own 8 s budget, independent of the model
	// call's timeout: obtaining a ticket must not eat the turn's allowance.
	actx, cancel := context.WithTimeout(ctx, TicketIssueTimeout)
	defer cancel()

	hreq, err := http.NewRequestWithContext(actx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, errTicketSoft
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Authorization", "Bearer "+req.Token)
	hreq.Header.Set("User-Agent", req.ticketUA())
	hreq.Header.Set(LocaleHeader, req.ticketLocale())

	hc, cerr := c.clientFor(req.Key, req.ProxyURL)
	if cerr != nil {
		return nil, errTicketSoft
	}
	res, err := hc.Do(hreq)
	if err != nil {
		// Timeout or transport failure: the client returns null here too.
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errTicketSoft
	}
	defer res.Body.Close()

	rawBody, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var js ticketJSON
	_ = json.Unmarshal(rawBody, &js)

	// 429 is its own verdict: enforced + turn_rate_limited stops the turn,
	// anything else is a silent degradation.
	if res.StatusCode == http.StatusTooManyRequests {
		if js.Enforced && strings.EqualFold(js.ErrorCode, TicketCodeRateLimited) {
			return nil, &TicketError{
				Code:       TicketCodeRateLimited,
				Message:    js.Error,
				Status:     res.StatusCode,
				RetryAfter: retryAfter(res.Header),
				Enforced:   true,
			}
		}
		return nil, errTicketSoft
	}

	if res.StatusCode < 200 || res.StatusCode > 299 {
		if js.Enforced {
			return nil, &TicketError{
				Code:     TicketCodeUnavailable,
				Message:  firstNonEmptyStr(js.Error, strings.TrimSpace(string(rawBody))),
				Status:   res.StatusCode,
				Enforced: true,
			}
		}
		return nil, errTicketSoft
	}

	tk := ticketFromJSON(js, req)
	if tk == nil {
		return nil, errTicketSoft
	}
	return tk, nil
}

// ticketFromJSON is WQt: ok/ticket/ticketId/runId are all required, expiresAt
// falls back to five minutes, and the response's profile wins over the request's.
func ticketFromJSON(js ticketJSON, req TicketRequest) *Ticket {
	if !js.OK || strings.TrimSpace(js.Ticket) == "" || strings.TrimSpace(js.TicketID) == "" || strings.TrimSpace(js.RunID) == "" {
		return nil
	}
	exp := parseTicketExpiry(js.ExpiresAt)
	if exp.IsZero() {
		exp = time.Now().Add(ticketDefaultTTL)
	}
	profile := firstNonEmptyStr(js.Profile, req.Profile, TicketProfileStandard)
	return &Ticket{
		Token:     strings.TrimSpace(js.Ticket),
		TicketID:  strings.TrimSpace(js.TicketID),
		RunID:     strings.TrimSpace(js.RunID),
		ExpiresAt: exp,
		Profile:   profile,
		Enforced:  js.Enforced,
	}
}

// parseTicketExpiry reads the response's expiresAt, which the client parses as
// an ISO timestamp.
func parseTicketExpiry(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	// A bare epoch in milliseconds also parses, which keeps the fallback from
	// shortening the ticket's life when the server changes format.
	if ms, err := jsonNumberMillis(s); err == nil {
		return time.UnixMilli(ms)
	}
	return time.Time{}
}

func jsonNumberMillis(s string) (int64, error) {
	var f float64
	if err := json.Unmarshal([]byte(s), &f); err != nil {
		return 0, err
	}
	if f <= 0 {
		return 0, fmt.Errorf("not a timestamp")
	}
	return int64(f), nil
}

// UseTicket records one use of an account's ticket and returns the header the
// model call must carry (empty when the turn proceeds unticketed).
//
// It re-reads the cached ticket so the counter lives on the stored copy: the
// budget must be consumed once per call, even though a Ticket value is copied
// out to each caller.
func (c *Client) UseTicket(key string) string {
	k := strings.TrimSpace(key)
	if k == "" {
		return ""
	}
	c.tickets.mu.Lock()
	defer c.tickets.mu.Unlock()
	c.tickets.lock()
	tk, ok := c.tickets.byK[k]
	if !ok || tk.ExpiresAt.Before(c.now()) {
		return ""
	}
	tk.callsSent++
	return tk.Token
}

// DropTicket forgets an account's ticket, for a server answer that says the
// ticket itself is no good.
func (c *Client) DropTicket(key string) { c.tickets.drop(strings.TrimSpace(key)) }

// TicketStatus reports an account's cached ticket and whether one is held. It
// is for the console and the diagnostic log; it never contacts the server.
func (c *Client) TicketStatus(key string) (Ticket, bool) { return c.peekTicket(strings.TrimSpace(key)) }

// ticketUA / ticketLocale resolve the identity headers the ticket request
// presents, matching how ModelCall resolves them for the model call.
func (r TicketRequest) ticketUA() string {
	if s := strings.TrimSpace(r.UserAgent); s != "" {
		return s
	}
	return UserAgent
}

func (r TicketRequest) ticketLocale() string { return Locale(r.Locale) }
