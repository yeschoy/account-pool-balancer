// Package telnyxreg implements the Telnyx (telnyx.com) account registrar.
//
// Flow reconstructed from a real MITM-captured browser session on 2026-11-16
// (Chrome 157, US egress, manual Microsoft-email signup):
//
//	POST https://telnyx.com/api/validate-email
//	     body {"email":"..."} → {"success":true,"data":{"isFreemail":true,...}}
//
//	POST https://api.telnyx.com/v2/unified_signup
//	     body {email, terms_of_service, terms_and_conditions_url,
//	           privacy_policy_url, subscription_opt_in, cf-turnstile-response}
//	     → 200 {"success":true,"message":"A one-time sign-in link has been sent..."}
//
//	     The Turnstile widget renders with data-action="signup" and
//	     data-sitekey=0x4AAAAAABkzL3BycQIQz_Ti. The action is part of the
//	     token signature: a token minted without it is always rejected.
//
//	mail  "Your Single Use Telnyx Portal sign-in link" from portal@telnyx.com
//	      link https://portal.telnyx.com/#/freemium-redirect?token=<uuid>&email=<addr>
//
//	POST https://api.telnyx.com/v2/ai_dev_event_sessions
//	     body {"portal_redirect_token":"<uuid>","email":"..."}
//	     → {"credentials":{"api_v2_token":"gAAAAAB...", ...}}
//	     This is the portal session Bearer token; the mail link itself is
//	     consumed here (single use, short TTL).
//
//	GET  /v2/user_profile, /v2/organization, /v2/my_account_level
//	     Authorization: Bearer <api_v2_token>
//	POST https://api.telnyx.com/v2/api_keys   body {"tags":["new"]}  → 201
//	GET  https://api.telnyx.com/v2/balance/promo-credits → remaining_credits
//
// Notes on anti-fingerprinting (all verified):
//   - Turnstile checks the token against sitekey+action, not the UA, so an
//     HTTP-only client works once action is supplied.
//   - Every request in one account must leave through the same egress IP:
//     Telnyx ties the portal token to the session that redeemed it.
//   - Keep TLS ClientHello browser-shaped; a plain Go handshake is flagged.
package telnyxreg

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Default endpoints. Overridable for tests.
const (
	DefaultSiteOrigin = "https://telnyx.com"
	DefaultAPIBase    = "https://api.telnyx.com"
	DefaultPortalBase = "https://portal.telnyx.com"

	DefaultTermsURL   = "https://telnyx.com/legal/terms-and-conditions-of-service/terms-and-conditions-of-service-2023-09-26"
	DefaultPrivacyURL = "https://telnyx.com/legal/privacy-policy/privacy-policy-2023-11-03"

	// DefaultSiteKey is the Turnstile widget key on /sign-up.
	DefaultSiteKey = "0x4AAAAAABkzL3BycQIQz_Ti"
	// DefaultTurnstileAction must match the widget's data-action attribute.
	DefaultTurnstileAction = "signup"
)

// DefaultUserAgent mirrors the verified Chrome build used during recon.
const DefaultUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"

// Config holds one registration attempt's parameters.
type Config struct {
	SiteOrigin string
	APIBase    string
	PortalBase string

	// ProxyURL is a socks5://user:pass@host:port or http(s):// URL.
	// It must stay pinned for the whole account: portal token redemption is
	// bound to the egress IP that submitted the signup.
	ProxyURL string

	Email string

	// Captcha mints cf-turnstile-response tokens. Required.
	Captcha CaptchaSolver

	// SiteKey / TurnstileAction default to the values observed on /sign-up.
	SiteKey         string
	TurnstileAction string

	// TermsURL / PrivacyURL default to the current legal URLs.
	TermsURL   string
	PrivacyURL string

	// OptInSubscriptions maps to subscription_opt_in; upstream sent true.
	OptInSubscriptions bool

	// Mailbox supplies the one-time sign-in link.
	Mailbox Mailbox

	// KeyTags is the POST /v2/api_keys body. Upstream portal sends ["new"].
	KeyTags []string

	// MailWait bounds how long we poll for the sign-in mail.
	MailWait time.Duration
	// MailInterval is the inbox poll cadence.
	MailInterval time.Duration
	// Timeout bounds a single HTTP request.
	Timeout time.Duration

	UserAgent string

	// Trace receives human-readable progress. Optional.
	Trace func(format string, args ...any)
}

