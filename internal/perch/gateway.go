// Package perch — the model-call side of Perch.
//
// Perch's hosted models are not reached through an OpenAI-compatible API: the
// client posts one envelope and reads back Perch's own event stream. There are
// two such envelopes (see SurfaceFor):
//
//   - /api/perch-terminal/model-call, the CLI surface, which refuses every call
//     whose first system message does not begin with Perch's operator preamble
//     (403 perch_surface_required). Prefixing that text is the path the public
//     "perch2api"-style gateways take; it makes each request impersonate a
//     surface the plan does not include, and Perch's refusal text says repeated
//     attempts may suspend the account.
//   - /api/perchai/chat, the surface the web app itself posts messages to. It
//     needs no preamble and no ticket — the same session that gets 403 on
//     model-call gets 200 here — but it is thread-bound and states the served
//     model in the done frame as modelLabel.
//
// This file is the transport for both; translate.go turns an OpenAI request
// into whichever envelope the surface wants and the stream back into OpenAI
// chunks.
//
// Everything here is per-call. The caller supplies the access token for one
// account and nothing is shared between accounts, which is what keeps two
// accounts' calls from ever mixing.
package perch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
)

// ModelCallPath is where a hosted-model call goes on the CLI surface.
const ModelCallPath = "/api/perch-terminal/model-call"

// Surface names which envelope a call uses.
const (
	// SurfaceCLI: the CLI/model-call envelope. Requires the persona preamble.
	SurfaceCLI = "cli"
	// SurfaceWeb: the web app's own chat lane. No preamble, thread-bound.
	SurfaceWeb = "web"
)

// Web call paths (chat.perchai.app). ThreadsURL takes GET (list) and POST
// (create); ChatURL posts one message and answers the event stream.
const (
	WebThreadsPath = "/api/perchai/threads"
	WebChatPath    = "/api/perchai/chat"
)

// SurfaceFor normalises a configured surface name. Anything that is not
// explicitly the CLI surface is the web one: the web lane is the only one that
// works without redistributing Perch's persona text.
func SurfaceFor(name string) string {
	if strings.EqualFold(strings.TrimSpace(name), SurfaceCLI) {
		return SurfaceCLI
	}
	return SurfaceWeb
}

// Event is one frame of Perch's stream. Only the fields this program uses are
// named; the rest ride in Raw for callers that want them.
type Event struct {
	Type string `json:"type"`
	// Text is the delta's text (answer_delta / reasoning_delta).
	Text string `json:"text"`
	// ModelLabel names the model a frame was produced by. The web chat lane
	// reports the served model in its done frame (modelLabel = "MiniMax M3"),
	// where model-call reports `model` as the provider's id.
	ModelLabel string `json:"modelLabel,omitempty"`
	// Tier is the Roost tier the web chat lane resolved ("standard").
	Tier string `json:"tier,omitempty"`
	// MessageID / Citation-less extras ride along unused; keeping them named
	// costs nothing and makes a captured frame readable.
	MessageID string `json:"messageId,omitempty"`
	// ToolCalls carries tool_call_delta / tool_use_end payloads.
	ToolCalls []RawToolCall `json:"toolCalls"`
	// Reason explains a stream_restart ("mid_tool_call_failover").
	Reason string `json:"reason,omitempty"`

	// The done frame's summary.
	OK           *bool       `json:"ok,omitempty"`
	Content      []doneBlock `json:"content,omitempty"`
	FinishReason string      `json:"finishReason,omitempty"`
	Provider     string      `json:"provider,omitempty"`
	Model        string      `json:"model,omitempty"`
	DurationMs   int64       `json:"durationMs,omitempty"`
	Usage        *Usage      `json:"usage,omitempty"`
	ErrorCode    string      `json:"errorCode,omitempty"`
	Error        string      `json:"error,omitempty"`
}

type doneBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// RawToolCall is one tool call as Perch names it. A delta carries a partial
// argument string; a tool_use_end carries the whole call.
type RawToolCall struct {
	ID       string `json:"id"`
	Index    *int   `json:"index,omitempty"`
	Name     string `json:"name,omitempty"`
	Function *struct {
		Name      string `json:"name"`
		Arguments any    `json:"arguments"`
	} `json:"function,omitempty"`
	Arguments any `json:"arguments,omitempty"`
}

