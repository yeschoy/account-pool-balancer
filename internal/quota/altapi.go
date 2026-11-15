package quota

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/newapiconsole"
)

// altapi.ai is a new-api console. Unlike Deploy, a real balance read exists,
// but only through the console session (the same `session` cookie a logged-in
// browser holds):
//
//	GET /api/status              → quota_per_unit (500000 on altapi.ai)
//	GET /api/user/self           → quota, used_quota, request_count, group
//
// The web UI's three statistics are exactly:
//
//	Current balance      = quota      / quota_per_unit
//	Consumption          = used_quota / quota_per_unit
//	Number of Requests   = request_count
//
// The inference key (Bearer, /v1/*) can NOT read the balance: /api/user/self
// answers `{"message":"无权进行此操作，access token 无效"}`. It can only report
// already-spent money via GET /dashboard/billing/usage, which is what the
// fallback below does — and that is spent, never remaining.
const (
	ALTAPIBalanceReasonNoSession = "该账号未绑定控制台会话（session cookie），余额未知；推理 Key 读不到余额（/api/user/self 返回 access token 无效）"
	ALTAPIBalanceHintNoSession   = "在充值页登录后复制 altapi.ai 的 session cookie 粘贴到账号里，即可读出真实余额（quota / quota_per_unit，与页面 Current balance 一致）"
	ALTAPIBalanceReasonKeyOnly   = "只绑定了推理 Key：可读已用金额，读不到剩余余额"
)

// ALTAPIState is the console read kept in the snapshot, in altapi's own fields.
type ALTAPIState struct {
	UserID       int64   `json:"userId,omitempty"`
	Username     string  `json:"username,omitempty"`
	Group        string  `json:"group,omitempty"`
	Quota        float64 `json:"quota"`
	UsedQuota    float64 `json:"usedQuota"`
	RequestCount int64   `json:"requestCount"`
	// QuotaPerUnit is the console's own divisor, read live from /api/status.
	QuotaPerUnit float64 `json:"quotaPerUnit"`
	// BalanceUSD / UsedUSD are the two UI figures, derived and never guessed.
	BalanceUSD float64 `json:"balanceUsd"`
	UsedUSD    float64 `json:"usedUsd"`
	// Source is "session" (real balance) or "key" (spent only).
	Source string `json:"source"`
	// Role 1 = normal user, 100 = admin/root in new-api.
	Role     int64  `json:"role,omitempty"`
	Status   int64  `json:"status,omitempty"`
	ReadAt   string `json:"readAt,omitempty"`
	ReadHint string `json:"readHint,omitempty"`
}

// balance stale window: a read older than this is shown as stale, not current.
const altapiBalanceStale = 30 * time.Minute

// altapiUserAgent is the console's own UA shape; some CF rules key on it.
const altapiUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"

func (s *Snapshot) ALTAPIDiagnosis(now time.Time) *ALTAPIState {
	if s == nil || s.ALTAPI == nil {
		return &ALTAPIState{Source: "none", ReadHint: ALTAPIBalanceHintNoSession}
	}
	cp := *s.ALTAPI
	if cp.Source == "session" {
		if t, err := time.Parse(time.RFC3339, cp.ReadAt); err == nil && now.Sub(t) > altapiBalanceStale {
			cp.ReadHint = "余额读数是旧的（超过 " + altapiBalanceStale.String() + "），下次轮询会刷新"
		}
	}
	return &cp
}

// altapiSessionCookie normalizes the stored credential into a `session=…`
// Cookie header value. It accepts either the bare value or a full cookie
// string pasted from DevTools.

type altapiStatus struct {
	Data struct {
		QuotaPerUnit     float64 `json:"quota_per_unit"`
		DisplayInCurrency bool   `json:"display_in_currency"`
		QuotaDisplayType string  `json:"quota_display_type"`
		TurnstileCheck   bool    `json:"turnstile_check"`
		SystemName       string  `json:"system_name"`
	} `json:"data"`
}