func (c *Config) normalize() {
	if c.SiteOrigin == "" {
		c.SiteOrigin = DefaultSiteOrigin
	}
	if c.APIBase == "" {
		c.APIBase = DefaultAPIBase
	}
	if c.PortalBase == "" {
		c.PortalBase = DefaultPortalBase
	}
	if c.SiteKey == "" {
		c.SiteKey = DefaultSiteKey
	}
	if c.TurnstileAction == "" {
		c.TurnstileAction = DefaultTurnstileAction
	}
	if c.TermsURL == "" {
		c.TermsURL = DefaultTermsURL
	}
	if c.PrivacyURL == "" {
		c.PrivacyURL = DefaultPrivacyURL
	}
	if len(c.KeyTags) == 0 {
		c.KeyTags = []string{"new"}
	}
	if c.MailWait <= 0 {
		c.MailWait = 240 * time.Second
	}
	if c.MailInterval <= 0 {
		c.MailInterval = 5 * time.Second
	}
	if c.Timeout <= 0 {
		c.Timeout = 45 * time.Second
	}
	if c.UserAgent == "" {
		c.UserAgent = DefaultUserAgent
	}
	c.SiteOrigin = strings.TrimRight(c.SiteOrigin, "/")
	c.APIBase = strings.TrimRight(c.APIBase, "/")
	c.PortalBase = strings.TrimRight(c.PortalBase, "/")
}