// CallName is the tool's name whichever shape the frame used.
func (c RawToolCall) CallName() string {
	if c.Function != nil && c.Function.Name != "" {
		return c.Function.Name
	}
	return c.Name
}

// CallArgs is the tool's arguments as a JSON string whichever shape the frame
// used (a delta may carry a bare fragment instead).
func (c RawToolCall) CallArgs() string {
	var v any
	switch {
	case c.Function != nil && c.Function.Arguments != nil:
		v = c.Function.Arguments
	case c.Arguments != nil:
		v = c.Arguments
	}
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// Usage is what a done frame reports, in Perch's token names.
type Usage struct {
	InputTokens                 int64 `json:"inputTokens"`
	OutputTokens                int64 `json:"outputTokens"`
	CacheReadInputTokens        int64 `json:"cacheReadInputTokens"`
	TotalTokens                 int64 `json:"totalTokens"`
	ContextInputTokens          int64 `json:"contextInputTokens"`
	CacheReadInputTokensPresent bool  `json:"-"`
	InputTokensPresent          bool  `json:"-"`
	OutputTokensPresent         bool  `json:"-"`
	DecodedFromJSON             bool  `json:"-"`
}

// UnmarshalJSON retains whether Perch reported token counts, including zero.
func (u *Usage) UnmarshalJSON(data []byte) error {
	type usageAlias Usage
	var decoded struct {
		*usageAlias
		InputTokens          *int64 `json:"inputTokens"`
		OutputTokens         *int64 `json:"outputTokens"`
		CacheReadInputTokens *int64 `json:"cacheReadInputTokens"`
	}
	var value usageAlias
	decoded.usageAlias = &value
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value.DecodedFromJSON = true
	if decoded.InputTokens != nil {
		value.InputTokens = *decoded.InputTokens
		value.InputTokensPresent = true
	}
	if decoded.OutputTokens != nil {
		value.OutputTokens = *decoded.OutputTokens
		value.OutputTokensPresent = true
	}
	if decoded.CacheReadInputTokens != nil {
		value.CacheReadInputTokens = *decoded.CacheReadInputTokens
		value.CacheReadInputTokensPresent = true
	}
	*u = Usage(value)
	return nil
}

// ModelError is a refusal or failure, already classified for the pool's rules.
type ModelError struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Err    string `json:"error"`
	// Limit is Perch's own plan/limit detail, passed through for the console.
	Limit json.RawMessage `json:"limit,omitempty"`
	// Auth: the access token was refused; the caller may renew and retry.
	Auth bool `json:"-"`
	// Retryable: a transient failure worth another attempt before any byte of
	// answer text was sent.
	Retryable bool `json:"-"`
}

func (e *ModelError) Error() string {
	if e.Code != "" {
		return e.Err + " (" + e.Code + ")"
	}
	return e.Err
}

// ModelCall is one request. The caller fills Base, Token and Body.
type ModelCall struct {
	// Base is the app host (https://app.perchai.app).
	Base string
	// WebBase is the chat host (https://chat.perchai.app) the web surface
	// uses. Only read when Surface is SurfaceWeb.
	WebBase string
	// Token is the account's Supabase access token.
	Token string
	// Body is the envelope built by BuildEnvelope (CLI) or BuildWebMessage
	// (web).
	Body []byte
	// Surface is SurfaceCLI or SurfaceWeb (see SurfaceFor).
	Surface string
	// ThreadID is the conversation a web-lane call joins. Empty means "open a
	// new thread" — the transport does that itself and reports it back in
	// CallResult.ThreadID so a tool loop can continue in the same thread.
	ThreadID string
	// ProxyURL routes this account's call through a proxy ("" = direct).
	ProxyURL string
	// UserAgent and Locale are the CLI identity this call presents. Empty
	// means the package defaults (UserAgent, DefaultLocale); see UAFor and
	// LocaleFor. They are per call because the console can change them and
	// because the web lane should not claim to be the CLI at all.
	UserAgent string
	Locale    string
	// AccountKey scopes the held-context delta to one account.
	//
	// A snapshot confirmed by account A says nothing about what account B
	// holds, and a failover moves the same conversation between them. Without
	// this scope the request would claim a prefix the new account never saw.
	// Empty means "no account scope": such a call never reuses a snapshot.
	AccountKey string
	// Ticket is the turn ticket this call presents, obtained from TicketFor.
	// Nil means the turn proceeds unticketed — which is what the published
	// client does when a ticket request fails without `enforced`. The web
	// lane never carries one.
	Ticket *Ticket
}

