// Package perch talks to Perch (chat.perchai.app) on behalf of one account at
// a time: it renews the account's Supabase session, reads the allowance the
// Settings → Usage page shows, learns the app's sign-in config and lists the
// hosted models.
//
// Everything here is stateless apart from the HTTP client it is handed: the
// caller owns the session, the caller decides when a renewal is due, and no
// function touches another account's credentials. That is what keeps two
// accounts refreshing at the same time from mixing their tokens up.
package perch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// ChatURL is the consumer chat product, where a sign-in is started.
	ChatURL = "https://chat.perchai.app"
	// AppURL is where model-call lives and what a chat.perchai.app sign-in
	// is answered with.
	AppURL = "https://app.perchai.app"
	// DefaultSupabaseURL is the project the published CLI is built against.
	// An imported account carries its own auth_context.supabase_url, which
	// always wins.
	DefaultSupabaseURL = "https://zlfuvsfjtgsdtqcaykia.supabase.co"
	// DefaultSupabaseKey is the publishable key of that project. GoTrue
	// requires an apikey header on every /auth/v1 call, including the
	// refresh; the account JSON does not carry one, so this is the default
	// and Analytics.SupabaseAnonKey overrides it.
	DefaultSupabaseKey = "sb_publishable_w4R_pNkUpygBFIljZCoOlA_67ACg0FO"
	// CLIVersion is what our User-Agent claims.
	CLIVersion = "2.4.116"
	// UserAgent is the CLI's own.
	UserAgent = "perchai-cli/" + CLIVersion
	// FreshMargin is the CLI's own idea of a session that is still good: 90 s.
	FreshMargin = 90 * time.Second
	// PTPerUSD is Perch's unit: one PT is a thousandth of a dollar of
	// recorded model spend, rounded up (20,000 PT = up to US$20).
	PTPerUSD = 1000
)

// ErrorKind classifies a failure so the caller can decide whether a session
// is dead, a token is spent, or the network merely hiccuped.
type ErrorKind string

const (
	// KindAuth: Perch refused the credentials (401/403). A refresh answering
	// this means the refresh token is gone for good.
	KindAuth ErrorKind = "auth"
	// KindRateLimited: 429, worth retrying later.
	KindRateLimited ErrorKind = "rate_limited"
	// KindTransient: 5xx, 408, 425, a timeout or a transport error. Never
	// evidence that the account is dead.
	KindTransient ErrorKind = "transient"
	// KindNotFound: the endpoint or RPC is not there (404) — a wrong
	// Supabase URL rather than a dead account.
	KindNotFound ErrorKind = "not_found"
	// KindRejected: the request itself is wrong (400/422).
	KindRejected ErrorKind = "rejected"
	// KindUnknown: anything else.
	KindUnknown ErrorKind = "unknown"
)

