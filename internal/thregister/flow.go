// Package thregister is the Token Harbor (tokenharbor.ai) signup engine.
//
// It is a pure-HTTP flow: no browser, no display. The one step that cannot be
// solved in-process is Cloudflare Turnstile, which tokenharbor.ai asks for on a
// fraction of signups (GET /api/auth/signup-precheck, keyed by the handed-out
// device fingerprint). That token comes from a solver service (captcha-run).
//
// The flow, as observed on the live site:
//
//  1. GET  /login?mode=signup                       -> page, cookies, form fields
//  2. GET  /api/auth/signup-precheck?fp=<uuid>      -> {"needCaptcha":bool}
//  3. POST /login?mode=signup (Next server action)  -> Set-Cookie auth session
//     multipart fields: _1_email, _1_password, _1_cf-turnstile-response,
//     _1_device_fingerprint, _1_timezone, _1_invite_code, _1_next
//  4. POST /api/me/privacy  {"free_models_enabled":true}  (free-tier routes)
//  5. GET  /verify-email?token=…                    (link arrives by mail)
//  6. POST /api/keys {"label":…}                     -> thk_live_…
//
// Registration returns a logged-in browser session; email verification only
// unlocks API access. The API key is shown once, so it is captured here and
// never requested again.
package thregister

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Endpoints of the live site. Tests override them through Config.
const (
	DefaultBase   = "https://tokenharbor.ai"
	DefaultSolver = "https://api.captcha-run.com"
	// TurnstileSiteKey is the key tokenharbor.ai renders for the signup form.
	TurnstileSiteKey = "0x4AAAAAADBuC8Knz1EJZx9-"
	// TurnstileAction is the data-action the widget is rendered with.
	TurnstileAction = "signup"
	defaultTZ       = "UTC"
	maxBody         = 1 << 20
)

// ErrCaptchaRequired reports a signup that cannot proceed without a solved
// Turnstile token (no solver configured, or the solver failed).
var ErrCaptchaRequired = errors.New("turnstile token required")

// ErrSessionRejected reports a signup the server refused (rate limit, closed
// registration, a rejected captcha, a duplicate email).
var ErrSessionRejected = errors.New("signup rejected")

// Credentials is one account to register.
type Credentials struct {
	Email    string
	Password string
	// MailboxURL is the receive page of a forwarding mailbox. It is only used
	// to wait for the verification mail and to name the mailbox in the log.
	MailboxURL string
	// ProxyURL is the egress for every request of this registration.
	ProxyURL string
}

// Result is what one successful registration produces. It carries credentials
// and must never be logged or serialized into a page.
type Result struct {
	Email string
	// APIKey is the thk_live_… key created on the dashboard. It is shown once
	// by the site and stored verbatim by the pool.
	APIKey string
	// UserID is a stable, credential-free identifier: SHA-256 of the account's
	// e-mail, so two runs of the same mailbox map to one pool account.
	UserID string
	// Cookies is the logged-in browser session, for later re-reads.
	Cookies []NamedCookie
	// VerifiedEmail records whether step 5 completed within this run.
	VerifiedEmail bool
	// FreeModelsEnabled records whether step 4 succeeded.
	FreeModelsEnabled bool
}

// NamedCookie is one session cookie (name, value) — the minimum needed to
// replay the session without carrying a full jar.
type NamedCookie struct{ Name, Value string }

// Solver resolves one Turnstile challenge to a token.
type Solver interface {
	Turnstile(ctx context.Context, siteKey, referer, action string) (string, error)
}

// Mailbox returns the messages a forwarding mailbox has received.
type Mailbox interface {
	Messages(ctx context.Context) ([]Message, error)
}

// Message is the part of a received mail the flow reads.
type Message struct {
	ID         string
	ReceivedAt time.Time
	Subject    string
	From       string
	Text       string
}

// Config selects endpoints, transports and helpers. Zero values are the live
// site with no proxy.
type Config struct {
	BaseURL  string
	ProxyURL string
	// Solver resolves Turnstile when the site asks for it; nil means "fail
	// with ErrCaptchaRequired instead of submitting blind".
	Solver Solver
	// Mailbox waits for the verification mail; nil skips verification (the
	// account works for the dashboard but the API stays locked).
	Mailbox Mailbox
	// Timezone is sent as the form's timezone field.
	Timezone string
	// Label is the API key label created on the dashboard.
	Label string
	// Direct reports that ProxyURL is the loopback relay rather than the real
	// egress; it only affects how the run is described in logs.
	Direct bool
	// RequestTimeout bounds a single HTTP request; zero means 30s.
	RequestTimeout time.Duration
	// MailTimeout bounds the wait for the verification mail; zero means 3m.
	MailTimeout time.Duration
	// PollEvery is the mailbox polling interval; zero means 5s.
	PollEvery time.Duration
}