// UAFor resolves the User-Agent a call presents.
func (m ModelCall) UAFor() string {
	if s := strings.TrimSpace(m.UserAgent); s != "" {
		return s
	}
	return UserAgent
}

// LocaleFor resolves the x-perch-locale value a call presents.
func (m ModelCall) LocaleFor() string {
	if s := strings.TrimSpace(m.Locale); s != "" {
		return Locale(s)
	}
	return DefaultLocale
}

// CallResult is what one successful call settled on.
type CallResult struct {
	Done  Event
	Text  string
	Calls []RawToolCall
	Usage *Usage
	Model string
	// ThreadID is the web-lane conversation the turn belongs to (empty on the
	// CLI surface).
	ThreadID string
	// Reasoning is the reasoning text accumulated across the turn, when the
	// stream carried any.
	Reasoning string
}

// CallModel performs one model call, handing every non-terminal frame to
// onEvent as it arrives, and returns the terminal done frame.
//
// Retry policy follows what the reference gateways observed:
//   - 401: the credential is gone — returned as Auth, never retried here (the
//     caller renews the session and calls again, which is what keeps a rotated
//     refresh token from being spent twice).
//   - 403 (a plan refusal) and 429 (a turn or allowance limit): terminal. They
//     are the account's plan talking, not a flake.
//   - 5xx / 408 / 425 / a timeout: retried with backoff, but only while no
//     event has been delivered — once text has gone downstream, restarting
//     would duplicate it.
func (c *Client) CallModel(ctx context.Context, call ModelCall, timeout time.Duration, onEvent func(Event) error) (CallResult, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	// An empty surface is the CLI envelope: it is what every older caller and
	// captured envelope means.
	web := SurfaceFor(call.Surface) == SurfaceWeb
	base := strings.TrimRight(strings.TrimSpace(call.Base), "/")
	if base == "" {
		base = AppURL
	}
	url := base + ModelCallPath
	if web {
		threadID, err := c.ensureThread(ctx, &call)
		if err != nil {
			return CallResult{}, err
		}
		call.ThreadID = threadID
		// BuildWebMessage runs before this, so the body still says "no
		// thread": a message posted without one is refused upstream with
		// missing_thread.
		if call.Body, err = withThread(call.Body, threadID); err != nil {
			return CallResult{}, &ModelError{Status: 502, Code: "request", Err: err.Error()}
		}
		if b := strings.TrimRight(strings.TrimSpace(call.WebBase), "/"); b != "" {
			base = b
		} else {
			base = ChatURL
		}
		url = base + WebChatPath
	}
	// Held-context delta: if the server already holds this run's message
	// prefix, send only the new tail. body is what this attempt posts.
	//
	// plan is never nil, even when the request is whole: a whole request is
	// what teaches the cache how much the server now holds, which is what makes
	// the NEXT call of the run a delta.
	body := call.Body
	plan := &DeltaPlan{}
	if !web {
		var env Envelope
		if json.Unmarshal(call.Body, &env) == nil && env.RunID != "" {
			plan = c.PlanDelta(url, call.AccountKey, env.RunID, env)
			if plan.IsDelta {
				if deltaBody, derr := plan.Body(env); derr == nil {
					body = deltaBody
				} else {
					plan.IsDelta = false
				}
			}
		}
	}

	// Count what this attempt actually sends, before it goes out.
	if !web {
		var env Envelope
		if json.Unmarshal(call.Body, &env) == nil {
			c.RecordSending(plan, env)
		}
	}

	attempts := 0
	sawEvent := false
	for {
		res, err := c.callOnce(ctx, url, call, body, timeout, onEvent, &sawEvent, web, plan)
		if err == nil {
			c.SettleDelta(plan, true)
			return res, nil
		}
		var me *ModelError
		if plan.IsDelta && plan.Refused() {
			// The server does not hold the prefix we claimed. Forget it and
			// resend the whole request once — the published client's whole().
			c.RecordRefused()
			c.deltas.dropPrefix(plan.key, plan.digests)
			// The retry is a whole request: same run, no delta claim.
			plan.IsDelta = false
			body = call.Body
			res, err = c.callOnce(ctx, url, call, body, timeout, onEvent, &sawEvent, web, nil)
			if err == nil {
				return res, nil
			}
		} else {
			c.SettleDelta(plan, false)
			plan.IsDelta = false
		}
		retryable := errors.As(err, &me) && me.Retryable
		if !retryable || sawEvent || attempts >= 2 || ctx.Err() != nil {
			return res, err
		}
		attempts++
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(time.Duration(400<<attempts) * time.Millisecond):
		}
	}
}