type altapiSelf struct {
	Data struct {
		ID           int64   `json:"id"`
		Username     string  `json:"username"`
		Group        string  `json:"group"`
		Quota        float64 `json:"quota"`
		UsedQuota    float64 `json:"used_quota"`
		RequestCount int64   `json:"request_count"`
		Role         int64   `json:"role"`
		Status       int64   `json:"status"`
	} `json:"data"`
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// fetchALTAPI reads the console for a real balance and the /v1 catalogue with
// the inference key. A failed balance read keeps the last good value and never
// becomes "0 left".
func (p *Poller) fetchALTAPI(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	current := p.Cfg()
	next, ok := current.Account(a.ID)
	if !ok || next.Provider != config.ProviderALTAPI || ResponseBindingSource(cfg, a) != ResponseBindingSource(current, next) {
		return errors.New("altapi account source changed; refresh again")
	}
	p.mu.Lock()
	s := p.snaps[a.ID].clone()
	p.mu.Unlock()
	now := time.Now()
	if s == nil {
		s = &Snapshot{}
	}
	if s.ALTAPI == nil {
		s.ALTAPI = &ALTAPIState{Source: "none"}
	}

	client := *p.HTTPFor(a.ProxyURL)
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }

	// Catalogue with the inference key (public metadata, no money spent).
	every := time.Duration(cfg.ALTAPI.ModelSyncSec) * time.Second
	if force || s.CatalogAt.IsZero() || (every > 0 && now.Sub(s.CatalogAt) >= every) {
		cat, err := FetchCatalogFull(ctx, &client, cfg.RuntimeBase(a), a.RuntimeKey, a.ID)
		if err != nil {
			s.CatalogError = err.Error()
			s.LastError, s.LastErrorAt = err.Error(), now
			s.AuthFailed = IsAuth(err)
		} else {
			applyCatalog(s, cat, now)
			s.CatalogError = ""
		}
	}

	cookie := newapiconsole.SessionValue(a.ControlKey)
	if cookie == "" {
		// No console session: spent-only view through the key, balance unknown.
		cl := newapiconsole.New(newapiconsole.Request{
			Origin: newapiconsole.Origin(cfg.ALTAPI.BaseURL), Bearer: a.RuntimeKey, ProxyURL: a.ProxyURL,
		})
		if spent, err := cl.SpentUSD(ctx); err == nil {
			s.ALTAPI.Source = "key"
			s.ALTAPI.UsedUSD = newapiconsole.Round4(spent)
			s.ALTAPI.BalanceUSD = -1 // unknown, never 0
			s.ALTAPI.ReadAt = now.Format(time.RFC3339)
			s.ALTAPI.ReadHint = ALTAPIBalanceReasonKeyOnly + "；" + ALTAPIBalanceHintNoSession
			s.LastOKAt = now
		} else {
			s.ALTAPI.Source = "none"
			s.ALTAPI.ReadHint = ALTAPIBalanceReasonNoSession
			s.LastError, s.LastErrorAt = err.Error(), now
		}
		s.NextAt = now.Add(time.Duration(cfg.ForProvider(config.ProviderALTAPI).Quota.IntervalSec) * time.Second)
		p.store(a.ID, s)
		if s.ALTAPI.Source == "none" {
			return errors.New(ALTAPIBalanceReasonNoSession)
		}
		return nil
	}

	st, err := altapiReadConsole(ctx, cfg, a, cookie)
	if err != nil {
		// Keep the last good numbers; record the failure honestly.
		s.LastError, s.LastErrorAt = err.Error(), now
		s.AuthFailed = IsAuth(err) || strings.Contains(err.Error(), "access token 无效")
		s.Failures++
		s.NextAt = now.Add(backoff(s.Failures))
		s.ALTAPI.ReadHint = "本次读取失败，展示的是上一次成功读数"
		p.store(a.ID, s)
		return err
	}
	s.ALTAPI = st
	s.LastOKAt = now
	s.Failures = 0
	s.LastError, s.LastErrorAt = "", time.Time{}
	s.NextAt = now.Add(time.Duration(cfg.ForProvider(config.ProviderALTAPI).Quota.IntervalSec) * time.Second)
	p.store(a.ID, s)
	return nil
}

// altapiReadConsole is the balance read: /api/status for the divisor, then
// /api/user/self for quota/used_quota. Derivation matches the console exactly.
//
// Credential rules (session cookie PLUS New-Api-User equal to the uid) live in
// newapiconsole, so this and the console-side read cannot drift apart.
func altapiReadConsole(ctx context.Context, cfg *config.Config, a config.Account, cookie string) (*ALTAPIState, error) {
	cl := newapiconsole.New(newapiconsole.Request{
		Origin:   newapiconsole.Origin(cfg.ALTAPI.BaseURL),
		Session:  cookie,
		UID:      a.UserID,
		Bearer:   a.RuntimeKey,
		ProxyURL: a.ProxyURL,
	})

	// 1) divisor from the console itself; the live value beats the config.
	unit := cfg.ALTAPI.QuotaPerUnit
	if unit <= 0 {
		unit = 500000
	}
	if st, err := cl.Status(ctx); err == nil && st.QuotaPerUnit > 0 {
		unit = st.QuotaPerUnit
	}

	// 2) the account row.
	self, err := cl.Self(ctx)
	if err != nil {
		switch {
		case errors.Is(err, newapiconsole.ErrNoUID):
			return nil, errors.New("altapi 账号缺 userId：/api/user/self 要求 New-Api-User 等于登录用户 id")
		case errors.Is(err, newapiconsole.ErrNoSession):
			return nil, err
		}
		return nil, err
	}

	out := &ALTAPIState{
		UserID:       self.ID,
		Username:     self.Username,
		Group:        self.Group,
		Quota:        self.Quota,
		UsedQuota:    self.UsedQuota,
		RequestCount: self.RequestCount,
		QuotaPerUnit: unit,
		Source:       "session",
		Role:         self.Role,
		Status:       self.Status,
		ReadAt:       time.Now().Format(time.RFC3339),
	}
	out.BalanceUSD = newapiconsole.BalanceUSD(out.Quota, unit)
	out.UsedUSD = newapiconsole.BalanceUSD(out.UsedQuota, unit)
	if out.Role >= 100 {
		out.ReadHint = "该账号是管理员（role≥100），额度可能不按普通用户计"
	}
	return out, nil
}

func blankAs(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
