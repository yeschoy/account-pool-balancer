package perch

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Session is one Supabase session as the account JSON keeps it: the pair of
// tokens plus what they say about the account.
type Session struct {
	AccessToken  string
	RefreshToken string
	// ExpiresAt is the access token's end, in Unix seconds. Zero means the
	// account JSON did not say; the token's own exp claim is used instead.
	ExpiresAt int64
	// UserID is Supabase's user id — the account's identity.
	UserID string
	// Email is the address Supabase reports (the account JSON may name one
	// too; see Merge).
	Email string
	// Provider is the auth provider ("github", "google", "email").
	Provider string
	// TokenType is GoTrue's token_type ("bearer").
	TokenType string
}

// Valid reports whether the session carries the pair a renewal needs.
func (s Session) Valid() bool { return s.AccessToken != "" && s.RefreshToken != "" }

// HasAccess reports whether there is an access token to present to Perch.
func (s Session) HasAccess() bool { return s.AccessToken != "" }

// Expires reports the access token's end; a session with no stated end but a
// parsable token is read from the token's own exp claim (and reported as
// unknown when that claim is absent too).
func (s Session) Expires() (time.Time, bool) {
	if s.ExpiresAt > 0 {
		return time.Unix(s.ExpiresAt, 0), true
	}
	if exp, ok := ExpiryOf(s.AccessToken); ok {
		return exp, true
	}
	return time.Time{}, false
}

// Due reports whether the access token should be renewed now: it is absent,
// its end is unknown, or less than margin remains.
func (s Session) Due(now time.Time, margin time.Duration) bool {
	if s.AccessToken == "" {
		return true
	}
	exp, ok := s.Expires()
	if !ok {
		// no stated end and no readable claim: treat it as stale, a refresh
		// is cheap and a refused one is the definitive answer
		return true
	}
	return !now.Add(margin).Before(exp)
}

// ExpiryOf reads a JWT's exp claim without verifying it. Perch's tokens are
// the Supabase access tokens whose end is what we schedule our refresh on;
// the signature is Perch's to check, this only needs the clock.
func ExpiryOf(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	seg := parts[1]
	if rem := len(seg) % 4; rem != 0 {
		seg += strings.Repeat("=", 4-rem)
	}
	raw, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}, false
		}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// TokensOf reads a JWT's scoped identity claims (uid, wid) and exp without
