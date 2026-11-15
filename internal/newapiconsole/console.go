// Package newapiconsole holds the one place that knows how to talk to a
// new-api console (/api/*) on behalf of a logged-in user.
//
// altapi.ai is a customized new-api fork. Its console needs THREE things on
// every request, and getting any of them wrong is a 401 that looks like a
// credential problem but is not:
//
//	Cookie: session=<...>        the login session (httpOnly, SameSite=Lax)
//	New-Api-User: <uid>          must equal the logged-in user id
//	                             (absent → "未提供 New-Api-User";
//	                              "-1"   → "与登录用户不匹配")
//
// The inference key (Authorization: Bearer) can NOT read a balance: with a key
// and no session, /api/user/self answers {"message":"access token 无效"}.
//
// Before this package existed the header was assembled in three places (the
// pool poller, the console quota read, the Python registrar) and two of them
// hard-coded "-1" and one normalized the cookie differently from the others.
// The rules below are shared so a new call site cannot re-introduce a variant.
package newapiconsole

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// UserAgent is the console's own browser shape; Cloudflare rules key on it.
const UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

// ErrNoSession is returned when no session cookie is stored: the caller must
// report "balance unknown", never zero.
var ErrNoSession = errors.New("no session cookie: the inference key cannot read a balance")

// ErrNoUID is returned when the session exists but the user id is unknown.
// new-api will not infer the id from the session, so the read is impossible
// until the id is known (the registrar writes it at login time).
var ErrNoUID = errors.New("no uid: /api/user/self requires New-Api-User to equal the logged-in user id")

// SessionValue normalizes a stored credential into the bare session value.
//
// An operator pastes whatever the browser shows: sometimes the value alone,
// sometimes a whole "session=…; other=…" string from the Network panel. Both
// are accepted; anything containing whitespace outside a cookie list is
// rejected rather than sent, because a malformed Cookie header fails closed
// with an opaque 401.
func SessionValue(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if strings.Contains(v, "session=") {
		for _, part := range strings.Split(v, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "session=") {
				return strings.TrimPrefix(part, "session=")
			}
		}
		return ""
	}
	if strings.ContainsAny(v, " \t\r\n") {
		return ""
	}
	return v
}

// CookieHeader turns a stored credential into the Cookie header value.
// Empty means "no session".
func CookieHeader(raw string) string {
	if v := SessionValue(raw); v != "" {
		return "session=" + v
	}
	return ""
}

// NormalizeUID trims a stored user id and rejects anything that is not a
// positive integer, so a typo cannot reach the wire as a header.
func NormalizeUID(raw string) string {
	// Reject any whitespace, including a trailing newline: a value pasted
	// from a terminal often carries one, and a newline inside a header value
	// is a header-injection vector. Trimming first would hide it.
	for _, r := range raw {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			return ""
		}
	}
	v := raw
	if v == "" {
		return ""
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return ""
		}
	}
	if strings.TrimLeft(v, "0") == "" {
		return "" // "0" and "000" are not user ids
	}
	return strings.TrimLeft(v, "0")
}

// Origin derives the console origin ("https://host") from an API base that
// ends in /v1.
func Origin(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return "https://altapi.ai"
	}
	return u.Scheme + "://" + u.Host
}

// Request is one console call: which credential, against which host.
type Request struct {
	// Origin is "https://altapi.ai" (see Origin).
	Origin string
	// Session is the stored session credential, any accepted shape.
	Session string
	// UID is the logged-in user id. Required for /api/user/self.
	UID string
	// Bearer is the inference key, used only for the /dashboard/billing/*
	// read that reports money already spent.
	Bearer string
	// ProxyURL is this account's egress. Empty means direct, with the
	// environment proxy ignored so the pool's per-account egress holds.
	ProxyURL string
}

// Client is a thin console client. It never retries: a 429 means shared
// per-IP throttling, and hammering makes it worse.
type Client struct {
	Req  Request
	HTTP *http.Client
}