// Error is what every call in this package returns.
type Error struct {
	Kind       ErrorKind
	Status     int
	Code       string // GoTrue's error_code, or Perch's errorCode
	Message    string
	RetryAfter time.Duration
	Err        error
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	if msg == "" {
		msg = "request failed"
	}
	if e.Status > 0 {
		return fmt.Sprintf("%s (%d)", msg, e.Status)
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// Retryable reports whether waiting and trying again could help.
func (e *Error) Retryable() bool {
	return e.Kind == KindTransient || e.Kind == KindRateLimited
}

// Authy reports whether the failure says the credentials are refused.
func (e *Error) Authy() bool { return e.Kind == KindAuth }

// KindOf reports a failure's kind (KindUnknown for nil or foreign errors).
func KindOf(err error) ErrorKind {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Kind
	}
	if err == nil {
		return ""
	}
	return KindUnknown
}

// classify turns an HTTP status and body into an Error.
func classify(status int, body []byte, header http.Header) *Error {
	e := &Error{Status: status}
	var js map[string]any
	if json.Unmarshal(body, &js) == nil {
		e.Code = first(str(js["error_code"]), str(js["errorCode"]), str(js["code"]))
		// GoTrue names the failure in `error` ("invalid_grant") and explains
		// it in `error_description`; Perch uses `errorCode`/`error`. Every
		// spelling is kept in Raw so the refusal test can look at all of it.
		e.Message = first(str(js["msg"]), str(js["error_description"]), str(js["error"]),
			str(js["message"]), str(js["errorMsg"]))
		if e.Code == "" && str(js["error"]) != "" && !strings.Contains(str(js["error"]), " ") {
			e.Code = str(js["error"])
		}
	}
	if h := header.Get("x-sb-error-code"); h != "" && !strings.EqualFold(h, "validation_failed") {
		e.Code = first(e.Code, h)
	}
	if e.Message == "" {
		e.Message = strings.TrimSpace(string(body))
		if len(e.Message) > 240 {
			e.Message = e.Message[:240]
		}
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Kind = KindAuth
	case status == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
	case status == http.StatusNotFound:
		e.Kind = KindNotFound
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		// GoTrue refuses a spent or unknown refresh token with 400
		// invalid_grant — that is a dead credential, not a malformed request,
		// and the distinction is the whole "needs signing in again" verdict
		if isGrantRefusal(e.Code, e.Message) {
			e.Kind = KindAuth
		} else {
			e.Kind = KindRejected
		}
	case status >= 500 || status == http.StatusRequestTimeout || status == http.StatusTooEarly:
		e.Kind = KindTransient
	default:
		e.Kind = KindUnknown
	}
	e.RetryAfter = retryAfter(header)
	return e
}

// isGrantRefusal reports a GoTrue answer that says the refresh token itself
// is no longer valid.
func isGrantRefusal(code, message string) bool {
	why := strings.ToLower(code + " " + message)
	for _, needle := range []string{
		"invalid_grant", "already_used", "refresh_token_not_found", "refresh token not found",
		// what Supabase really answers for a spent/unknown token:
		// 400 validation_failed "Refresh token is not valid"
		"refresh token is not valid", "refresh token not valid", "invalid refresh token",
	} {
		if strings.Contains(why, needle) {
			return true
		}
	}
	return false
}

func retryAfter(h http.Header) time.Duration {
	raw := strings.TrimSpace(h.Get("Retry-After"))
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(raw); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

func first(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// num reads a JSON number that may also arrive as a string.
func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

// Client performs Perch's HTTP calls. It holds no per-account state.
type Client struct {
	HTTP *http.Client
	// Now is the clock (tests set it).
	Now func() time.Time

	mu      sync.Mutex
	proxies map[string]*http.Client // per proxy URL, model calls only
}

// NewClient returns a client with a transport sized for a pool of accounts
// refreshing at the same time.
func NewClient(hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{
			Timeout: 45 * time.Second,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          512,
				MaxIdleConnsPerHost:   64,
				MaxConnsPerHost:       64,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
			},
		}
	}
	return &Client{HTTP: hc, Now: time.Now}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// do sends one request and decodes a JSON answer into out (which may be nil).
// limit caps the answer's size so a captive portal page can't flood memory.
func (c *Client) do(ctx context.Context, req *http.Request, out any) error {
	res, err := c.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return &Error{Kind: KindTransient, Message: "request cancelled: " + ctx.Err().Error(), Err: err}
		}
		var ne net.Error
		msg := err.Error()
		if errors.As(err, &ne) && ne.Timeout() {
			msg = "request timed out"
		}
		return &Error{Kind: KindTransient, Message: msg, Err: err}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return &Error{Kind: KindTransient, Message: "reading the reply failed: " + err.Error(), Err: err}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return classify(res.StatusCode, body, res.Header)
	}
	if out == nil {
		return nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &Error{Status: res.StatusCode, Kind: KindUnknown, Message: "the reply is not JSON: " + err.Error(), Err: err}
	}
	return nil
}

// TrimBase normalises a Supabase/app URL: no trailing slash, and an /auth/v1
// or /rest/v1 suffix the caller may have pasted is removed.
func TrimBase(raw string) string {
	s := strings.TrimRight(strings.TrimSpace(raw), "/")
	for _, suffix := range []string{"/auth/v1", "/rest/v1"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return strings.TrimRight(s, "/")
}