func (c Config) base() string {
	if c.BaseURL == "" {
		return DefaultBase
	}
	return strings.TrimSuffix(c.BaseURL, "/")
}

func (c Config) timeout() time.Duration {
	if c.RequestTimeout <= 0 {
		return 30 * time.Second
	}
	return c.RequestTimeout
}

func (c Config) timezone() string {
	if c.Timezone == "" {
		return defaultTZ
	}
	return c.Timezone
}

func (c Config) label() string {
	if c.Label == "" {
		return "pool"
	}
	return c.Label
}

// Flow is one registration run. It keeps the cookie jar across steps, which is
// what makes the server treat every request as one browser.
type Flow struct {
	cfg    Config
	client *http.Client
	jar    *cookiejar.Jar
}

// New builds a flow with a fresh cookie jar and one transport.
func New(cfg Config) (*Flow, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	tr, err := Transport(cfg.ProxyURL)
	if err != nil {
		return nil, err
	}
	f := &Flow{cfg: cfg, jar: jar, client: &http.Client{
		Transport: tr,
		Jar:       jar,
		Timeout:   cfg.timeout(),
		// Redirects are followed by default; the auth endpoints redirect on
		// purpose and the flow re-reads state from the final response.
	}}
	return f, nil
}

// Close releases idle connections.
func (f *Flow) Close() {
	if tr, ok := f.client.Transport.(*http.Transport); ok {
		tr.CloseIdleConnections()
	}
}

// Transport is the egress transport of one registration. proxyUrl accepts
// http(s):// and socks5(://h) exactly like the rest of the program. A proxy is
// required in practice: the inference API answers 403 region_blocked to the
// regions these pools run in, while the site itself stays reachable.
func Transport(proxyURL string) (*http.Transport, error) {
	tr := &http.Transport{
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	if strings.TrimSpace(proxyURL) == "" {
		return tr, nil
	}
	u, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || u.Host == "" {
		return nil, errors.New("代理地址无效")
	}
	tr.Proxy = http.ProxyURL(u)
	return tr, nil
}

// Run registers one account end to end.
//
// The steps are ordered so the expensive part (the solved captcha) is spent
// last: the form and its action key are read first, then the precheck decides
// whether a token is needed, then the token is bought once and submitted.
func (f *Flow) Run(ctx context.Context, c Credentials) (Result, error) {
	email := strings.TrimSpace(c.Email)
	if !strings.Contains(email, "@") {
		return Result{}, errors.New("邮箱格式无效")
	}
	if c.Password == "" {
		return Result{}, errors.New("密码不能为空")
	}
	page, err := f.loadSignupPage(ctx)
	if err != nil {
		return Result{}, err
	}
	form, err := parseSignupForm(page)
	if err != nil {
		return Result{}, err
	}
	res := Result{Email: email, UserID: AccountID(email)}
	referer := f.cfg.base() + "/login?mode=signup"
	fingerprint := form.fingerprint
	if fingerprint == "" {
		fingerprint = newUUID()
	}

	needCaptcha, err := f.precheck(ctx, fingerprint)
	if err != nil {
		return Result{}, err
	}
	token := ""
	if needCaptcha {
		if f.cfg.Solver == nil {
			return Result{}, ErrCaptchaRequired
		}
		token, err = f.cfg.Solver.Turnstile(ctx, TurnstileSiteKey, referer, TurnstileAction)
		if err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrCaptchaRequired, err)
		}
		if strings.TrimSpace(token) == "" {
			return Result{}, ErrCaptchaRequired
		}
	}

	if err := f.submitSignup(ctx, form, signupFields{
		Email:       email,
		Password:    c.Password,
		Token:       token,
		Fingerprint: fingerprint,
		Timezone:    f.cfg.timezone(),
	}); err != nil {
		return Result{}, err
	}

	// Free models are a per-account switch; without it the key works but only
	// paid routes answer.
	if err := f.enableFreeModels(ctx); err == nil {
		res.FreeModelsEnabled = true
	}

	if f.cfg.Mailbox != nil {
		if link, err := f.waitVerificationLink(ctx, email); err == nil && link != "" {
			res.VerifiedEmail = f.verifyEmail(ctx, link) == nil
		}
	}

	key, err := f.createAPIKey(ctx)
	if err != nil {
		return Result{}, err
	}
	res.APIKey = key
	for _, ck := range f.jar.Cookies(mustURL(f.cfg.base())) {
		res.Cookies = append(res.Cookies, NamedCookie{Name: ck.Name, Value: ck.Value})
	}
	sort.Slice(res.Cookies, func(i, j int) bool { return res.Cookies[i].Name < res.Cookies[j].Name })
	return res, nil
}