// New builds a client with a transport that honours the account's egress.
func New(req Request) *Client {
	t := &http.Transport{
		MaxIdleConns:        4,
		IdleConnTimeout:     30 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	// Proxy nil (not ProxyFromEnvironment) is deliberate: the host may run a
	// Clash-style proxy, and an account pinned to one exit must not silently
	// use another.
	if p := strings.TrimSpace(req.ProxyURL); p != "" {
		if u, err := url.Parse(p); err == nil && u.Host != "" &&
			(u.Scheme == "http" || u.Scheme == "https") {
			t.Proxy = http.ProxyURL(u)
		}
	}
	return &Client{Req: req, HTTP: &http.Client{Transport: t, Timeout: 30 * time.Second}}
}

// header applies the credential rules in one place.
func (c *Client) header(h http.Header, withSession, withUID, withBearer bool) {
	h.Set("Accept", "application/json")
	h.Set("Referer", c.Req.Origin+"/console")
	h.Set("User-Agent", UserAgent)
	if withUID {
		if uid := NormalizeUID(c.Req.UID); uid != "" {
			h.Set("New-Api-User", uid)
		}
	}
	if withSession {
		if ck := CookieHeader(c.Req.Session); ck != "" {
			h.Set("Cookie", ck)
		}
	}
	if withBearer && strings.TrimSpace(c.Req.Bearer) != "" {
		h.Set("Authorization", "Bearer "+strings.TrimSpace(c.Req.Bearer))
	}
}

// Get issues one GET and reads at most 1 MiB.
func (c *Client) Get(ctx context.Context, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Req.Origin+path, nil)
	if err != nil {
		return 0, nil, err
	}
	// The session and the uid travel together: a uid without a session is
	// meaningless, and a session without a uid is refused by /api/user/self.
	c.header(req.Header, true, true, true)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

// Status is the public /api/status payload (no credential needed).
type Status struct {
	QuotaPerUnit      float64 `json:"quota_per_unit"`
	TurnstileCheck    bool    `json:"turnstile_check"`
	EmailVerification bool    `json:"email_verification"`
	QuotaDisplayType  string  `json:"quota_display_type"`
	SystemName        string  `json:"system_name"`
	Version           string  `json:"version"`
}

// Status reads /api/status with no credential. The live divisor wins over
// any configured fallback, because a fork may change it.
func (c *Client) Status(ctx context.Context) (Status, error) {
	an := *c
	an.Req.Session, an.Req.UID, an.Req.Bearer = "", "", ""
	code, body, err := an.Get(ctx, "/api/status")
	if err != nil {
		return Status{}, err
	}
	if code != http.StatusOK {
		return Status{}, &HTTPError{Status: code}
	}
	var env struct {
		Data Status `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return Status{}, fmt.Errorf("status: %w", err)
	}
	return env.Data, nil
}

// Self is the logged-in account row from /api/user/self.
type Self struct {
	ID           int64   `json:"id"`
	Username     string  `json:"username"`
	Group        string  `json:"group"`
	Quota        float64 `json:"quota"`
	UsedQuota    float64 `json:"used_quota"`
	RequestCount int64   `json:"request_count"`
	Role         int64   `json:"role"`
	Status       int64   `json:"status"`
}

// Self reads /api/user/self. This is the ONLY way to learn a real balance:
// the two figures the console shows are quota/quota_per_unit and
// used_quota/quota_per_unit.
func (c *Client) Self(ctx context.Context) (Self, error) {
	if SessionValue(c.Req.Session) == "" {
		return Self{}, ErrNoSession
	}
	if NormalizeUID(c.Req.UID) == "" {
		return Self{}, ErrNoUID
	}
	code, body, err := c.Get(ctx, "/api/user/self")
	if err != nil {
		return Self{}, err
	}
	if code != http.StatusOK {
		return Self{}, &HTTPError{Status: code}
	}
	var env struct {
		Data    Self   `json:"data"`
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return Self{}, fmt.Errorf("user/self: %w", err)
	}
	if !env.Success {
		msg := strings.TrimSpace(env.Message)
		if msg == "" {
			msg = "success=false"
		}
		return Self{}, fmt.Errorf("user/self: %s", msg)
	}
	return env.Data, nil
}

// SpentUSD reads money already spent with the inference key, in USD.
// It is spent, never remaining: /dashboard/billing/usage reports cents.
func (c *Client) SpentUSD(ctx context.Context) (float64, error) {
	if strings.TrimSpace(c.Req.Bearer) == "" {
		return 0, errors.New("no inference key")
	}
	an := *c
	an.Req.Session, an.Req.UID = "", ""
	code, body, err := an.Get(ctx, "/dashboard/billing/usage")
	if err != nil {
		return 0, err
	}
	if code != http.StatusOK {
		return 0, &HTTPError{Status: code}
	}
	var out struct {
		TotalUsage float64 `json:"total_usage"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, err
	}
	return out.TotalUsage / 100, nil
}

// HTTPError is a non-2xx console answer; the body is not retained, because it
// may quote credentials back.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("console http %d", e.Status) }

// IsThrottled reports whether err is the shared per-IP throttling that a retry
// would only worsen.
func IsThrottled(err error) bool {
	var he *HTTPError
	if errors.As(err, &he) {
		return he.Status == http.StatusTooManyRequests
	}
	return false
}

// Round4 rounds to four decimals, the precision the console shows.
func Round4(v float64) float64 {
	if v < 0 {
		return float64(int64(v*10000-0.5)) / 10000
	}
	return float64(int64(v*10000+0.5)) / 10000
}

// BalanceUSD converts a raw quota into the console's "Current balance":
// quota / quota_per_unit. A non-positive divisor falls back to 500000, the
// value measured on altapi.ai.
func BalanceUSD(quota, quotaPerUnit float64) float64 {
	if quotaPerUnit <= 0 {
		quotaPerUnit = 500000
	}
	return Round4(quota / quotaPerUnit)
}

// IsLoopbackOnly reports whether a proxy URL would keep traffic on this host,
// which is never what a pool account wants.
func IsLoopbackOnly(proxyURL string) bool {
	u, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