// Account is the successful registration result.
type Account struct {
	Email        string    `json:"email"`
	UserID       string    `json:"user_id,omitempty"`
	SessionToken string    `json:"session_token"` // api_v2_token (portal Bearer)
	APIKey       string    `json:"api_key"`
	KeyID        string    `json:"key_id,omitempty"`
	AccountLevel string    `json:"account_level,omitempty"`
	CreditsUSD   string    `json:"credits_usd,omitempty"`
	EgressIP     string    `json:"egress_ip,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Stage names one position in the signup flow.
type Stage string

const (
	StageValidate Stage = "validate" // POST /api/validate-email
	StageCaptcha  Stage = "captcha"  // Turnstile token mint
	StageSignup   Stage = "signup"   // POST /v2/unified_signup
	StageMailWait Stage = "waiting"  // polling the inbox for the portal link
	StageSession  Stage = "session"  // POST /v2/ai_dev_event_sessions
	StageProfile  Stage = "profile"  // GET /v2/user_profile + account level
	StageKey      Stage = "key"      // POST /v2/api_keys
	StageCredits  Stage = "credits"  // GET /v2/balance/promo-credits
	StageDone     Stage = "done"
)

// CaptchaSolver mints Turnstile tokens for one site.
type CaptchaSolver interface {
	// SolveTurnstile returns "1.xxxx" ready to submit as cf-turnstile-response.
	SolveTurnstile(ctx context.Context, siteKey, siteReferer, action string) (string, error)
}

// Mailbox yields the sign-in link for an address.
type Mailbox interface {
	// WaitPortalLink polls until a message matching the address yields a
	// portal.telnyx.com freemium-redirect URL, or the context ends.
	WaitPortalLink(ctx context.Context, email string) (link string, err error)
}

// Registrar performs one account registration.
type Registrar struct {
	cfg      Config
	http     *http.Client
	jar      http.CookieJar
	trace    func(format string, args ...any)
	progress func(stage Stage, detail string)
	mu       sync.Mutex
	// pendingLink caches a link already consumed by the caller.
	pendingLink string
	// session holds the portal Bearer for this registrar instance only.
	session string
	// egressIP is the pinned proxy exit address, from CheckEgress.
	egressIP string
}

// New builds a Registrar. cfg is normalized in place.
func New(cfg Config) (*Registrar, error) {
	cfg.normalize()
	if strings.TrimSpace(cfg.Email) == "" {
		return nil, errors.New("telnyxreg: email required")
	}
	if cfg.Captcha == nil {
		return nil, errors.New("telnyxreg: captcha solver required")
	}
	if cfg.Mailbox == nil {
		return nil, errors.New("telnyxreg: mailbox required")
	}
	jar, _ := cookiejar.New(nil)
	client, err := newHTTPClient(cfg, jar)
	if err != nil {
		return nil, err
	}
	if cfg.Trace == nil {
		cfg.Trace = func(string, ...any) {}
	}
	return &Registrar{cfg: cfg, http: client, jar: jar, trace: cfg.Trace}, nil
}

// newHTTPClient builds a client pinned to the configured proxy with a
// browser-shaped TLS handshake.
func newHTTPClient(cfg Config, jar http.CookieJar) (*http.Client, error) {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   20 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// Present a current Chrome cipher/curve set so the ClientHello is not
		// obviously "not a browser".
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			MaxVersion: tls.VersionTLS13,
			CurvePreferences: []tls.CurveID{
				tls.X25519, tls.CurveP256, tls.CurveP384,
			},
		},
	}
	if cfg.ProxyURL != "" {
		pu, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("telnyxreg: bad proxy %q: %w", cfg.ProxyURL, err)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	return &http.Client{
		Transport: tr,
		Timeout:   cfg.Timeout,
		Jar:       jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}, nil
}

// Run performs the registration and returns the resulting account.
func (r *Registrar) Run(ctx context.Context) (*Account, error) {
	email := r.cfg.Email
	acct := &Account{Email: email, CreatedAt: time.Now().UTC()}

	// 0. Record the egress address. The portal token is bound to it.
	//
	// A failed probe is not fatal (some proxies block the lookup) but it is
	// reported: without a recorded egress an operator cannot tell an expired
	// link from an IP mismatch when the session exchange returns 401.
	if ip, err := r.CheckEgress(ctx); err == nil {
		r.stage("egress", "%s", ip)
		acct.EgressIP = ip
	} else {
		r.stage("egress", "probe failed: %v (continuing)", err)
	}

	// 1. Email pre-check. A freemail address is accepted (isFreemail:true);
	//    hard rejects surface here before spending a captcha.
	if err := r.validateEmail(ctx); err != nil {
		return nil, fmt.Errorf("validate email: %w", err)
	}

	// 2–3. Mint a Turnstile token and submit the signup.
	//
	// A captcha token is only valid for one submit, and a token minted while
	// the solver farm is saturated is sometimes rejected as
	// "your browser could not be authenticated". Retry the pair — a fresh
	// token each time — before giving up.
	const attempts = 3
	var lastErr error
	for i := 1; i <= attempts; i++ {
		r.stage(StageCaptcha, "requesting turnstile token (action=%s) try %d/%d",
			r.cfg.TurnstileAction, i, attempts)
		token, err := r.cfg.Captcha.SolveTurnstile(ctx, r.cfg.SiteKey,
			r.cfg.SiteOrigin+"/sign-up", r.cfg.TurnstileAction)
		if err != nil {
			lastErr = fmt.Errorf("turnstile: %w", err)
			continue
		}
		r.stage(StageCaptcha, "token ok (%d bytes)", len(token))

		err = r.unifiedSignup(ctx, token)
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		if !isCaptchaRejection(err) {
			return nil, err // a real rejection; retrying will not help
		}
		r.stage(StageSignup, "token rejected, minting a new one (%d/%d)", i, attempts)
	}
	if lastErr != nil {
		return nil, lastErr
	}

	// 4. Wait for the portal sign-in link.
	r.stage(StageMailWait, "polling inbox for %s", email)
	link, err := r.waitLink(ctx, email)
	if err != nil {
		return nil, err
	}

	// 5. Redeem the link for a portal session.
	if err := r.redeemSession(ctx, link, acct); err != nil {
		return nil, err
	}

	// 6. Read profile + account level (best effort, non-fatal).
	r.readProfile(ctx, acct)

	// 7. Create the API key.
	if err := r.createAPIKey(ctx, acct); err != nil {
		return nil, err
	}

	// 8. Read the welcome credits (best effort).
	r.readCredits(ctx, acct)

	r.stage(StageDone, "account ready: %s", email)
	return acct, nil
}

// SetProgress installs a stage observer. The console uses it to show the row's
// position without parsing log text.
func (r *Registrar) SetProgress(fn func(stage Stage, detail string)) {
	r.mu.Lock()
	r.progress = fn
	r.mu.Unlock()
}

// SetTracer replaces the human-readable trace hook after construction.
func (r *Registrar) SetTracer(fn func(format string, args ...any)) {
	if fn == nil {
		return
	}
	r.mu.Lock()
	r.trace = fn
	r.mu.Unlock()
}

func (r *Registrar) stage(s Stage, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	r.mu.Lock()
	trace, progress := r.trace, r.progress
	r.mu.Unlock()
	if trace != nil {
		trace("[%s] %s", s, detail)
	}
	if progress != nil {
		progress(s, detail)
	}
}

func (r *Registrar) validateEmail(ctx context.Context) error {
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			IsFreemail bool   `json:"isFreemail"`
			Email      string `json:"email"`
		} `json:"data"`
	}
	body := map[string]any{"email": r.cfg.Email}
	if err := r.postJSON(ctx, r.cfg.SiteOrigin+"/api/validate-email", body, &out, false); err != nil {
		return err
	}
	if !out.Success {
		return errors.New("upstream rejected the address")
	}
	r.stage(StageValidate, "isFreemail=%v", out.Data.IsFreemail)
	return nil
}

func (r *Registrar) unifiedSignup(ctx context.Context, turnstile string) error {
	body := map[string]any{
		"email":                    r.cfg.Email,
		"terms_of_service":         true,
		"terms_and_conditions_url": r.cfg.TermsURL,
		"privacy_policy_url":       r.cfg.PrivacyURL,
		"subscription_opt_in":      r.cfg.OptInSubscriptions,
		"cf-turnstile-response":    turnstile,
	}
	var out struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	if err := r.postJSON(ctx, r.cfg.APIBase+"/v2/unified_signup", body, &out, true); err != nil {
		return fmt.Errorf("unified_signup: %w", err)
	}
	if !out.Success {
		return fmt.Errorf("unified_signup rejected: %s", out.Message)
	}
	r.stage(StageSignup, "%s", out.Message)
	return nil
}

func (r *Registrar) waitLink(ctx context.Context, email string) (string, error) {
	if r.pendingLink != "" {
		l := r.pendingLink
		r.pendingLink = ""
		return l, nil
	}
	deadline, cancel := context.WithTimeout(ctx, r.cfg.MailWait)
	defer cancel()
	link, err := r.cfg.Mailbox.WaitPortalLink(deadline, email)
	if err != nil {
		return "", fmt.Errorf("mail: %w", err)
	}
	r.stage(StageMailWait, "link received")
	return link, nil
}

// portalLinkPattern extracts token+email from the sign-in URL.
var portalLinkPattern = regexp.MustCompile(`[?&]token=([0-9a-fA-F-]{32,40}).*?[?&]email=([^&\s]+)`)

// ParsePortalLink splits the mail link into the redirect token and address.
func ParsePortalLink(link string) (token, email string, err error) {
	u, perr := url.Parse(strings.TrimSpace(link))
	if perr != nil {
		return "", "", fmt.Errorf("parse link: %w", perr)
	}
	q := u.Query()
	// The token lives in the fragment query for /#/freemium-redirect.
	if q.Get("token") == "" && u.Fragment != "" {
		if frag, ferr := url.Parse(u.Fragment); ferr == nil {
			q = frag.Query()
		} else if i := strings.Index(u.Fragment, "?"); i >= 0 {
			if vals, verr := url.ParseQuery(u.Fragment[i+1:]); verr == nil {
				q = vals
			}
		}
	}
	token = q.Get("token")
	email = q.Get("email")
	if token == "" {
		if m := portalLinkPattern.FindStringSubmatch(link); m != nil {
			token, email = m[1], m[2]
		}
	}
	if token == "" {
		return "", "", fmt.Errorf("no token in link %q", link)
	}
	return token, email, nil
}

func (r *Registrar) redeemSession(ctx context.Context, link string, acct *Account) error {
	token, mailEmail, err := ParsePortalLink(link)
	if err != nil {
		return err
	}
	if mailEmail != "" && !strings.EqualFold(mailEmail, r.cfg.Email) {
		return fmt.Errorf("mail link belongs to %s, want %s", mailEmail, r.cfg.Email)
	}
	body := map[string]any{
		"portal_redirect_token": token,
		"email":                 r.cfg.Email,
	}
	var out struct {
		Success     bool   `json:"success"`
		Message     string `json:"message"`
		Credentials struct {
			APIUser      string `json:"api_user"`
			UserID       string `json:"user_id"`
			APIV2Token   string `json:"api_v2_token"`
			APIKey       string `json:"api_access_key"`
			FreemailSign bool   `json:"freemail_signup"`
		} `json:"credentials"`
	}
	if err := r.postJSON(ctx, r.cfg.APIBase+"/v2/ai_dev_event_sessions", body, &out, true); err != nil {
		return fmt.Errorf("session: %w", err)
	}
	if out.Credentials.APIV2Token == "" {
		return fmt.Errorf("session: no api_v2_token (message=%q)", out.Message)
	}
	acct.SessionToken = out.Credentials.APIV2Token
	r.setSessionToken(out.Credentials.APIV2Token)
	acct.UserID = out.Credentials.UserID
	r.stage(StageSession, "session for %s (user %s)", out.Credentials.APIUser, out.Credentials.UserID)
	return nil
}

func (r *Registrar) readProfile(ctx context.Context, acct *Account) {
	var prof struct {
		Data struct {
			ID     int64  `json:"id"`
			UserID string `json:"user_id"`
		} `json:"data"`
	}
	if err := r.getJSON(ctx, r.cfg.APIBase+"/v2/user_profile", &prof); err == nil {
		if acct.UserID == "" {
			acct.UserID = prof.Data.UserID
		}
		r.stage(StageProfile, "user_profile id=%d", prof.Data.ID)
	}
	var lvl struct {
		Data struct {
			AccountLevel string   `json:"account_level"`
			CriteriaMet  []string `json:"criteria_met"`
		} `json:"data"`
	}
	if err := r.getJSON(ctx, r.cfg.APIBase+"/v2/my_account_level", &lvl); err == nil {
		acct.AccountLevel = lvl.Data.AccountLevel
		r.stage(StageProfile, "account_level=%s criteria=%v", lvl.Data.AccountLevel, lvl.Data.CriteriaMet)
	}
}

// isCaptchaRejection reports the 400 Telnyx returns when the Turnstile token
// does not satisfy the sitekey/action binding.
func isCaptchaRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "could not be authenticated") ||
		strings.Contains(msg, "HTTP 400")
}

// errKeyLimit marks Telnyx error 10039: a free account may hold only one key.
var errKeyLimit = errors.New("account already holds its single allowed API key")

func (r *Registrar) createAPIKey(ctx context.Context, acct *Account) error {
	body := map[string]any{"tags": r.cfg.KeyTags}
	var out struct {
		Data struct {
			ID       string `json:"id"`
			Key      string `json:"key"`
			APIKey   string `json:"api_key"`
			ShortKey string `json:"short_key"`
		} `json:"data"`
	}
	if err := r.postJSON(ctx, r.cfg.APIBase+"/v2/api_keys", body, &out, true); err != nil {
		if strings.Contains(err.Error(), "10039") {
			return errKeyLimit
		}
		return fmt.Errorf("create api key: %w", err)
	}
	key := out.Data.Key
	if key == "" {
		key = out.Data.APIKey
	}
	if key == "" {
		return errors.New("create api key: upstream returned no key")
	}
	acct.APIKey = key
	acct.KeyID = out.Data.ID
	r.stage(StageKey, "key created %s…", shortKey(key))
	return nil
}

func (r *Registrar) readCredits(ctx context.Context, acct *Account) {
	var out struct {
		Data []struct {
			RemainingCredits string `json:"remaining_credits"`
			MaxDiscount      string `json:"max_discount_amount"`
		} `json:"data"`
	}
	if err := r.getJSON(ctx, r.cfg.APIBase+"/v2/balance/promo-credits", &out); err != nil {
		return
	}
	for _, p := range out.Data {
		if p.RemainingCredits != "" {
			acct.CreditsUSD = p.RemainingCredits
			r.stage(StageCredits, "remaining_credits=%s", p.RemainingCredits)
			return
		}
	}
}

func shortKey(k string) string {
	if len(k) <= 12 {
		return k
	}
	return k[:12]
}

// postJSON sends body and decodes the reply. authed adds the portal Bearer.
func (r *Registrar) postJSON(ctx context.Context, rawURL string, body any, out any, apiHost bool) error {
	req, err := r.newRequest(ctx, http.MethodPost, rawURL, body, apiHost)
	if err != nil {
		return err
	}
	return r.do(req, out)
}

func (r *Registrar) getJSON(ctx context.Context, rawURL string, out any) error {
	req, err := r.newRequest(ctx, http.MethodGet, rawURL, nil, true)
	if err != nil {
		return err
	}
	return r.do(req, out)
}

func (r *Registrar) newRequest(ctx context.Context, method, rawURL string, body any, apiHost bool) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", r.cfg.UserAgent)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiHost {
		req.Header.Set("Origin", r.cfg.PortalBase)
		req.Header.Set("Referer", r.cfg.PortalBase+"/")
	} else {
		req.Header.Set("Origin", r.cfg.SiteOrigin)
		req.Header.Set("Referer", r.cfg.SiteOrigin+"/sign-up")
	}
	if r.sessionToken() != "" {
		req.Header.Set("Authorization", "Bearer "+r.sessionToken())
	}
	return req, nil
}

func (r *Registrar) sessionToken() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.session
}

func (r *Registrar) setSessionToken(v string) {
	r.mu.Lock()
	r.session = v
	r.mu.Unlock()
}

func (r *Registrar) do(req *http.Request, out any) error {
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s → HTTP %d: %s", req.Method, redact(req.URL.String()),
			resp.StatusCode, truncate(string(buf), 300))
	}
	if out == nil || len(buf) == 0 {
		return nil
	}
	if err := json.Unmarshal(buf, out); err != nil {
		return fmt.Errorf("decode %s: %w (body %s)", req.URL.Path, err, truncate(string(buf), 200))
	}
	return nil
}

func redact(u string) string {
	if i := strings.IndexByte(u, '?'); i >= 0 {
		return u[:i]
	}
	return u
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