func (c *Client) callOnce(ctx context.Context, url string, call ModelCall, body []byte, timeout time.Duration, onEvent func(Event) error, sawEvent *bool, web bool, plan *DeltaPlan) (CallResult, error) {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return CallResult{}, &ModelError{Status: 502, Code: "transport_error", Err: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if web {
		// The web app posts JSON and reads JSON back; only the CLI surface
		// streams. Promising an event stream on a request that does not stream
		// is a shape the browser app never sends.
		req.Header.Set("Accept", "application/json")
	} else {
		req.Header.Set("Accept", "text/event-stream")
	}
	req.Header.Set("Authorization", "Bearer "+call.Token)
	req.Header.Set("User-Agent", call.UAFor())
	if !web {
		// The locale header is the CLI's; the web lane is posted by the
		// browser app, which sends none.
		req.Header.Set(LocaleHeader, call.LocaleFor())
	}
	if web {
		// The web lane is posted by the browser app: same origin, same UA. No
		// call-delta header (that is the CLI's held-context mode) and no
		// ticket, which this surface neither needs nor checks.
		req.Header.Set("Origin", ChatURL)
		req.Header.Set("Referer", ChatURL+"/perchai")
	} else {
		if call.Ticket != nil && call.Ticket.Token != "" {
			// The CLI attaches the ticket to every model call of the turn.
			req.Header.Set(TicketHeader, call.Ticket.Token)
		}
		if plan != nil && plan.key != "" {
			// x-perch-call-delta is sent on EVERY model call, whole or delta,
			// which is what the published client does: perchai-cli sets it
			// unconditionally in its request builder (MYt's headers) and only
			// the BODY differs between the two modes. Measured against
			// perchai-cli@2.4.120: a whole request carries the header and the
			// server answers with x-perch-call-held-*, which is how the client
			// learns its first baseline.
			//
			// Withholding it on whole requests (the previous behaviour) meant
			// the server never reported what it held, SettleDelta never
			// confirmed a prefix, the cache stayed empty and every call went
			// out whole — confirmedPrefixes=0 against a wholeRequests count
			// that only grew.
			req.Header.Set(DeltaHeader, "1")
		}
	}

	httpc, cerr := c.clientFor(call.AccountKey, call.ProxyURL)
	if cerr != nil {
		return CallResult{}, &ModelError{Status: 502, Code: "bad_proxy", Err: cerr.Error()}
	}
	res, err := httpc.Do(req)
	if err != nil {
		if actx.Err() != nil && ctx.Err() == nil {
			return CallResult{}, &ModelError{Status: 504, Code: "timeout", Err: "Perch did not answer in time", Retryable: true}
		}
		return CallResult{}, &ModelError{Status: 502, Code: "transport_error", Err: err.Error(), Retryable: true}
	}
	defer res.Body.Close()

	// Record what the server says it holds, so a delta can be judged: a 409, or
	// a 400 that does not echo x-perch-call-delta, means it did not accept the
	// prefix and the caller must resend the whole request.
	if plan != nil {
		hdr := map[string]string{
			DeltaHeader:      res.Header.Get(DeltaHeader),
			HeldLengthHeader: res.Header.Get(HeldLengthHeader),
			HeldDigestHeader: res.Header.Get(HeldDigestHeader),
			HeldToolsHeader:  res.Header.Get(HeldToolsHeader),
		}
		plan.Observe(res.StatusCode, hdr)
	}

	if res.StatusCode < 200 || res.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return CallResult{}, refusal(res.StatusCode, body)
	}

	if strings.Contains(res.Header.Get("Content-Type"), "application/json") {
		// a proxy that answered in one piece instead of streaming
		var ev Event
		body, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
		if err := json.Unmarshal(body, &ev); err != nil {
			return CallResult{}, &ModelError{Status: 502, Code: "bad_upstream_body", Err: err.Error()}
		}
		*sawEvent = true
		out, err := finish(ev, []Event{ev}, nil)
		out.ThreadID = call.ThreadID
		return out, err
	}

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var events []Event
	// answers the stream may be prefixed or suffixed with junk whitespace or
	// an SSE comment; the first data line is where a frame begins
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue // keepalive comment
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue // a partial or unknown frame is not worth failing a turn over
		}
		*sawEvent = true
		events = append(events, ev)
		if ev.Type == "done" {
			if onEvent != nil {
				if err := onEvent(ev); err != nil {
					return CallResult{}, err
				}
			}
			out, err := finish(ev, events, nil)
			out.ThreadID = call.ThreadID
			return out, err
		}
		if onEvent != nil {
			if err := onEvent(ev); err != nil {
				return CallResult{}, err
			}
		}
	}
	if err := sc.Err(); err != nil {
		if actx.Err() != nil && ctx.Err() == nil {
			return CallResult{}, &ModelError{Status: 504, Code: "timeout", Err: "Perch's stream stalled", Retryable: !*sawEvent}
		}
		return CallResult{}, &ModelError{Status: 502, Code: "stream_error", Err: err.Error(), Retryable: !*sawEvent}
	}
	// a stream that ended without `done` never produced a complete answer
	return CallResult{}, &ModelError{Status: 502, Code: "incomplete_stream", Err: "Perch's stream ended without a done event", Retryable: !*sawEvent}
}

