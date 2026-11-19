// Package factoryauth implements Factory's WorkOS device authorization and
// refresh-token exchanges. It never reads the desktop keychain: a device login
// yields an access token and a refresh token directly, which is the portable
// form an account pool can keep and renew.
package factoryauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// ProductionDefaultBase is WorkOS User Management for Factory production.
	ProductionDefaultBase = "https://api.workos.com/user_management"
	// DevelopmentDefaultBase is the WorkOS User Management host of Factory's
	// non-production environment. Its device codes approve at
	// auth.factory.ai/device too, but its tokens are rejected by
	// api.factory.ai with 401.
	DevelopmentDefaultBase = "https://api.workos.com/user_management"
	// DefaultBase is ProductionDefaultBase, kept for existing callers.
	DefaultBase = ProductionDefaultBase
	// ProductionClientID is Factory's production WorkOS client. It is public
	// (it appears in the OAuth authorization URL), not a secret.
	ProductionClientID = "client_01HNM792M5G5G1A2THWPXKFMXB"
	// DevClientID is Factory's development WorkOS client.
	DevClientID = "client_01HNM7927XNSKCJ4982Z5J3FFZ"
	// DeviceGrant is the OAuth device-code grant type.
	DeviceGrant = "urn:ietf:params:oauth:grant-type:device_code"
)

// Device is one device-authorization response.
type Device struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Token is an exchanged or refreshed credential. ExpiresAt is read from the
// access token's JWT exp when present; otherwise a conservative default is
// used so the caller refreshes before a real 401.
type Token struct {
	AccessToken string
	// AccessTokenRaw is the exact response text of access_token. WorkOS
	// documents it as a JWT, but the opaque value it actually returns is not
	// parseable here, and whoami must receive what WorkOS issued.
	AccessTokenRaw string
	RefreshToken   string
	OrgID          string
	UserID         string
	Email          string
	ExpiresAt      time.Time
}

// OAuthError is WorkOS's structured OAuth error.
type OAuthError struct {
	Code        string
	Description string
	Status      int
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return e.Code + ": " + e.Description
	}
	return e.Code
}

var (
	ErrPending  = errors.New("factoryauth: authorization pending")
	ErrSlowDown = errors.New("factoryauth: slow down")
	ErrExpired  = errors.New("factoryauth: device code expired")
	ErrDenied   = errors.New("factoryauth: access denied")
)

// Client talks to WorkOS. Base and ClientID default to Factory production.
type Client struct {
	Base      string
	ClientID  string
	UserAgent string
	HTTP      *http.Client
}

// EnvironmentOf names the WorkOS environment from a JWT access token. WorkOS
// puts the issuing client in `iss` ("./client_<id>") and the workspace in
// `wids`; Factory's production API only accepts tokens minted for its
// production client, so a development-client token always ends in HTTP 401.
//
// Any claim that is missing or not readable returns "", and an issuer that is
// neither Factory client returns "unknown". Callers must not treat "" as proof
// that the token is production.
func EnvironmentOf(jwt string) string {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var raw struct {
		Iss  string   `json:"iss"`
		Wids []string `json:"wids"`
	}
	if json.Unmarshal(payload, &raw) != nil {
		return ""
	}
	if raw.Iss != "" {
		iss := strings.TrimSuffix(raw.Iss, "/")
		if i := strings.LastIndex(iss, "/"); i >= 0 {
			iss = iss[i+1:]
		}
		iss = strings.TrimPrefix(iss, "client_")
		switch iss {
		case strings.TrimPrefix(ProductionClientID, "client_"):
			return "production"
		case strings.TrimPrefix(DevClientID, "client_"):
			return "development"
		}
	}
	return "unknown"
}

func (c *Client) base() string {
	if strings.TrimSpace(c.Base) != "" {
		return strings.TrimRight(c.Base, "/")
	}
	return ProductionDefaultBase
}

func (c *Client) clientID() string {
	if strings.TrimSpace(c.ClientID) != "" {
		return c.ClientID
	}
	return ProductionClientID
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second}
}

func (c *Client) post(ctx context.Context, path string, form url.Values) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, errors.New("factoryauth: invalid request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	// WorkOS is called by droid's Bun runtime, not by a bare Go client.
	ua := c.UserAgent
	if strings.TrimSpace(ua) == "" {
		ua = "Bun/1.4.4"
	}
	req.Header.Set("User-Agent", ua)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, errors.New("factoryauth: network request failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return nil, resp.StatusCode, errors.New("factoryauth: response incomplete or too large")
	}
	return b, resp.StatusCode, nil
}

// StartDevice asks WorkOS for a device code. The user approves the returned
// code at VerificationURI while this process polls.
func (c *Client) StartDevice(ctx context.Context) (Device, error) {
	b, st, err := c.post(ctx, "/authorize/device", url.Values{"client_id": {c.clientID()}})
	if err != nil {
		return Device{}, err
	}
	var d Device
	if st != http.StatusOK || json.Unmarshal(b, &d) != nil || d.DeviceCode == "" || d.UserCode == "" || d.VerificationURI == "" {
		return Device{}, fmt.Errorf("factoryauth: device authorization failed (HTTP %d)", st)
	}
	if d.ExpiresIn <= 0 {
		d.ExpiresIn = 300
	}
	if d.Interval <= 0 {
		d.Interval = 5
	}
	return d, nil
}