// AccountID is the stable, credential-free identity of a mailbox: two runs of
// the same e-mail map to one pool account.
func AccountID(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "th_" + hex.EncodeToString(sum[:])[:24]
}

// ---- step 1: the page and its server action ------------------------------------

type signupForm struct {
	actionID    string
	actionKey   string
	fingerprint string
	tz          string
}

var (
	// The action id lives in the hidden $ACTION_1:0 field as escaped JSON.
	reActionID    = regexp.MustCompile(`name="\$ACTION_1:0" value="[^"]*?([0-9a-f]{40,44})`)
	reActionKey   = regexp.MustCompile(`name="\$ACTION_KEY"\s+value="([^"]+)"`)
	reFingerprint = regexp.MustCompile(`name="device_fingerprint"\s+value="([^"]*)"`)
	reTimezone    = regexp.MustCompile(`name="timezone"\s+value="([^"]*)"`)
)

func (f *Flow) loadSignupPage(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.cfg.base()+"/login?mode=signup", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("accept-language", "en-US,en;q=0.9")
	req.Header.Set("user-agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("打开注册页失败: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("打开注册页失败: HTTP %d", resp.StatusCode)
	}
	return string(body), nil
}

// parseSignupForm reads the hidden server-action fields the page renders.
// A page carrying no action id means the signup form is not offered (paused
// signups or a redesigned page) — better to stop than to post garbage.
func parseSignupForm(page string) (signupForm, error) {
	var out signupForm
	if m := reActionID.FindStringSubmatch(page); m != nil {
		out.actionID = m[1]
	}
	if m := reActionKey.FindStringSubmatch(page); m != nil {
		out.actionKey = m[1]
	}
	if m := reFingerprint.FindStringSubmatch(page); m != nil {
		out.fingerprint = m[1]
	}
	if m := reTimezone.FindStringSubmatch(page); m != nil {
		out.tz = m[1]
	}
	if out.actionID == "" || out.actionKey == "" {
		return out, errors.New("注册页没有可用的注册表单（可能已暂停注册或页面改版）")
	}
	return out, nil
}

func (f *Flow) precheck(ctx context.Context, fingerprint string) (bool, error) {
	u := f.cfg.base() + "/api/auth/signup-precheck?fp=" + url.QueryEscape(fingerprint)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("accept", "application/json")
	req.Header.Set("referer", f.cfg.base()+"/login?mode=signup")
	req.Header.Set("user-agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("注册预检失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("注册预检失败: HTTP %d", resp.StatusCode)
	}
	var out struct {
		NeedCaptcha bool `json:"needCaptcha"`
	}
	if json.Unmarshal(body, &out) != nil {
		return false, errors.New("注册预检返回无法解析")
	}
	return out.NeedCaptcha, nil
}

// ---- step 3: the server action POST -------------------------------------------

type signupFields struct {
	Email, Password, Token, Fingerprint, Timezone string
}

