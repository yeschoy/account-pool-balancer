// Package perch — the model-call side of Perch.
//
// Perch's hosted models are not reached through an OpenAI-compatible API: the
// client posts one envelope to /api/perch-terminal/model-call and reads back
// Perch's own event stream. This file is that transport; translate.go turns an
// OpenAI request into the envelope and the stream back into OpenAI chunks.
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
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ModelCallPath is where a hosted-model call goes.
const ModelCallPath = "/api/perch-terminal/model-call"

// Event is one frame of Perch's stream. Only the fields this program uses are
// named; the rest ride in Raw for callers that want them.
type Event struct {
	Type string `json:"type"`
	// Text is the delta's text (answer_delta / reasoning_delta).
	Text string `json:"text"`
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
	InputTokens          int64 `json:"inputTokens"`
	OutputTokens         int64 `json:"outputTokens"`
	CacheReadInputTokens int64 `json:"cacheReadInputTokens"`
	TotalTokens          int64 `json:"totalTokens"`
	ContextInputTokens   int64 `json:"contextInputTokens"`
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
	// Token is the account's Supabase access token.
	Token string
	// Body is the envelope built by BuildRequestBody.
	Body []byte
	// ProxyURL routes this account's call through a proxy ("" = direct).
	ProxyURL string
}

// CallResult is what one successful call settled on.
type CallResult struct {
	Done  Event
	Text  string
	Calls []RawToolCall
	Usage *Usage
	Model string
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
	base := strings.TrimRight(strings.TrimSpace(call.Base), "/")
	if base == "" {
		base = AppURL
	}
	attempts := 0
	sawEvent := false
	for {
		res, err := c.callOnce(ctx, base+ModelCallPath, call, timeout, onEvent, &sawEvent)
		if err == nil {
			return res, nil
		}
		var me *ModelError
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

func (c *Client) callOnce(ctx context.Context, url string, call ModelCall, timeout time.Duration, onEvent func(Event) error, sawEvent *bool) (CallResult, error) {
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, url, bytes.NewReader(call.Body))
	if err != nil {
		return CallResult{}, &ModelError{Status: 502, Code: "transport_error", Err: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+call.Token)
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("x-perch-locale", "en")
	// held-context delta mode: the CLI sets it, and it is what makes
	// reasoning/answer deltas incremental instead of replayed.
	req.Header.Set("x-perch-call-delta", "1")

	httpc, cerr := c.clientFor(call.ProxyURL)
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
		return finish(ev, []Event{ev}, nil)
	}

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	var events []Event
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
			return finish(ev, events, nil)
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
	out := CallResult{Done: done, Usage: done.Usage, Model: done.Model}
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

// clientFor returns the HTTP client for a call: a cached per-proxy client, or
// the shared direct one. An unparsable proxy URL is an error rather than a
// silent direct connection.
func (c *Client) clientFor(proxyURL string) (*http.Client, error) {
	if proxyURL == "" {
		return c.HTTP, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.proxies == nil {
		c.proxies = map[string]*http.Client{}
	}
	if hc, ok := c.proxies[proxyURL]; ok {
		return hc, nil
	}
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid proxy URL")
	}
	t := &http.Transport{
		Proxy:                 http.ProxyURL(u),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	hc := &http.Client{Transport: t}
	c.proxies[proxyURL] = hc
	return hc, nil
}

// isCapacityCode reports Perch's "I am full" answer, which is not this
// account's fault and not something a retry can fix.
func isCapacityCode(code string) bool {
	return strings.EqualFold(strings.TrimSpace(code), "capacity_reached")
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
