// Package x50reg implements the 50x AI (ai.50x.energy) account registrar.
//
// Flow observed from a real browser session on 2026-11-15:
//
//	GET  https://api.ai.50x.energy/api/auth/signup      → {"open":true,...}
//	POST https://api.ai.50x.energy/api/auth/register    → {"status":"verification_sent"}
//	     body {"email":...,"password":...}   (no captcha, no consent field)
//	     mail subject "Confirm your 50x AI account"
//	     link https://ai.50x.energy/?verify=<token>
//	POST https://api.ai.50x.energy/api/auth/verify      → {"token":"<session>"}
//	     body {"token":"<mail token>"}
//	POST https://api.ai.50x.energy/api/auth/login       → {"token":"<session>"}
//	GET  https://api.ai.50x.energy/api/keys  (Authorization: Token <session>)
//	POST https://api.ai.50x.energy/api/keys  {"name":"..."} → api_key revealed once
//	GET  https://api.ai.50x.energy/api/billing  → welcome credit
package x50reg

import (
	"bytes"
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

// Default endpoints. Overridable for tests and for a staging host.
const (
	DefaultAPIBase  = "https://api.ai.50x.energy"
	DefaultOrigin   = "https://ai.50x.energy"
	DefaultMailBase = "https://icloud.mida.vip"
)

// Config holds one registration attempt's parameters.
type Config struct {
	APIBase  string // e.g. https://api.ai.50x.energy
	Origin   string // e.g. https://ai.50x.energy
	MailBase string // e.g. https://icloud.mida.vip
	ProxyURL string // socks5://user:pass@host:port (optional)

	Email    string
	Password string

	// Mailbox supplies the shared read-only inbox holding the confirm mail.
	Mailbox Mailbox

	// KeyName for the created API key. Empty keeps upstream's default naming.
	KeyName string

	// MailWait bounds how long we poll the shared mailbox for the confirm mail.
	MailWait time.Duration
	// MailInterval is the poll cadence.
	MailInterval time.Duration
	// Timeout bounds a single HTTP request.
	Timeout time.Duration
	// UserAgent is sent on every request.
	UserAgent string
}

func (c *Config) normalize() {
	if c.APIBase == "" {
		c.APIBase = DefaultAPIBase
	}
	if c.Origin == "" {
		c.Origin = DefaultOrigin
	}
	if c.MailBase == "" {
		c.MailBase = DefaultMailBase
	}
	c.APIBase = strings.TrimRight(c.APIBase, "/")
	c.MailBase = strings.TrimRight(c.MailBase, "/")
	if c.MailWait <= 0 {
		c.MailWait = 120 * time.Second
	}
	if c.MailInterval <= 0 {
		c.MailInterval = 3 * time.Second
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.UserAgent == "" {
		c.UserAgent = DefaultUserAgent
	}
}

// DefaultUserAgent mirrors the verified Chrome build used during recon.
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"

// Account is the successful registration result.
type Account struct {
	Email        string    `json:"email"`
	Password     string    `json:"password"`
	SessionToken string    `json:"session_token"`
	APIKey       string    `json:"api_key"`
	KeyID        int64     `json:"key_id"`
	KeyPrefix    string    `json:"key_prefix"`
	Plan         string    `json:"plan"`
	Tier         string    `json:"tier"`
	BalanceUSD   string    `json:"balance_usd"`
	CreatedAt    time.Time `json:"created_at"`
}

// Tracer receives progress lines. Optional.
type Tracer func(format string, args ...any)

// Stage names one position in the signup flow so the console can show
// progress without parsing log text.
type Stage string

const (
	StageChecking Stage = "checking" // reading the signup switch
	StageRegister Stage = "register" // POST /api/auth/register
	StageMailWait Stage = "waiting"  // polling the shared inbox
	StageVerify   Stage = "verify"   // POST /api/auth/verify
	StageLogin    Stage = "login"    // fallback POST /api/auth/login
	StageKey      Stage = "key"      // POST /api/keys
	StageBilling  Stage = "billing"  // GET /api/billing
	StageDone     Stage = "done"
)

// ProgressFunc is called whenever the flow advances.
type ProgressFunc func(stage Stage, detail string)

// Registrar performs one account registration.
type Registrar struct {
	cfg      Config
	http     *http.Client
	mailbox  Mailbox
	trace    Tracer
	progress ProgressFunc
	now      func() time.Time
}

// New builds a Registrar. cfg is normalized in place.
func New(cfg Config) (*Registrar, error) {
	cfg.normalize()
	if cfg.Mailbox.Email == "" && cfg.Email != "" {
		cfg.Mailbox.Email = cfg.Email
	}
	if cfg.Mailbox.Token != "" && cfg.Email == "" {
		cfg.Email = cfg.Mailbox.Email
	}
	if cfg.Email == "" || cfg.Password == "" {
		return nil, errors.New("email and password are required")
	}
	tr := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if cfg.ProxyURL != "" {
		pu, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy: %w", err)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	return &Registrar{
		cfg:      cfg,
		mailbox:  cfg.Mailbox,
		http:     &http.Client{Transport: tr, Timeout: cfg.Timeout},
		now:      time.Now,
		trace:    func(string, ...any) {},
		progress: func(Stage, string) {},
	}, nil
}

// SetTracer installs a log-line callback.
func (r *Registrar) SetTracer(t Tracer) {
	if t != nil {
		r.trace = t
	}
}

// emit reports a stage without requiring a caller to install a callback.
func (r *Registrar) emit(stage Stage) {
	if r.progress != nil {
		r.progress(stage, "")
	}
}

// SetProgress installs a stage callback.
func (r *Registrar) SetProgress(p ProgressFunc) {
	if p != nil {
		r.progress = p
	}
}

func (r *Registrar) apiURL(path string) string { return r.cfg.APIBase + path }

// do performs one JSON request against the API host.
// token, when non-empty, becomes "Authorization: Token <token>".
func (r *Registrar) do(ctx context.Context, method, path, token string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.apiURL(path), rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("User-Agent", r.cfg.UserAgent)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", r.cfg.Origin)
	req.Header.Set("Referer", r.cfg.Origin+"/")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Token "+token)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

// errorFrom maps an upstream error body to a readable error.
func errorFrom(status int, body []byte) error {
	var e struct {
		Error  any    `json:"error"`
		Detail string `json:"detail"`
		Code   string `json:"code"`
	}
	_ = json.Unmarshal(body, &e)
	msg := ""
	switch v := e.Error.(type) {
	case string:
		msg = v
	case map[string]any:
		if m, ok := v["message"].(string); ok {
			msg = m
		}
	}
	if msg == "" {
		msg = e.Detail
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	if msg == "" {
		return fmt.Errorf("http %d", status)
	}
	return fmt.Errorf("http %d: %s", status, msg)
}

// CheckSignup reports whether registration is currently open.
func (r *Registrar) CheckSignup(ctx context.Context) (bool, error) {
	status, body, err := r.do(ctx, http.MethodGet, "/api/auth/signup", "", nil)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, errorFrom(status, body)
	}
	var v struct {
		Open bool `json:"open"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return false, err
	}
	return v.Open, nil
}

// Register runs the whole flow: signup → mail wait → verify → key → billing.
func (r *Registrar) Register(ctx context.Context) (*Account, error) {
	acc := &Account{Email: r.cfg.Email, Password: r.cfg.Password, CreatedAt: r.now()}

	r.emit(StageChecking)
	open, err := r.CheckSignup(ctx)
	if err != nil {
		return nil, fmt.Errorf("signup state: %w", err)
	}
	if !open {
		return nil, errors.New("registration is closed upstream")
	}

	since := r.now().Add(-2 * time.Minute)
	r.emit(StageRegister)
	r.trace("register %s", r.cfg.Email)
	if err := r.register(ctx); err != nil {
		return nil, err
	}

	r.emit(StageMailWait)
	r.trace("waiting for confirm mail")
	mailTok, err := r.waitMailToken(ctx, since)
	if err != nil {
		return nil, err
	}

	r.emit(StageVerify)
	r.trace("verify mail token")
	session, err := r.verify(ctx, mailTok)
	if err != nil {
		return nil, err
	}
	acc.SessionToken = session

	// The verify response already returns the session token; a login is only a
	// fallback when upstream declines that token on an authenticated probe.
	if err := r.probeSession(ctx, session); err != nil {
		r.emit(StageLogin)
		r.trace("session probe failed (%v); falling back to login", err)
		s2, lerr := r.login(ctx)
		if lerr != nil {
			return nil, fmt.Errorf("session probe: %v; login: %w", err, lerr)
		}
		acc.SessionToken = s2
		session = s2
	}

	r.emit(StageKey)
	r.trace("create key")
	key, kid, prefix, err := r.createKey(ctx, session)
	if err != nil {
		return nil, err
	}
	acc.APIKey, acc.KeyID, acc.KeyPrefix = key, kid, prefix

	r.emit(StageBilling)
	if p, t, bal, berr := r.billing(ctx, session); berr == nil {
		acc.Plan, acc.Tier, acc.BalanceUSD = p, t, bal
	}
	r.emit(StageDone)
	return acc, nil
}

func (r *Registrar) register(ctx context.Context) error {
	status, body, err := r.do(ctx, http.MethodPost, "/api/auth/register", "", map[string]string{
		"email":    r.cfg.Email,
		"password": r.cfg.Password,
	})
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated &&
		status != http.StatusAccepted && status != http.StatusNoContent {
		return fmt.Errorf("register: %w", errorFrom(status, body))
	}
	return nil
}

func (r *Registrar) verify(ctx context.Context, mailToken string) (string, error) {
	status, body, err := r.do(ctx, http.MethodPost, "/api/auth/verify", "", map[string]string{"token": mailToken})
	if err != nil {
		return "", fmt.Errorf("verify: %w", err)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("verify: %w", errorFrom(status, body))
	}
	var v struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", fmt.Errorf("verify: decode: %w", err)
	}
	if v.Token == "" {
		return "", errors.New("verify: empty session token")
	}
	return v.Token, nil
}

func (r *Registrar) login(ctx context.Context) (string, error) {
	status, body, err := r.do(ctx, http.MethodPost, "/api/auth/login", "", map[string]string{
		"email":    r.cfg.Email,
		"password": r.cfg.Password,
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", errorFrom(status, body)
	}
	var v struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", err
	}
	if v.Token == "" {
		return "", errors.New("login: empty token")
	}
	return v.Token, nil
}

func (r *Registrar) probeSession(ctx context.Context, session string) error {
	status, body, err := r.do(ctx, http.MethodGet, "/api/auth/me", session, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return errorFrom(status, body)
	}
	return nil
}

func (r *Registrar) createKey(ctx context.Context, session string) (key string, id int64, prefix string, err error) {
	payload := map[string]string{}
	if r.cfg.KeyName != "" {
		payload["name"] = r.cfg.KeyName
	}
	status, body, err := r.do(ctx, http.MethodPost, "/api/keys", session, payload)
	if err != nil {
		return "", 0, "", fmt.Errorf("create key: %w", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return "", 0, "", fmt.Errorf("create key: %w", errorFrom(status, body))
	}
	var v struct {
		ID     int64  `json:"id"`
		Prefix string `json:"prefix"`
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", 0, "", fmt.Errorf("create key: decode: %w", err)
	}
	if v.APIKey == "" {
		return "", 0, "", errors.New("create key: upstream did not reveal api_key")
	}
	return v.APIKey, v.ID, v.Prefix, nil
}

func (r *Registrar) billing(ctx context.Context, session string) (plan, tier, balance string, err error) {
	status, body, err := r.do(ctx, http.MethodGet, "/api/billing", session, nil)
	if err != nil {
		return "", "", "", err
	}
	if status != http.StatusOK {
		return "", "", "", errorFrom(status, body)
	}
	var v struct {
		BalanceUSD string `json:"balance_usd"`
		Tier       string `json:"tier"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", "", "", err
	}
	plan = "starter"
	return plan, v.Tier, v.BalanceUSD, nil
}