// verifying the signature.
func TokensOf(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	seg := parts[1]
	if rem := len(seg) % 4; rem != 0 {
		seg += strings.Repeat("=", 4-rem)
	}
	raw, err := base64.URLEncoding.DecodeString(seg)
	if err != nil {
		if raw, err = base64.RawURLEncoding.DecodeString(parts[1]); err != nil {
			return nil
		}
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

// Merge folds in over s: a field in omits keeps s's value, so a bare token
// renewal can never blank an account's email or user id. The access token is
// the only field a caller relies on, and renew refuses an answer without one.
func (s Session) Merge(in Session) Session {
	out := s
	if in.AccessToken != "" {
		out.AccessToken = in.AccessToken
	}
	if in.RefreshToken != "" {
		out.RefreshToken = in.RefreshToken
	}
	if in.ExpiresAt > 0 {
		out.ExpiresAt = in.ExpiresAt
	} else if exp, ok := ExpiryOf(in.AccessToken); ok {
		out.ExpiresAt = exp.Unix()
	}
	if in.UserID != "" {
		out.UserID = in.UserID
	}
	if in.Email != "" {
		out.Email = in.Email
	}
	if in.Provider != "" {
		out.Provider = in.Provider
	}
	if in.TokenType != "" {
		out.TokenType = in.TokenType
	}
	return out
}

// SignIn is GoTrue's session answer, narrowed to what an account keeps.
type SignIn struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	User         struct {
		ID            string         `json:"id"`
		Email         string         `json:"email"`
		UserMetadata  map[string]any `json:"user_metadata"`
		AppMetadata   map[string]any `json:"app_metadata"`
		ConfirmedAt   string         `json:"confirmed_at"`
		EmailVerified bool           `json:"email_verified"`
	} `json:"user"`
	// An error answer {error, error_code, msg} is turned into an *Error by
	// renew, so reaching SignIn means a session came back.
}

// session normalises the answer: email from user_metadata when user.email is
// empty, provider from app_metadata, and the end from expires_at, else
// expires_in, else the token's own claim.
func (in SignIn) session(now time.Time) Session {
	s := Session{
		AccessToken:  strings.TrimSpace(in.AccessToken),
		RefreshToken: strings.TrimSpace(in.RefreshToken),
		TokenType:    strings.TrimSpace(in.TokenType),
		UserID:       strings.TrimSpace(in.User.ID),
		Email:        strings.TrimSpace(in.User.Email),
	}
	if s.Email == "" {
		if v, ok := in.User.UserMetadata["email"].(string); ok {
			s.Email = strings.TrimSpace(v)
		}
	}
	if v, ok := in.User.AppMetadata["provider"].(string); ok {
		s.Provider = strings.TrimSpace(v)
	}
	switch {
	case in.ExpiresAt > 0:
		s.ExpiresAt = in.ExpiresAt
	case in.ExpiresIn > 0:
		s.ExpiresAt = now.Unix() + in.ExpiresIn
	default:
		if exp, ok := ExpiryOf(s.AccessToken); ok {
			s.ExpiresAt = exp.Unix()
		}
	}
	return s
}

// Creds are what one refresh call needs: the project the session belongs to
// and the publishable key GoTrue demands as apikey.
type Creds struct {
	SupabaseURL string
	SupabaseKey string
	// ProxyURL routes this account's calls through a proxy ("" = direct).
	ProxyURL string
}

func (c Creds) url() string {
	u := TrimBase(c.SupabaseURL)
	if u == "" {
		u = DefaultSupabaseURL
	}
	return u
}

func (c Creds) key() string {
	if k := strings.TrimSpace(c.SupabaseKey); k != "" {
		return k
	}
	return DefaultSupabaseKey
}

// Renew exchanges a refresh token for a new session. GoTrue rotates the
// refresh token: the answer carries the only copy of the new one, so the
// caller has to persist it immediately or the account is locked out.
//
// An explicitly invalid refresh token is reported as KindAuth. A bare HTTP
// refusal does not establish that the token is invalid.
func (c *Client) Renew(ctx context.Context, creds Creds, refreshToken string) (Session, error) {
	if creds.ProxyURL != "" {
		ctx = WithProxy(ctx, creds.ProxyURL)
	}
	rt := strings.TrimSpace(refreshToken)
	if rt == "" {
		return Session{}, &Error{Kind: KindAuth, Code: "no_refresh_token", Message: "the account has no refresh token; sign in again"}
	}
	body, _ := json.Marshal(map[string]string{"refresh_token": rt})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, creds.url()+"/auth/v1/token?grant_type=refresh_token", strings.NewReader(string(body)))
	if err != nil {
		return Session{}, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("apikey", creds.key())
	req.Header.Set("Authorization", "Bearer "+creds.key())
	var in SignIn
	if err := c.do(ctx, req, &in); err != nil {
		return Session{}, classifyRefreshError(err)
	}
	if strings.TrimSpace(in.AccessToken) == "" {
		return Session{}, &Error{Kind: KindUnknown, Message: "the refresh answered without an access token"}
	}
	s := in.session(c.now())
	if s.RefreshToken == "" {
		// GoTrue rotates, so this should not happen; keeping the spent token
		// would schedule a refresh that is certain to fail.
		return Session{}, &Error{Kind: KindUnknown, Message: "the refresh answered without a refresh token"}
	}
	return s, nil
}

// User reads the signed-in user's profile. It is the cheap check that says
// whether an access token still works without spending any allowance.
func (c *Client) User(ctx context.Context, creds Creds, accessToken string) (Session, error) {
	if creds.ProxyURL != "" {
		ctx = WithProxy(ctx, creds.ProxyURL)
	}
	if strings.TrimSpace(accessToken) == "" {
		return Session{}, &Error{Kind: KindAuth, Message: "the account has no access token"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, creds.url()+"/auth/v1/user", nil)
	if err != nil {
		return Session{}, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("apikey", creds.key())
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var u struct {
		ID            string         `json:"id"`
		Email         string         `json:"email"`
		UserMetadata  map[string]any `json:"user_metadata"`
		AppMetadata   map[string]any `json:"app_metadata"`
		EmailVerified bool           `json:"email_verified"`
	}
	if err := c.do(ctx, req, &u); err != nil {
		return Session{}, err
	}
	s := Session{UserID: u.ID, Email: u.Email}
	if s.Email == "" {
		if v, ok := u.UserMetadata["email"].(string); ok {
			s.Email = v
		}
	}
	if v, ok := u.AppMetadata["provider"].(string); ok {
		s.Provider = v
	}
	return s, nil
}

// ---- usage meter -------------------------------------------------------------

// Meter is what the Settings → Usage page shows, as
// perch_ai_my_inference_usage_meter reports it. Perch states each figure in
// USD; PtOf converts one to Perch's PT (a thousandth of a dollar, rounded up).
type Meter struct {
	MonthlyUSD       float64
	MonthlyManualUSD float64
	DailyUSD         float64
	WeeklyUSD        float64
	DailyCalls       int64
	WeeklyCalls      int64
	MonthlyCalls     int64
	WeeklyTokens     int64
	MonthlyTokens    int64
	// PeriodStart is when the month's allowance began; the reset is a month
	// on. Empty when Perch did not say.
	PeriodStart string
	// Raw is Perch's own answer, kept for the console's "raw" view; the
	// pool's own fields above are what it shows.
	Raw map[string]any

	// The account route states spend in PT directly. Those figures are exact
	// (no USD round-trip), so they win over the converted ones when present.
	MonthlyPTExact, ManualPTExact, WeeklyPTExact, DailyPTExact int64
	MonthlyPTKnown, ManualPTKnown, WeeklyPTKnown, DailyPTKnown bool
}

// PtOf converts USD to Perch's PT unit.
func PtOf(usd float64) int64 {
	if usd <= 0 {
		return 0
	}
	return int64(usd*PTPerUSD + 0.999999)
}

// ResetAt is the day the allowance rolls over: a month after the period
// began. Zero when Perch did not name a readable start.
func (m Meter) ResetAt() time.Time {
	at, err := time.Parse(time.RFC3339, strings.TrimSpace(m.PeriodStart))
	if err != nil {
		return time.Time{}
	}
	return at.AddDate(0, 1, 0)
}

// Usage reads the allowance through the Postgres function the app's own
// Settings → Usage page calls. It costs no model allowance: it is a read of
// recorded spend, not a turn.
//
// The user's own token is what answers; the app's REST route refuses the
// CLI surface, which is why the RPC is what this calls.
func (c *Client) Usage(ctx context.Context, creds Creds, accessToken string) (Meter, error) {
	if creds.ProxyURL != "" {
		ctx = WithProxy(ctx, creds.ProxyURL)
	}
	if strings.TrimSpace(accessToken) == "" {
		return Meter{}, &Error{Kind: KindAuth, Message: "the account has no access token"}
	}
	now := c.now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	weekStart := dayStart.AddDate(0, 0, -7)
	body, _ := json.Marshal(map[string]string{
		"p_day_start":  dayStart.UTC().Format(time.RFC3339Nano),
		"p_week_start": weekStart.UTC().Format(time.RFC3339Nano),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, creds.url()+"/rest/v1/rpc/perch_ai_my_inference_usage_meter", strings.NewReader(string(body)))
	if err != nil {
		return Meter{}, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("apikey", creds.key())
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var raw map[string]any
	if err := c.do(ctx, req, &raw); err != nil {
		return Meter{}, err
	}
	if raw == nil {
		return Meter{}, &Error{Kind: KindUnknown, Message: "the usage meter answered nothing"}
	}
	return Meter{
		MonthlyUSD:       num(raw["monthly_usd"]),
		MonthlyManualUSD: num(raw["monthly_manual_usd"]),
		DailyUSD:         num(raw["daily_usd"]),
		WeeklyUSD:        num(raw["weekly_usd"]),
		DailyCalls:       int64(num(raw["daily_calls"])),
		WeeklyCalls:      int64(num(raw["weekly_calls"])),
		MonthlyCalls:     int64(num(raw["monthly_calls"])),
		WeeklyTokens:     int64(num(raw["weekly_tokens"])),
		MonthlyTokens:    int64(num(raw["monthly_tokens"])),
		PeriodStart:      str(raw["period_start"]),
		Raw:              raw,
	}, nil
}

// ---- sign-in config ----------------------------------------------------------

// Config is /api/perch-terminal/cli-auth/config: where the app's Supabase is,
// which OAuth providers are on, and where a loopback callback may point.
type Config struct {
	AppURL       string
	SupabaseURL  string
	SupabaseKey  string
	RedirectHost string
	Providers    []string
}

// SignInConfig reads the CLI sign-in config. It is used only to fill in a
// Supabase URL or key an account did not carry; a failure is not fatal.
func (c *Client) SignInConfig(ctx context.Context, appURL string) (Config, error) {
	base := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if base == "" {
		base = AppURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/perch-terminal/cli-auth/config", nil)
	if err != nil {
		return Config{}, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	var raw struct {
		OK           bool     `json:"ok"`
		AppURL       string   `json:"appUrl"`
		SupabaseURL  string   `json:"supabaseUrl"`
		SupabaseKey  string   `json:"supabaseAnonKey"`
		RedirectHost string   `json:"redirectHost"`
		Providers    []string `json:"providers"`
		ErrorCode    string   `json:"errorCode"`
		ErrorMessage string   `json:"error"`
	}
	if err := c.do(ctx, req, &raw); err != nil {
		return Config{}, err
	}
	if !raw.OK && raw.SupabaseURL == "" {
		return Config{}, &Error{Kind: KindUnknown, Message: first(raw.ErrorMessage, "Perch gave no sign-in config")}
	}
	return Config{
		AppURL:       first(raw.AppURL, base),
		SupabaseURL:  raw.SupabaseURL,
		SupabaseKey:  first(raw.SupabaseKey),
		RedirectHost: first(raw.RedirectHost, "127.0.0.1"),
		Providers:    raw.Providers,
	}, nil
}

// ---- hosted models -----------------------------------------------------------

// StarterMonthlyUSD reads /api/perch-terminal/starter-limits: the monthly
// allowance Perch's own client shows for the free plan, in dollars.
//
// This is the only route that states that figure, and it answers the account's
// own access token (unlike the billing routes, which take a browser cookie
// only). It is what keeps the Starter cap Perch's number rather than a
// constant in this program: when Perch changes the free plan, the console
// follows.
func (c *Client) StarterMonthlyUSD(ctx context.Context, appURL, accessToken string) (float64, error) {
	base := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if base == "" {
		base = AppURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/perch-terminal/starter-limits", nil)
	if err != nil {
		return 0, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	var raw struct {
		OK     bool `json:"ok"`
		Config struct {
			MonthlyUSD float64 `json:"monthlyUsd"`
		} `json:"config"`
	}
	if err := c.do(ctx, req, &raw); err != nil {
		return 0, err
	}
	if !raw.OK {
		return 0, &Error{Kind: KindUnknown, Message: "starter-limits answered ok=false"}
	}
	return raw.Config.MonthlyUSD, nil
}

// CliContext is /api/perch-terminal/cli-context: the identity the CLI attaches
// to a turn.
//
// This is where workspaceId comes from, and it is the only route that states
// it for an account. The published client resolves it exactly this way (its
// vte / Z8i): POST {query:"", includeIndex:false}, read session.userId and
// session.workspaceId from the answer, and fall back to the token's own user id
// when the route omits one. It is the same host and token as turn-ticket, so a
// session that can run a turn can answer this; there is no control key and no
// /api/v1/workspaces/current here (that route belongs to a different pool).
//
// A missing workspaceId is reported as such rather than guessed: the
// attribution block the model call carries is dropped entirely when either id
// is absent, which is what the published client does too.
type CliContext struct {
	// UserID is session.userId, or the token's own subject when Perch omitted
	// it.
	UserID string
	// WorkspaceID is session.workspaceId. Empty means Perch stated none.
	WorkspaceID string
}

// CliContext reads the account's cli-context. The request body matches the
// client's (query "" and no index): asking for an index would make Perch do
// work this program does not use.
func (c *Client) CliContext(ctx context.Context, appURL, accessToken string) (CliContext, error) {
	base := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if base == "" {
		base = AppURL
	}
	if strings.TrimSpace(accessToken) == "" {
		return CliContext{}, &Error{Kind: KindAuth, Message: "the account has no access token"}
	}
	body, _ := json.Marshal(map[string]any{"query": "", "includeIndex": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/perch-terminal/cli-context", strings.NewReader(string(body)))
	if err != nil {
		return CliContext{}, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var raw struct {
		OK      bool `json:"ok"`
		Session struct {
			UserID      string `json:"userId"`
			WorkspaceID string `json:"workspaceId"`
		} `json:"session"`
	}
	if err := c.do(ctx, req, &raw); err != nil {
		return CliContext{}, err
	}
	if !raw.OK {
		return CliContext{}, &Error{Kind: KindUnknown, Message: "cli-context answered ok=false"}
	}
	out := CliContext{
		UserID:      strings.TrimSpace(raw.Session.UserID),
		WorkspaceID: strings.TrimSpace(raw.Session.WorkspaceID),
	}
	// The client falls back to the token's subject when the route omits a user.
	if out.UserID == "" {
		if id, ok := userIDOf(accessToken); ok {
			out.UserID = id
		}
	}
	return out, nil
}

// userIDOf reads the `sub` claim out of an access token. It is not a
// verification: the token already came from a successful renewal, and this is
// only used to fill a field the server chose not to repeat.
func userIDOf(token string) (string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", false
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(raw, &claims) != nil {
		return "", false
	}
	sub := strings.TrimSpace(claims.Sub)
	return sub, sub != ""
}

// modelDefault is /api/perch-terminal/public-model-default: the model Perch
// would pick for an account with no preference.
//
// The answer nests the ids under `selection` (one field per worker role, all
// the same model unless the account tuned them), so the model is read from
// there and not from the top level: a flat read finds nothing and hands back
// an empty name that looks like success.
func (c *Client) DefaultModel(ctx context.Context, appURL, accessToken string) (string, error) {
	base := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if base == "" {
		base = AppURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/perch-terminal/public-model-default", nil)
	if err != nil {
		return "", &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	var raw map[string]any
	if err := c.do(ctx, req, &raw); err != nil {
		return "", err
	}
	sel, _ := raw["selection"].(map[string]any)
	return first(
		str(sel["chatModelId"]), str(sel["supervisorModelId"]), str(sel["thinkingModelId"]),
		str(sel["fastWorkerModelId"]), str(sel["codeWorkerModelId"]),
		// older or flat answers still name it at the top level
		str(raw["name"]), str(raw["model"]), str(raw["id"]),
	), nil
}

// Model is one entry of Perch's hosted-model catalogue.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"label"`
	// ContextTokens is the window Perch states for the model, 0 when unknown.
	ContextTokens int `json:"contextTokens"`
	// Locked / Requires mark a model the account's plan does not include in
	// the app UI. The model-call proxy still serves it: an account with
	// limits.allowed_models = ["*"] may call any id, which is what this pool
	// schedules on. Kept so the console can show the difference.
	Locked   bool   `json:"locked"`
	Requires string `json:"requires"`
}

// Catalog is Perch's hosted-model catalogue.
type Catalog struct {
	Tiers  []string `json:"tiers"`
	Models []Model  `json:"models"`
}

// Models reads /api/perchai/models: the catalogue the web app builds its
// model picker from, as this account sees it (tier locks included). This is
// the only endpoint that lists Perch's models — the pool has no
// OpenAI-compatible /models to forward.
func (c *Client) Models(ctx context.Context, appURL, accessToken, tier string) (*Catalog, error) {
	base := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if base == "" {
		base = AppURL
	}
	u := base + "/api/perchai/models"
	if tier != "" {
		u += "?tier=" + url.QueryEscape(tier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	var raw struct {
		OK    bool `json:"ok"`
		Tiers []struct {
			ID string `json:"id"`
		} `json:"tiers"`
		Models []Model `json:"models"`
	}
	if err := c.do(ctx, req, &raw); err != nil {
		return nil, err
	}
	out := &Catalog{Models: raw.Models}
	for _, t := range raw.Tiers {
		out.Tiers = append(out.Tiers, t.ID)
	}
	return out, nil
}

// QueryEscape is exposed for callers building a Perch URL.
func QueryEscape(v string) string { return url.QueryEscape(v) }

// IsWeakRefreshError reports whether a refused refresh may simply mean
// "another client spent this token first". GoTrue names that case
// refresh_token_already_used; older answers put it in the message.
func IsWeakRefreshError(err error) bool {
	var pe *Error
	if !errors.As(err, &pe) {
		return false
	}
	if pe.Kind != KindAuth {
		return false
	}
	why := strings.ToLower(pe.Code + " " + pe.Message)
	for _, needle := range []string{"already_used", "invalid_grant", "refresh_token_not_found", "not valid", "validation_failed"} {
		if strings.Contains(why, needle) {
			return true
		}
	}
	return false
}

// ---- account: plan, entitlements and the authoritative usage meter -----------

// AllowanceSource names where a monthly cap came from. A cap that Perch
// actually stated is authoritative; one derived from the plan code is Perch's
// own fallback (its client applies the same one), and "" means there is none.
type AllowanceSource string

const (
	AllowanceEntitlement  AllowanceSource = "entitlement"
	AllowancePlanFallback AllowanceSource = "plan_fallback"
	AllowanceNone         AllowanceSource = "none"
)

// Account is GET /api/perchai/account: the plan the account is on, its
// entitlements, and the usage meter the app's Settings → Usage page reads.
//
// This is the endpoint Perch's own client uses for limits, and it carries what
// the usage RPC alone cannot: the entitlement list (which states the monthly
// cap) and the credit balance.
type Account struct {
	PlanCode  string
	PlanName  string
	PlanLabel string
	// TierSelectionRequired: a new account that has not picked a plan yet.
	// Perch then states no plan code and NO entitlements, so there is no
	// allowance to read at all — the console says so instead of showing a
	// cap, and the account cannot be used for model calls until its owner
	// picks a plan on the website (the billing routes that would do it take a
	// browser cookie, not this access token, so this program cannot).
	TierSelectionRequired bool
	// MembershipRole is "admin"/"internal" for staff accounts, which are
	// unlimited.
	MembershipRole string
	// Unlimited: an internal account, or a roost entitlement marked unlimited.
	Unlimited bool
	// RoostUnlimited: the "usage.roost" entitlement says so.
	RoostUnlimited bool
	// MonthlyLimitPT is the month's cap: the entitlement when it names one,
	// else Perch's plan-code fallback, else 0 with Source "none".
	MonthlyLimitPT int64
	MonthlySource  AllowanceSource
	// StarterCapPT is /api/perch-terminal/starter-limits converted to PT, when
	// the caller read it. It outranks the published constant so the free
	// tier's figure is always Perch's own.
	StarterCapPT int64
	// ManualLimitPT is the cap on manual (non-plan) model calls.
	ManualLimitPT int64
	ManualSource  AllowanceSource
	// CreditBalancePT is purchased credit, which sits outside the plan.
	CreditBalancePT int64
	// Meter is the usage figures, in USD (as Perch states them).
	Meter Meter
	// MeterPresent: the answer carried a usageMeter object.
	MeterPresent bool
	// Entitlements is the raw list, kept for the console's detail view.
	Entitlements []Entitlement
	Raw          map[string]any
}

// Entitlement is one entry of session.entitlements.
type Entitlement struct {
	Key   string
	Value map[string]any
}

// entitlementLimit reads "limit" out of an entitlement's value_json. present
// distinguishes "the entitlement exists but states no cap" from "absent",
// which is what decides whether the plan fallback applies.
func (a Account) entitlementLimit(key string) (limit int64, present bool) {
	for _, e := range a.Entitlements {
		if e.Key != key {
			continue
		}
		present = true
		if v, ok := numOK(e.Value["limit"]); ok && v > 0 {
			limit = int64(v)
		}
		return limit, present
	}
	return 0, false
}

// planIsStarter: pilot and demo are what Perch labels Starter.
func planIsStarter(code string) bool { return code == "pilot" || code == "demo" }

// manualPlanFallback is Perch's own per-plan cap for manual model calls.
func manualPlanFallback(code string) (int64, bool) {
	switch code {
	case "ultra":
		return 225_000, true
	case "pro":
		return 75_000, true
	}
	return 0, false
}

// StarterMonthlyPT is the fallback cap used only when Perch could not be asked
// for the real one (StarterMonthlyLimits failed). It is Perch's published
// free-tier figure, kept so the console has something to say rather than
// nothing, and it is labelled as an estimate wherever it is shown.
const StarterMonthlyPT = 20_000

// StarterMonthlyLimits is the cap Perch itself states for the free plan,
// filled in by the caller that read /api/perch-terminal/starter-limits. Zero
// means "not read".
//
// It is a field rather than a constant because Perch owns that number: the
// route reports it per install, and hardcoding it here would go stale the day
// Perch changes its free tier.
func (a *Account) StarterMonthlyLimits(monthlyUSD float64) {
	if monthlyUSD > 0 {
		a.StarterCapPT = PtOf(monthlyUSD)
	}
}

// ResolveAllowances fills the caps from the entitlement list, applying Perch's
// own fallback chain: an entitlement that states a limit wins; otherwise the
// free plan's allowance (Perch's own figure when it was read, else the
// published constant); a plan that states nothing gets no cap — the console
// then says the headroom is unknown rather than inventing one.
//
// An account that has not picked a plan yet (TierSelectionRequired) gets NO
// cap even though Perch would call it Starter: its owner has not chosen, so
// claiming an allowance for it would be a guess dressed as a fact. The console
// shows it as "awaiting plan selection" instead.
func (a *Account) ResolveAllowances() {
	if lim, present := a.entitlementLimit("usage.monthly_pt"); present && lim > 0 {
		a.MonthlyLimitPT, a.MonthlySource = lim, AllowanceEntitlement
	} else if a.TierSelectionRequired {
		a.MonthlyLimitPT, a.MonthlySource = 0, AllowanceNone
	} else if planIsStarter(a.PlanCode) || strings.EqualFold(strings.TrimSpace(a.PlanName), "Starter") {
		switch {
		case a.StarterCapPT > 0:
			a.MonthlyLimitPT, a.MonthlySource = a.StarterCapPT, AllowancePlanFallback
		default:
			a.MonthlyLimitPT, a.MonthlySource = StarterMonthlyPT, AllowancePlanFallback
		}
	} else {
		a.MonthlyLimitPT, a.MonthlySource = 0, AllowanceNone
	}

	if lim, present := a.entitlementLimit("usage.manual_monthly_pt"); present && lim > 0 {
		a.ManualLimitPT, a.ManualSource = lim, AllowanceEntitlement
	} else if lim, ok := manualPlanFallback(a.PlanCode); ok {
		a.ManualLimitPT, a.ManualSource = lim, AllowancePlanFallback
	} else {
		a.ManualLimitPT, a.ManualSource = 0, AllowanceNone
	}

	for _, e := range a.Entitlements {
		if e.Key != "usage.roost" {
			continue
		}
		if v, ok := e.Value["unlimited"].(bool); ok && v {
			a.RoostUnlimited = true
		}
	}
	switch a.MembershipRole {
	case "admin", "internal":
		a.Unlimited = true
	}
	if a.PlanCode == "internal" {
		a.Unlimited = true
	}
}

// planLabelOf names the plan the way Perch's client does.
//
// An account that states neither a plan name nor a code has not picked one
// (tierSelectionRequired), so this returns "" rather than "Starter": naming a
// plan the account has not chosen would label an open state as settled, and
// the console shows "awaiting plan selection" instead.
func planLabelOf(planName, planCode string) string {
	if s := strings.TrimSpace(planName); s != "" {
		if strings.EqualFold(s, "pilot") {
			return "Starter"
		}
		return s
	}
	if planCode == "pilot" {
		return "Starter"
	}
	if s := strings.TrimSpace(planCode); s != "" {
		return strings.ToUpper(s[:1]) + s[1:]
	}
	return ""
}

// numOK reads a JSON number that may also arrive as a string.
func numOK(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

// Account reads the plan, entitlements and usage meter.
//
// It is the same call Perch's client makes to fill its usage card, so the caps
// it reports are the ones the product itself applies.
func (c *Client) Account(ctx context.Context, appURL, accessToken string) (Account, error) {
	if strings.TrimSpace(accessToken) == "" {
		return Account{}, &Error{Kind: KindAuth, Message: "the account has no access token"}
	}
	base := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if base == "" {
		base = AppURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/perchai/account", nil)
	if err != nil {
		return Account{}, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var raw struct {
		OK      bool           `json:"ok"`
		Error   string         `json:"error"`
		Session map[string]any `json:"session"`
		Usage   map[string]any `json:"usageMeter"`
		Credit  any            `json:"creditBalancePt"`
	}
	if err := c.do(ctx, req, &raw); err != nil {
		return Account{}, err
	}
	if !raw.OK || raw.Session == nil {
		return Account{}, &Error{Kind: KindUnknown, Message: first(raw.Error, "Perch returned no account")}
	}
	out := Account{
		PlanCode:       strings.TrimSpace(str(raw.Session["planCode"])),
		PlanName:       strings.TrimSpace(str(raw.Session["planName"])),
		MembershipRole: strings.TrimSpace(str(raw.Session["membershipRole"])),
	}
	if v, ok := raw.Session["tierSelectionRequired"].(bool); ok {
		out.TierSelectionRequired = v
	}
	out.PlanLabel = planLabelOf(out.PlanName, out.PlanCode)
	if list, ok := raw.Session["entitlements"].([]any); ok {
		for _, item := range list {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			key := strings.TrimSpace(str(m["key"]))
			if key == "" {
				continue
			}
			val, _ := m["value_json"].(map[string]any)
			if val == nil {
				val = map[string]any{}
			}
			out.Entitlements = append(out.Entitlements, Entitlement{Key: key, Value: val})
		}
	}
	if raw.Usage != nil {
		out.MeterPresent = true
		out.Meter = Meter{
			MonthlyUSD:       num(raw.Usage["monthlyUsd"]),
			MonthlyManualUSD: num(raw.Usage["monthlyManualPt"]) / PTPerUSD,
			DailyUSD:         num(raw.Usage["dailyUsd"]),
			WeeklyUSD:        num(raw.Usage["weeklyUsd"]),
			DailyCalls:       int64(num(raw.Usage["dailyCalls"])),
			WeeklyCalls:      int64(num(raw.Usage["weeklyCalls"])),
			MonthlyCalls:     int64(num(raw.Usage["monthlyCalls"])),
			WeeklyTokens:     int64(num(raw.Usage["weeklyTokens"])),
			MonthlyTokens:    int64(num(raw.Usage["monthlyTokens"])),
			PeriodStart:      str(raw.Usage["periodStart"]),
			Raw:              raw.Usage,
		}
		// the account route states PT directly; keep the PT figures exact
		if v, ok := numOK(raw.Usage["monthlyPt"]); ok {
			out.Meter.MonthlyPTExact, out.Meter.MonthlyPTKnown = int64(v), true
		}
		if v, ok := numOK(raw.Usage["monthlyManualPt"]); ok {
			out.Meter.ManualPTExact, out.Meter.ManualPTKnown = int64(v), true
		}
		if v, ok := numOK(raw.Usage["weeklyPt"]); ok {
			out.Meter.WeeklyPTExact, out.Meter.WeeklyPTKnown = int64(v), true
		}
		if v, ok := numOK(raw.Usage["dailyPt"]); ok {
			out.Meter.DailyPTExact, out.Meter.DailyPTKnown = int64(v), true
		}
	}
	if v, ok := numOK(raw.Credit); ok && v > 0 {
		out.CreditBalancePT = int64(v)
	}
	out.ResolveAllowances()
	return out, nil
}

// ---- privacy: the data-program switch the Settings page toggles -------------

// DataProgramPath is where the web app's Settings page writes the account's
// data-program (training-data/privacy) switch.
const DataProgramPath = "/api/perchai/data-program"

// DataProgramUpdate is the body PATCH /api/perchai/data-program takes. The web
// app sends both fields on every toggle: Enabled is the new state, and
// AcknowledgeExistingNotice covers the "you already sent data" case, which the
// app sets only after showing that notice. Turning collection off therefore
// sends acknowledgeExistingNotice=false: the account is not confirming a
// notice nothing showed it.
type DataProgramUpdate struct {
	Enabled bool `json:"enabled"`
	// AcknowledgeExistingNotice is a pointer so that an omission stays an
	// omission; this package only ever sends the explicit false.
	AcknowledgeExistingNotice *bool `json:"acknowledgeExistingNotice,omitempty"`
}

// DataProgramResult is what Perch acknowledged.
type DataProgramResult struct {
	// OK is Perch's own verdict on the update.
	OK bool
	// ResponseCode is Perch's errorCode when the update was refused, for the
	// caller's logs. It is never shown as user-facing text.
	ResponseCode string
}

// PrivacyOff returns the body that turns data collection off.
func PrivacyOff() DataProgramUpdate {
	off := false
	return DataProgramUpdate{Enabled: false, AcknowledgeExistingNotice: &off}
}

// SetDataProgram writes the account's data-program switch.
//
// It is a web-app call, so it carries the account's access token as a Bearer
// and no turn ticket. A refusal is an error even on a 2xx: Perch answers
// ok:false when it declines the change, and reporting that as success would
// say collection is off when it is not.
func (c *Client) SetDataProgram(ctx context.Context, appURL, accessToken string, body DataProgramUpdate) (DataProgramResult, error) {
	if strings.TrimSpace(accessToken) == "" {
		return DataProgramResult{}, &Error{Kind: KindAuth, Message: "the account has no access token"}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return DataProgramResult{}, &Error{Kind: KindRejected, Message: "invalid data-program request", Err: err}
	}
	base := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if base == "" {
		base = AppURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, base+DataProgramPath, bytes.NewReader(raw))
	if err != nil {
		return DataProgramResult{}, &Error{Kind: KindTransient, Message: err.Error(), Err: err}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var reply struct {
		OK        bool   `json:"ok"`
		Error     string `json:"error"`
		ErrorCode string `json:"errorCode"`
	}
	if err := c.do(ctx, req, &reply); err != nil {
		return DataProgramResult{}, err
	}
	res := DataProgramResult{OK: reply.OK, ResponseCode: first(reply.ErrorCode, wireCode(reply.Error))}
	if !res.OK {
		return res, &Error{Kind: KindRejected, Code: res.ResponseCode, Message: first(reply.Error, "Perch did not acknowledge the data-program update")}
	}
	return res, nil
}

// wireCode returns s when it reads as a machine code rather than a sentence:
// Perch puts short codes in `error` for some refusals and prose in others.
func wireCode(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, " ") || len(s) > 64 {
		return ""
	}
	return s
}