// finish reads the terminal frame into a result. A done frame whose ok is
// false is a failed call even though the HTTP status was 200.
func finish(done Event, events []Event, err error) (CallResult, error) {
	if err != nil {
		return CallResult{}, err
	}
	if done.OK != nil && !*done.OK {
		code := done.ErrorCode
		if code == "" {
			code = "model_call_failed"
		}
		return CallResult{}, &ModelError{Status: 502, Code: code, Err: firstNonEmptyStr(done.Error, "Perch reported a failed model call")}
	}
	out := CallResult{Done: done, Usage: done.Usage, Model: firstNonEmptyStr(done.Model, done.ModelLabel)}
	var reasoning strings.Builder
	for _, ev := range events {
		if ev.Type == "reasoning_delta" {
			reasoning.WriteString(ev.Text)
		}
	}
	out.Reasoning = reasoning.String()
	// The authoritative text is done.text; the deltas carried it too, so it is
	// used as a fallback rather than a second source.
	out.Text = done.Text
	if out.Text == "" {
		for _, b := range done.Content {
			if b.Type == "text" {
				out.Text += b.Text
			}
		}
	}
	out.Calls = done.ToolCalls
	// a stream_restart voids everything before it: the last run wins
	restart := -1
	for i, ev := range events {
		if ev.Type == "stream_restart" {
			restart = i
		}
	}
	if restart >= 0 && out.Text == "" {
		var b strings.Builder
		for _, ev := range events[restart+1:] {
			if ev.Type == "answer_delta" {
				b.WriteString(ev.Text)
			}
		}
		out.Text = b.String()
	}
	return out, nil
}

// refusal turns a non-2xx answer into a classified error.
func refusal(status int, body []byte) *ModelError {
	e := &ModelError{Status: status}
	var js struct {
		Error     string          `json:"error"`
		Msg       string          `json:"msg"`
		Message   string          `json:"message"`
		ErrorCode string          `json:"errorCode"`
		Code      any             `json:"code"`
		Limit     json.RawMessage `json:"limit"`
	}
	if json.Unmarshal(body, &js) == nil {
		e.Code = js.ErrorCode
		if e.Code == "" {
			if s, ok := js.Code.(string); ok {
				e.Code = s
			}
		}
		e.Err = firstNonEmptyStr(js.Error, js.Msg, js.Message)
		e.Limit = js.Limit
	}
	if e.Err == "" {
		e.Err = strings.TrimSpace(string(body))
		if len(e.Err) > 240 {
			e.Err = e.Err[:240]
		}
	}
	if e.Err == "" {
		e.Err = http.StatusText(status)
	}
	switch {
	case status == http.StatusUnauthorized:
		e.Auth = true
	case isTurnLimitCode(e.Code):
		// The turn ticket's own budget was exhausted. Perch states this about
		// the account's plan, not about a flake, and the published client
		// fails the turn here rather than retrying (its $ke). Retrying — here
		// or on another account — is exactly the rotation this code avoids.
		e.Retryable = false
	case status == http.StatusForbidden || status == http.StatusTooManyRequests:
		// plan limits and turn limits are the account's plan talking
	case isCapacityCode(e.Code):
		// Perch itself is full. Every account meets the same wall, so a retry
		// (here or on another account) only spends budget on a request that
		// cannot succeed yet.
	case status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooEarly:
		e.Retryable = true
	case status == http.StatusRequestEntityTooLarge:
		// a body too large for the plan will not shrink on a retry
	default:
		e.Retryable = true
	}
	return e
}