// submitSignup posts the signup server action as multipart/form-data, exactly
// as the browser's FormData would.
func (f *Flow) submitSignup(ctx context.Context, form signupForm, in signupFields) error {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	writeField := func(name, value string) error { return mw.WriteField(name, value) }
	steps := []struct{ name, value string }{
		{"_1_$ACTION_REF_1", ""},
		{"_1_$ACTION_1:0", `{"id":"` + form.actionID + `","bound":"$@1"}`},
		{"_1_$ACTION_1:1", `["$undefined"]`},
		{"_1_$ACTION_KEY", form.actionKey},
		{"_1_device_fingerprint", in.Fingerprint},
		{"_1_timezone", in.Timezone},
		{"_1_next", ""},
		{"_1_email", in.Email},
		{"_1_password", in.Password},
		{"_1_invite_code", ""},
	}
	if in.Token != "" {
		steps = append(steps, struct{ name, value string }{"_1_cf-turnstile-response", in.Token})
	}
	for _, s := range steps {
		if err := writeField(s.name, s.value); err != nil {
			return err
		}
	}
	if err := mw.WriteField("0", `["$undefined","$K1"]`); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.base()+"/login?mode=signup", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("content-type", mw.FormDataContentType())
	req.Header.Set("accept", "text/x-component")
	req.Header.Set("next-action", form.actionID)
	req.Header.Set("origin", f.cfg.base())
	req.Header.Set("referer", f.cfg.base()+"/login?mode=signup")
	req.Header.Set("user-agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("提交注册失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode == http.StatusOK && !bytes.Contains(body, []byte("NEXT_REDIRECT")) && len(body) == 0 {
		return fmt.Errorf("%w: HTTP 200 但无响应内容", ErrSessionRejected)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("%w: HTTP %d %s", ErrSessionRejected, resp.StatusCode, compact(body))
	}
	return nil
}

// ---- step 4: the per-account data-program switch -------------------------------

// shownText is the consent wording the dashboard submits with the switch. The
// server compares it against its own copy, so it is sent verbatim.
const shownText = "Token Harbor may keep the requests and responses you send through free models and use them for service diagnostics, safety, optimization, and to evaluate and improve models and products. Our staff may read free-route conversations and may use third-party AI tools to help analyze them, and the providers that serve free models may use them under their own terms, including to train their own models."

func (f *Flow) enableFreeModels(ctx context.Context) error {
	body, _ := json.Marshal(map[string]any{"free_models_enabled": true, "shown_text": shownText})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.base()+"/api/me/privacy", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("origin", f.cfg.base())
	req.Header.Set("referer", f.cfg.base()+"/dashboard")
	req.Header.Set("user-agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("开启免模型费失败: HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---- step 5: e-mail verification -----------------------------------------------

var verifyLinkRe = regexp.MustCompile(`https://tokenharbor\.ai/verify-email\?token=([A-Za-z0-9_\-]+)`)

// waitVerificationLink polls the mailbox for the newest verification link,
// ignoring mails that predate this run.
func (f *Flow) waitVerificationLink(ctx context.Context, email string) (string, error) {
	deadline := time.Now().Add(f.cfg.mailTimeout())
	poll := f.cfg.pollEvery()
	started := time.Now().Add(-2 * time.Minute) // tolerate clock skew
	for {
		msgs, err := f.cfg.Mailbox.Messages(ctx)
		if err == nil {
			// Newest first; the link in the newest matching mail wins.
			sort.Slice(msgs, func(i, j int) bool { return msgs[i].ReceivedAt.After(msgs[j].ReceivedAt) })
			for _, m := range msgs {
				if m.ReceivedAt.Before(started) {
					continue
				}
				if link := verifyLinkRe.FindString(m.Text); link != "" {
					return link, nil
				}
			}
		}
		if time.Now().After(deadline) {
			return "", errors.New("等待验证邮件超时")
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
	}
}

func (f *Flow) verifyEmail(ctx context.Context, link string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	req.Header.Set("referer", f.cfg.base()+"/dashboard")
	req.Header.Set("user-agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
	// A used or stale token lands on ?verify=invalid; the redirect target is
	// the only honest signal, so it is checked rather than the status code.
	if strings.Contains(resp.Request.URL.RawQuery, "verify=invalid") {
		return errors.New("验证链接无效")
	}
	return nil
}

// ---- step 6: the API key -------------------------------------------------------

var apiKeyRe = regexp.MustCompile(`thk_[A-Za-z0-9_]{20,}`)

func (f *Flow) createAPIKey(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{"label": f.cfg.label()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.base()+"/api/keys", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "application/json")
	req.Header.Set("origin", f.cfg.base())
	req.Header.Set("referer", f.cfg.base()+"/dashboard/api-keys")
	req.Header.Set("user-agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("创建 API Key 失败: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("创建 API Key 失败: HTTP %d", resp.StatusCode)
	}
	// The key may come back as a JSON field or only in the rendered page; both
	// shapes are searched rather than assumed.
	if key := apiKeyRe.FindString(string(raw)); key != "" {
		return key, nil
	}
	return "", errors.New("创建 API Key 成功但响应里没有 Key（可能已是旧页面结构）")
}

// ---- helpers -------------------------------------------------------------------

const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/156.0.0.0 Safari/537.36"

func mustURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		return &url.URL{}
	}
	return u
}

// compact trims a response body to a short, log-safe hint.
func compact(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func (c Config) mailTimeout() time.Duration {
	if c.MailTimeout <= 0 {
		return 3 * time.Minute
	}
	return c.MailTimeout
}

func (c Config) pollEvery() time.Duration {
	if c.PollEvery <= 0 {
		return 5 * time.Second
	}
	return c.PollEvery
}
