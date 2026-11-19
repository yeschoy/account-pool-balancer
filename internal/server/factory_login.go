package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factoryauth"
)

// factoryLoginFlow is one WorkOS device authorization shown in the console.
// deviceCode never leaves the server; the view exposes only the user-facing
// code and the approval URL.
type factoryLoginFlow struct {
	ID                      string    `json:"id"`
	UserCode                string    `json:"userCode"`
	VerificationURI         string    `json:"verificationUri"`
	VerificationURIComplete string    `json:"verificationUriComplete,omitempty"`
	ExpiresAt               time.Time `json:"expiresAt"`
	Status                  string    `json:"status"` // pending | approved | expired | failed | cancelled
	Error                   string    `json:"error,omitempty"`
	AccountID               string    `json:"accountId,omitempty"`
	Email                   string    `json:"email,omitempty"`
	Updated                 bool      `json:"updated,omitempty"`
	// Diagnostic explains a failed token exchange in terms the operator can
	// act on (wrong WorkOS client, expired token, proxy egress). It never
	// contains a token.
	Diagnostic string `json:"diagnostic,omitempty"`

	deviceCode    string
	interval      int
	group         string
	proxyURL      string
	userAgent     string
	targetAccount string
	cancel        context.CancelFunc
}

type factoryLoginFlows struct {
	mu    sync.Mutex
	flows map[string]*factoryLoginFlow
}

func newFactoryLoginFlows() *factoryLoginFlows {
	return &factoryLoginFlows{flows: map[string]*factoryLoginFlow{}}
}

func (f *factoryLoginFlows) view(id string) (factoryLoginFlow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.flows[id]
	if v == nil {
		return factoryLoginFlow{}, false
	}
	return *v, true
}

func (s *Server) factoryAuthClient(proxy string) *factoryauth.Client {
	base := s.factoryAuthBase
	if base == "" {
		base = factoryauth.DefaultBase
	}
	cfg := s.cfg.Get()
	key := factoryTransportPrefix + "auth:" + proxy
	return &factoryauth.Client{Base: base, UserAgent: cfg.Factory.EffectiveManagementUserAgent(), HTTP: &http.Client{Transport: s.factoryTransportFor(proxy, cfg.Factory.EffectiveRouting(), key)}}
}