// clientFor returns the HTTP client for a call.
//
// The pool is keyed by account, not by egress address, and every transport it
// builds presents the measured Node handshake. Both halves matter for the same
// reason: the published client is one account in one process, so its TLS
// session, its HTTP/2 connection and its ClientHello belong to exactly one
// identity. Sharing any of them between accounts is a correlation the server
// gets for free.
//
// An empty account id keeps the old proxy-keyed behaviour so callers that do
// not scope a request still work; an unparsable proxy URL is an error rather
// than a silent direct connection.
func (c *Client) clientFor(accountID, proxyURL string) (*http.Client, error) {
	var proxy *url.URL
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil || u.Host == "" {
			return nil, errors.New("invalid proxy URL")
		}
		proxy = u
	}
	// No account and no proxy: the caller's own client (tests inject one).
	if accountID == "" && proxy == nil {
		return c.HTTP, nil
	}
	key := EgressKey(accountID, proxyURL)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proxies == nil {
		c.proxies = map[string]*http.Client{}
	}
	if hc, ok := c.proxies[key]; ok {
		return hc, nil
	}
	t := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	if proxy != nil {
		t.Proxy = http.ProxyURL(proxy)
	}
	// The fingerprint rides on DialTLSContext, whose contract is to return a
	// connection that has already completed its handshake. That is only
	// reachable on direct egress: behind a proxy the transport performs TLS
	// itself after its own CONNECT, so installing a hook here would hand back
	// an unfinished connection and break the route. Proxied calls therefore
	// keep Go's own handshake; see the note on dialFingerprinted.
	//
	// DISABLED: the measured Node ClientHello advertises ALPN as
	// "http/1.1, h2", but the server picks h2 anyway. DialTLSContext hands
	// back a connection that already negotiated h2, while *http.Transport
	// still writes an HTTP/1.1 request line into it, so every direct call
	// fails with "bogus greeting GET / HTTP/1.1" on the server and
	// "malformed HTTP response" (an h2 SETTINGS frame) on the client. That
	// failure surfaces as a TLS-looking transport error, then exhausts the
	// accounts and ends as a local pool_cooling_down 429.
	//
	// Restoring it needs the h2 side to agree, not a different ALPN string:
	// either carry the connection on http2.Transport, or let uTLS own ALPN
	// so ConnectionState reports the protocol Go then actually speaks.
	// isFingerprintingEnabled is the single switch for that work.
	if isFingerprintingEnabled() {
		if spec := c.fingerprintOr(NodeClientHelloSpec()); spec != nil && proxy == nil {
			t.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialFingerprinted(ctx, network, addr, spec)
			}
		}
	}
	hc := &http.Client{Transport: t}
	c.proxies[key] = hc
	return hc, nil
}

// EgressKey names one connection pool.
//
// The account id leads so two accounts never meet even when they share a proxy
// URL. An empty id degrades to the proxy-only key, which is what an unscoped
// caller means by "wherever this proxy goes".
func EgressKey(accountID, proxyURL string) string {
	proxyURL = strings.TrimSpace(proxyURL)
	if accountID == "" {
		return "proxy:" + proxyURL
	}
	return accountID + "\x00" + proxyURL
}

// isFingerprintingEnabled reports whether the uTLS ClientHello replaces Go's
// own handshake on direct egress.
//
// It is off while the ALPN/HTTP2 mismatch described in clientFor is open. The
// code below is kept intact rather than deleted: the ClientHello itself was
// measured correctly (1640 bytes, matching perchai-cli on Node v26), and only
// the protocol Go speaks afterwards is wrong, so restoring this is a
// transport change and not a re-measurement.
//
// Tests that pin the spec shape and the negotiated length call the helpers
// directly and are unaffected by this switch.
func isFingerprintingEnabled() bool { return false }