// PollDevice exchanges a device code once. ErrPending/ErrSlowDown are normal
// polling results; ErrExpired/ErrDenied are terminal.
func (c *Client) PollDevice(ctx context.Context, deviceCode string) (Token, error) {
	b, st, err := c.post(ctx, "/authenticate", url.Values{
		"grant_type":  {DeviceGrant},
		"device_code": {deviceCode},
		"client_id":   {c.clientID()},
	})
	if err != nil {
		return Token{}, err
	}
	if st == http.StatusOK {
		return parseToken(b, "", "")
	}
	var oe struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(b, &oe)
	switch oe.Error {
	case "authorization_pending":
		return Token{}, ErrPending
	case "slow_down":
		return Token{}, ErrSlowDown
	case "expired_token":
		return Token{}, ErrExpired
	case "access_denied":
		return Token{}, ErrDenied
	}
	return Token{}, &OAuthError{Code: oe.Error, Description: oe.Description, Status: st}
}

// Refresh exchanges a refresh token. WorkOS may rotate the refresh token; a
// response without one keeps the caller's existing token.
func (c *Client) Refresh(ctx context.Context, refreshToken, orgID string) (Token, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Token{}, errors.New("factoryauth: refresh token is empty")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.clientID()},
	}
	if orgID != "" {
		form.Set("organization_id", orgID)
	}
	b, st, err := c.post(ctx, "/authenticate", form)
	if err != nil {
		return Token{}, err
	}
	if st == http.StatusOK {
		return parseToken(b, refreshToken, orgID)
	}
	var oe struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(b, &oe)
	switch oe.Error {
	case "invalid_grant", "expired_token", "access_denied":
		return Token{}, ErrDenied
	}
	return Token{}, &OAuthError{Code: oe.Error, Description: oe.Description, Status: st}
}

func parseToken(b []byte, fallbackRefresh, fallbackOrg string) (Token, error) {
	var doc struct {
		AccessToken          string `json:"access_token"`
		AccessTokenAlt       string `json:"token"`
		RefreshToken         string `json:"refresh_token"`
		ActiveOrganizationID string `json:"active_organization_id"`
		OrganizationID       string `json:"organization_id"`
		ExpiresIn            int    `json:"expires_in"`
		User                 *struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return Token{}, errors.New("factoryauth: invalid token response")
	}
	rawAccess := first(doc.AccessToken, doc.AccessTokenAlt)
	if rawAccess == "" {
		return Token{}, errors.New("factoryauth: token response has no access token")
	}
	claims := jwtClaims(rawAccess)
	userID, email := "", ""
	if doc.User != nil {
		userID, email = doc.User.ID, doc.User.Email
	}
	t := Token{
		AccessToken:    strings.TrimSpace(rawAccess),
		AccessTokenRaw: rawAccess,
		RefreshToken:   doc.RefreshToken,
		OrgID:          first(doc.ActiveOrganizationID, doc.OrganizationID, claims["org_id"], claims["organization_id"], fallbackOrg),
		UserID:         first(userID, claims["sub"], claims["user_id"]),
		Email:          first(email, claims["email"]),
	}
	if t.RefreshToken == "" {
		t.RefreshToken = fallbackRefresh
	}
	if exp := claims["exp"]; exp != "" {
		if n, err := parseInt(exp); err == nil && n > 0 {
			t.ExpiresAt = time.Unix(n, 0)
		}
	}
	if t.ExpiresAt.IsZero() {
		ttl := doc.ExpiresIn
		if ttl <= 0 {
			ttl = 3000 // 50 minutes; conservative vs. a typical one-hour token
		}
		t.ExpiresAt = time.Now().Add(time.Duration(ttl) * time.Second)
	}
	return t, nil
}

func first(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseInt(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscan(s, &n)
	return n, err
}

// jwtClaims decodes a JWT payload without verifying its signature. It is used
// only for non-security metadata (exp, email, subject); the upstream verifies
// the token on every call.
func jwtClaims(token string) map[string]string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var raw map[string]any
	if json.Unmarshal(payload, &raw) != nil {
		return nil
	}
	out := map[string]string{}
	for _, k := range []string{"exp", "sub", "user_id", "email", "org_id", "organization_id"} {
		switch v := raw[k].(type) {
		case string:
			out[k] = v
		case float64:
			out[k] = fmt.Sprintf("%.0f", v)
		}
	}
	return out
}

// JWTClaim reads one unverified claim from a WorkOS access token. The token is
// never validated here; callers only use it to name a rejected account.
func JWTClaim(token, name string) string { return jwtClaims(token)[name] }

// Get performs an authenticated WorkOS User Management GET. It exists for the
// organization lookups a device login needs; token exchange stays on post.
func (c *Client) Get(ctx context.Context, path, accessToken string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base()+path, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	ua := c.UserAgent
	if strings.TrimSpace(ua) == "" {
		ua = "Bun/1.4.4"
	}
	req.Header.Set("User-Agent", ua)
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}