// apiFactoryLoginStart begins a WorkOS device authorization. The browser
// approval happens outside this server; the background poll adds the account
// when WorkOS reports the code approved.
func (s *Server) apiFactoryLoginStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Group     string `json:"group"`
		ProxyURL  string `json:"proxyUrl"`
		UserAgent string `json:"userAgent"`
		AccountID string `json:"accountId"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	cfg := s.cfg.Get()
	if !cfg.PoolEnabled(config.ProviderFactory) {
		apiError(w, http.StatusServiceUnavailable, errors.New("Factory号池已关闭"))
		return
	}
	flowID := randHex(8)
	proxy, ua := s.factoryEgressFor(cfg, "login-"+flowID)
	if p := strings.TrimSpace(in.ProxyURL); p != "" {
		proxy = p
	}
	if u := strings.TrimSpace(in.UserAgent); u != "" {
		if err := config.ValidateUserAgent(u); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		ua = u
	}
	if in.AccountID != "" {
		a, ok := cfg.Account(in.AccountID)
		if !ok || a.Provider != config.ProviderFactory {
			apiError(w, http.StatusNotFound, errors.New("Factory account not found"))
			return
		}
		proxy, ua = a.ProxyURL, a.UserAgent
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	dev, err := s.factoryAuthClient(proxy).StartDevice(ctx)
	if err != nil {
		apiError(w, http.StatusBadGateway, errors.New("申请 Factory 设备授权失败，请检查网络/代理"))
		return
	}
	bg, bgCancel := context.WithDeadline(s.poolWorkContext(config.ProviderFactory), time.Now().Add(time.Duration(dev.ExpiresIn+30)*time.Second))
	f := &factoryLoginFlow{
		ID: flowID, UserCode: dev.UserCode, VerificationURI: dev.VerificationURI,
		VerificationURIComplete: dev.VerificationURIComplete,
		ExpiresAt:               time.Now().Add(time.Duration(dev.ExpiresIn) * time.Second),
		Status:                  "pending", deviceCode: dev.DeviceCode, interval: max(dev.Interval, 1),
		group: in.Group, proxyURL: proxy, userAgent: ua, targetAccount: in.AccountID, cancel: bgCancel,
	}
	s.factoryLogins.mu.Lock()
	for id, old := range s.factoryLogins.flows {
		if old.Status != "pending" && time.Since(old.ExpiresAt) > time.Hour {
			delete(s.factoryLogins.flows, id)
		}
	}
	s.factoryLogins.flows[f.ID] = f
	s.factoryLogins.mu.Unlock()
	go s.pollFactoryLogin(bg, f, s.factoryAuthClient(proxy))
	v, _ := s.factoryLogins.view(f.ID)
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) pollFactoryLogin(ctx context.Context, f *factoryLoginFlow, c *factoryauth.Client) {
	defer f.cancel()
	setStatus := func(status, msg string) {
		s.factoryLogins.mu.Lock()
		f.Status, f.Error, f.deviceCode = status, msg, ""
		s.factoryLogins.mu.Unlock()
	}
	interval := f.interval
	if interval < 1 {
		interval = 5
	}
	t := time.NewTicker(time.Duration(interval) * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.factoryLogins.mu.Lock()
			if f.Status == "pending" {
				f.Status, f.deviceCode = "expired", ""
			}
			s.factoryLogins.mu.Unlock()
			return
		case <-t.C:
		}
		s.factoryLogins.mu.Lock()
		code, status := f.deviceCode, f.Status
		s.factoryLogins.mu.Unlock()
		if status != "pending" || code == "" {
			return
		}
		pollCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		tok, err := c.PollDevice(pollCtx, code)
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, factoryauth.ErrPending):
			continue
		case errors.Is(err, factoryauth.ErrSlowDown):
			interval += 5
			t.Reset(time.Duration(interval) * time.Second)
			continue
		case errors.Is(err, factoryauth.ErrExpired):
			setStatus("expired", "")
			return
		case errors.Is(err, factoryauth.ErrDenied):
			setStatus("failed", "Factory 拒绝了本次授权")
			return
		default:
			var oe *factoryauth.OAuthError
			if errors.As(err, &oe) && oe.Status >= 400 && oe.Status < 500 {
				setStatus("failed", "Factory 授权失败："+oe.Code)
				return
			}
			continue // transient network error; keep polling until expiry
		}
		userID, email, org, err := s.factoryWhoamiOAuth(ctx, tok, f.proxyURL, f.ID)
		if err != nil {
			s.factoryLogins.mu.Lock()
			f.Diagnostic = s.factoryLoginDiagnostic(tok)
			s.factoryLogins.mu.Unlock()
			setStatus("failed", err.Error())
			return
		}
		accountID, updated, err := s.storeFactoryOAuth(f, tok, userID, email, org)
		s.factoryLogins.mu.Lock()
		f.deviceCode = ""
		if err != nil {
			f.Status, f.Error = "failed", err.Error()
		} else {
			f.Status, f.AccountID, f.Email, f.Updated = "approved", accountID, email, updated
		}
		s.factoryLogins.mu.Unlock()
		return
	}
}

// factoryLoginDiagnostic records the non-secret facts that explain a rejected
// login: which WorkOS client minted the token, its expiry, and whether a proxy
// carried the whoami call.
func (s *Server) factoryLoginDiagnostic(tok factoryauth.Token) string {
	env := factoryauth.EnvironmentOf(tok.AccessTokenRaw)
	parts := []string{"WorkOS客户端=" + env}
	if !tok.ExpiresAt.IsZero() {
		parts = append(parts, "令牌有效期="+tok.ExpiresAt.Format(time.RFC3339))
	}
	if env == "development" {
		parts = append(parts, "该令牌由 Factory 的 development client 签发，api.factory.ai 只接受生产 client 的令牌（401）")
	}
	return strings.Join(parts, "；")
}

// factoryWhoamiOAuth validates a freshly issued access token against Factory's
// whoami and reads the identity the account should be stored under. The request
// mirrors the captured droid call exactly (Bearer, org, whoami-extended, Bun UA,
// Accept */*) and nothing else.
func (s *Server) factoryWhoamiOAuth(ctx context.Context, tok factoryauth.Token, proxy, label string) (userID, email, org string, err error) {
	cfg := s.cfg.Get()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Factory.BaseURL+"/api/cli/whoami", nil)
	if err != nil {
		return "", "", "", errors.New("Factory whoami request failed")
	}
	bearer := tok.AccessToken
	if bearer == "" {
		bearer = tok.AccessTokenRaw
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", cfg.Factory.EffectiveManagementUserAgent())
	req.Header.Set("X-Factory-Whoami-Extended", "true")
	if tok.OrgID != "" {
		req.Header.Set("X-Factory-Org-Id", tok.OrgID)
	}
	resp, err := s.factoryHTTPClient(cfg, proxy).Do(req)
	if err != nil {
		log.Printf("factory login %s: whoami transport error (proxy=%s): %v", label, egressLabel(proxy), err)
		return "", "", "", errors.New("Factory whoami 网络请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		log.Printf("factory login %s: whoami HTTP %d (proxy=%s) body=%q", label, resp.StatusCode, egressLabel(proxy), strings.TrimSpace(string(detail)))
		return "", "", "", fmt.Errorf("Factory whoami HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return "", "", "", errors.New("Factory whoami 响应不完整")
	}
	var who struct {
		UserID string `json:"userId"`
		Email  string `json:"email"`
		OrgID  string `json:"orgId"`
	}
	if json.Unmarshal(b, &who) != nil || who.UserID == "" {
		return "", "", "", errors.New("Factory whoami 缺少身份")
	}
	email = firstNonEmpty(who.Email, tok.Email)
	org = firstNonEmpty(who.OrgID, tok.OrgID)
	return who.UserID, email, org, nil
}

// storeFactoryOAuth adds a device-login account, or replaces the tokens of the
// account being re-authorized or already holding the same WorkOS user id.
func (s *Server) storeFactoryOAuth(f *factoryLoginFlow, tok factoryauth.Token, userID, email, factoryOrg string) (string, bool, error) {
	id, updated := "", false
	_, err := s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			a := &c.Accounts[i]
			if a.Provider != config.ProviderFactory {
				continue
			}
			match := f.targetAccount != "" && a.ID == f.targetAccount
			if !match && f.targetAccount == "" {
				match = a.FactoryRefreshToken != "" && (userID != "" && a.UserID == userID || email != "" && strings.EqualFold(a.Email, email))
			}
			if !match {
				continue
			}
			if f.targetAccount != "" {
				if a.UserID != "" && userID != "" && a.UserID != userID {
					return errors.New("登录的 Factory 用户与要重新授权的账号不一致")
				}
				if a.Email != "" && email != "" && !strings.EqualFold(a.Email, email) {
					return errors.New("登录的 Factory 邮箱与要重新授权的账号不一致")
				}
			}
			a.RuntimeKey, a.FactoryRefreshToken = tok.AccessToken, tok.RefreshToken
			a.FactoryAccessExpires = tok.ExpiresAt
			a.FactoryOrgID = firstNonEmpty(factoryOrg, a.FactoryOrgID)
			a.FactoryWorkOSOrgID = firstNonEmpty(tok.OrgID, a.FactoryWorkOSOrgID)
			if f.userAgent != "" {
				a.UserAgent = f.userAgent
			}
			if userID != "" {
				a.UserID = userID
			}
			if email != "" {
				a.Email = email
			}
			if f.group != "" {
				a.Group = f.group
			}
			if f.proxyURL != "" {
				a.ProxyURL = f.proxyURL
			}
			a.Enabled = true
			id, updated = a.ID, true
			return nil
		}
		if f.targetAccount != "" {
			return errors.New("要重新授权的 Factory 账号已不存在")
		}
		name := firstNonEmpty(email, userID, "Factory OAuth")
		a := config.Account{ID: config.NewID("acc_"), Provider: config.ProviderFactory, Name: name, Email: email, UserID: userID,
			Group: f.group, Enabled: true, RuntimeKey: tok.AccessToken, FactoryRefreshToken: tok.RefreshToken,
			FactoryAccessExpires: tok.ExpiresAt, FactoryOrgID: factoryOrg, FactoryWorkOSOrgID: tok.OrgID, ProxyURL: f.proxyURL, UserAgent: f.userAgent}
		if a.Group == "" {
			a.Group = config.GroupForProvider(config.ProviderFactory)
		}
		c.Accounts = append(c.Accounts, a)
		id = a.ID
		return nil
	})
	if err == nil {
		s.pool.Recover(id)
		s.quota.Forget(id)
	}
	return id, updated, err
}

func (s *Server) apiFactoryLoginPoll(w http.ResponseWriter, r *http.Request) {
	v, ok := s.factoryLogins.view(r.PathValue("flow"))
	if !ok {
		apiError(w, http.StatusNotFound, errors.New("flow not found"))
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) apiFactoryLoginCancel(w http.ResponseWriter, r *http.Request) {
	s.factoryLogins.mu.Lock()
	if f := s.factoryLogins.flows[r.PathValue("flow")]; f != nil && f.Status == "pending" {
		f.Status, f.deviceCode = "cancelled", ""
		f.cancel()
	}
	s.factoryLogins.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// factoryAccountToken renews an OAuth account's access token when it is near
// expiry. A lock per account prevents concurrent requests from rotating the
// refresh token more than once.
func (s *Server) factoryAccountToken(ctx context.Context, cfg *config.Config, a config.Account) (config.Account, error) {
	if a.Provider != config.ProviderFactory || a.FactoryRefreshToken == "" {
		return a, nil
	}
	if a.RuntimeKey != "" && a.FactoryAccessExpires.After(time.Now().Add(2*time.Minute)) {
		return a, nil
	}
	s.factoryTokenMu.Lock()
	if s.factoryTokenLock == nil {
		s.factoryTokenLock = map[string]*sync.Mutex{}
	}
	lock := s.factoryTokenLock[a.ID]
	if lock == nil {
		lock = &sync.Mutex{}
		s.factoryTokenLock[a.ID] = lock
	}
	s.factoryTokenMu.Unlock()
	lock.Lock()
	defer lock.Unlock()
	if cur, ok := s.cfg.Get().Account(a.ID); ok {
		a = cur
	}
	if a.RuntimeKey != "" && a.FactoryAccessExpires.After(time.Now().Add(2*time.Minute)) {
		return a, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tok, err := s.factoryAuthClient(a.ProxyURL).Refresh(rctx, a.FactoryRefreshToken, a.FactoryWorkOSOrgID)
	if err != nil {
		return a, err
	}
	_, uerr := s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID != a.ID {
				continue
			}
			ac := &c.Accounts[i]
			ac.RuntimeKey, ac.FactoryRefreshToken = tok.AccessToken, tok.RefreshToken
			ac.FactoryAccessExpires = tok.ExpiresAt
			ac.FactoryWorkOSOrgID = firstNonEmpty(tok.OrgID, ac.FactoryWorkOSOrgID)
			if ac.UserID == "" {
				ac.UserID = tok.UserID
			}
			if ac.Email == "" {
				ac.Email = tok.Email
			}
			return nil
		}
		return errors.New("Factory OAuth account disappeared")
	})
	if uerr != nil {
		return a, uerr
	}
	a.RuntimeKey, a.FactoryRefreshToken = tok.AccessToken, tok.RefreshToken
	a.FactoryAccessExpires = tok.ExpiresAt
	a.FactoryWorkOSOrgID = firstNonEmpty(tok.OrgID, a.FactoryWorkOSOrgID)
	return a, nil
}