// fingerprintOr returns the ClientHello to present, defaulting to the measured
// Node handshake.
func (c *Client) fingerprintOr(fallback *utls.ClientHelloSpec) *utls.ClientHelloSpec {
	if c.fingerprint != nil {
		return c.fingerprint
	}
	return fallback
}

// dialFingerprinted opens a direct connection that presents spec as its
// ClientHello, completing the handshake before it returns.
//
// It is only wired up for direct egress. Behind a proxy the transport runs its
// own TLS after its own CONNECT tunnel, and a DialTLSContext that returned an
// unfinished connection would break that path outright — the request would be
// sent in the clear to a proxy expecting a tunnel. Reshaping the handshake
// inside a CONNECT would mean owning the tunnel here, which is a larger change
// than the fingerprint and is not attempted.
//
// Verification stays on: the certificate is the one the real host presents, and
// accepting it unchecked would trade a fingerprint for a silent MITM hole.
func dialFingerprinted(ctx context.Context, network, addr string, spec *utls.ClientHelloSpec) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	raw, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
	}
	u := utls.UClient(raw, &utls.Config{
		ServerName: host,
		NextProtos: []string{"http/1.1", "h2"},
		MinVersion: utls.VersionTLS12,
	}, utls.HelloCustom)
	if err := u.ApplyPreset(spec); err != nil {
		raw.Close()
		return nil, err
	}
	if err := u.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return u, nil
}

// isCapacityCode reports Perch's "I am full" answer, which is not this
// account's fault and not something a retry can fix.
func isCapacityCode(code string) bool {
	return strings.EqualFold(strings.TrimSpace(code), "capacity_reached")
}

// isTurnLimitCode reports the codes that mean "this account's turn allowance is
// spent": the ticket gate's own answer, and the pool's older spelling of it.
// They are a statement about the plan, so the turn ends instead of retrying.
func isTurnLimitCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case TicketCodeRateLimited, "usage_limit_reached":
		// TicketCodeRateLimited is "turn_rate_limited" (the CLI's $ke).
		return true
	}
	return false
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---- the web surface -----------------------------------------------------------

// ensureThread returns the thread a web call joins: the caller's, or a new one
// which call is updated to carry.
//
// The web lane is thread-bound — /api/perchai/chat refuses a message with
// "missing_thread" when it names none — so a call that arrives without a
// thread opens one first. A chat-completions request carries no conversation
// id, which is why this is done here rather than asked of the caller.
// withThread sets the thread id on an already-built web message. The message is
// built before a thread exists (a chat-completions request names none), so the
// field is filled in here rather than at build time.
func withThread(body []byte, threadID string) ([]byte, error) {
	var msg map[string]any
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, err
	}
	msg["threadId"] = threadID
	return json.Marshal(msg)
}

func (c *Client) ensureThread(ctx context.Context, call *ModelCall) (string, error) {
	if t := strings.TrimSpace(call.ThreadID); t != "" {
		return t, nil
	}
	base := strings.TrimRight(strings.TrimSpace(call.WebBase), "/")
	if base == "" {
		base = ChatURL
	}
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, base+WebThreadsPath, strings.NewReader("{}"))
	if err != nil {
		return "", &ModelError{Status: 502, Code: "transport_error", Err: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+call.Token)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Origin", ChatURL)
	req.Header.Set("Referer", ChatURL+"/perchai")
	httpc, cerr := c.clientFor(call.AccountKey, call.ProxyURL)
	if cerr != nil {
		return "", &ModelError{Status: 502, Code: "bad_proxy", Err: cerr.Error()}
	}
	res, err := httpc.Do(req)
	if err != nil {
		return "", &ModelError{Status: 502, Code: "transport_error", Err: err.Error(), Retryable: true}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", refusal(res.StatusCode, body)
	}
	var ans struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(body, &ans); err != nil || strings.TrimSpace(ans.Thread.ID) == "" {
		return "", &ModelError{Status: 502, Code: "thread_unavailable", Err: "Perch did not return a thread id"}
	}
	return strings.TrimSpace(ans.Thread.ID), nil
}
