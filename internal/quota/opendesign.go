package quota

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

// ReadResult contains only fixed classifications and sanitized error codes,
// never an upstream message, URL or credential. An OK catalogue is NOT proof
// that inference is authorized.
type ReadResult struct {
	State     string    `json:"state"`
	Status    int       `json:"status,omitempty"`
	Code      string    `json:"code,omitempty"`
	CheckedAt time.Time `json:"checkedAt,omitzero"`
}

func readResult(err error) ReadResult {
	r := ReadResult{State: "ok", CheckedAt: time.Now()}
	if err == nil {
		return r
	}
	r.State = "read-failed"
	var he *HTTPError
	if errors.As(err, &he) {
		r.Status, r.Code = he.Status, he.Code
		switch {
		case he.OwnerBanned:
			r.State = "banned"
		case he.Status == http.StatusUnauthorized:
			r.State = "auth-failed"
		case he.Status == http.StatusForbidden:
			r.State = "permission-denied"
		case he.Status == http.StatusPaymentRequired:
			r.State = "funding-rejected"
		case he.Status == http.StatusTooManyRequests:
			r.State = "rate-limited"
		case he.Status >= 500:
			r.State = "upstream-unavailable"
		}
	}
	return r
}

func (r ReadResult) Label() string {
	switch r.State {
	case "ok":
		return "读取成功"
	case "banned":
		return "上游明确账号已封禁"
	case "auth-failed":
		return "密钥失效或认证失败"
	case "permission-denied":
		return "权限不足（不等于封禁）"
	case "funding-rejected":
		return "计费拒绝（具体余额未知）"
	case "rate-limited":
		return "上游限流"
	case "upstream-unavailable":
		return "上游服务故障，当前结果未知"
	case "missing-key":
		return "无控制密钥，无法读取"
	case "workspace-unconfirmed":
		return "工作区未确认，未读取"
	default:
		return "查询失败，当前结果未知"
	}
}

func (r ReadResult) Description() string {
	s := r.Label()
	if r.Status != 0 {
		s += fmt.Sprintf(" · HTTP %d", r.Status)
	}
	if r.Code != "" {
		s += " " + r.Code
	}
	return s
}

func (r ReadResult) fresh(now time.Time, stale time.Duration) bool {
	return r.State == "ok" && !r.CheckedAt.IsZero() && !now.Before(r.CheckedAt) && now.Sub(r.CheckedAt) < stale
}

type OpenDesignState struct {
	Source    string     `json:"source"`
	Catalog   ReadResult `json:"catalog"`
	Workspace ReadResult `json:"workspace"`
	Plan      ReadResult `json:"plan"`
	Wallet    ReadResult `json:"wallet"`
	Billing   ReadResult `json:"billing"`
	// A transient failure must not erase a prior explicit ban. Only a new
	// successful runtime catalogue read (or changed source) clears this.
	RuntimeRestriction *ReadResult `json:"runtimeRestriction,omitempty"`
}

func OpenDesignSource(cfg *config.Config, a config.Account) string {
	return config.HashKey(ResponseBindingSource(cfg, a) + "\x00" + a.ControlKey + "\x00" + cfg.Upstream.ControlBase)
}

// OpenDesignDiagnosis is a display-only projection. It does not modify the
// scheduler's AuthFailed, EffectiveRemaining, quarantine or account switch.
type OpenDesignDiagnosis struct {
	Status       string    `json:"status"`
	Label        string    `json:"label"`
	Detail       string    `json:"detail"`
	CheckedAt    time.Time `json:"checkedAt,omitzero"`
	PlanFresh    bool      `json:"planFresh"`
	WalletFresh  bool      `json:"walletFresh"`
	BillingFresh bool      `json:"billingFresh"`
	CatalogFresh bool      `json:"catalogFresh"`
	AutoRefresh  bool      `json:"autoRefresh"`
	PlanStatus   string    `json:"planStatus"`
	WalletStatus string    `json:"walletStatus"`
}

func (s *Snapshot) OpenDesignDiagnosis(now time.Time, stale time.Duration) *OpenDesignDiagnosis {
	d := &OpenDesignDiagnosis{Status: "unknown", Label: "尚未检测，当前状态未知", PlanStatus: "unknown", WalletStatus: "unknown"}
	if s == nil || s.OpenDesign == nil {
		return d
	}
	o := s.OpenDesign
	d.CatalogFresh = o.Catalog.fresh(now, stale)
	d.Status, d.Label = "checked", "只读检测完成（不证明推理授权）"
	var details []string
	for _, part := range []struct {
		name string
		read ReadResult
	}{{"运行密钥/模型目录", o.Catalog}, {"工作区", o.Workspace}, {"套餐窗口", o.Plan}, {"钱包", o.Wallet}, {"账单", o.Billing}} {
		if part.read.CheckedAt.After(d.CheckedAt) {
			d.CheckedAt = part.read.CheckedAt
		}
		if part.read.State != "ok" {
			details = append(details, part.name+"："+part.read.Description())
			if d.Status == "checked" {
				d.Status, d.Label = "partial", "部分检测失败，状态/额度可能未知"
			}
		}
	}
	if o.RuntimeRestriction != nil {
		d.Status, d.Label = o.RuntimeRestriction.State, o.RuntimeRestriction.Label()
		if o.Catalog.State != o.RuntimeRestriction.State {
			details = append(details, "当前运行密钥复查失败，保留上次明确拒绝证据（"+o.RuntimeRestriction.CheckedAt.Format(time.RFC3339)+"）")
		}
	} else if o.Workspace.State == "auth-failed" || o.Plan.State == "auth-failed" {
		d.Status, d.Label = "control-auth-failed", "控制密钥失效或认证失败，需重新授权"
	} else if o.Catalog.State == "upstream-unavailable" || o.Plan.State == "upstream-unavailable" || o.Billing.State == "upstream-unavailable" {
		d.Status, d.Label = "upstream-unavailable", "上游服务故障，状态/额度未知"
	} else if !o.Catalog.fresh(now, stale) {
		d.Status, d.Label = "unknown", "运行密钥状态未知/过期"
	} else if o.Plan.State == "missing-key" {
		d.Status, d.Label = "metadata-only", "运行密钥检测通过，额度未知（无控制密钥）"
		details = []string{"未配置控制密钥，无法读取套餐/钱包/账单；目录成功不证明推理授权"}
	}
	d.PlanFresh = o.Plan.fresh(now, stale) && !s.PlanAt.IsZero() && now.Sub(s.PlanAt) < stale
	if d.PlanFresh {
		d.PlanStatus = "no-plan"
		if s.Eligible && len(s.Windows) == 0 {
			d.PlanFresh, d.PlanStatus = false, "unknown"
		} else if s.Eligible {
			d.PlanStatus = "available"
			for _, w := range s.Windows {
				if w.ResetsAt.IsZero() || !now.Before(w.ResetsAt) || w.Limit <= 0 {
					d.PlanFresh, d.PlanStatus = false, "unknown"
					break
				}
				if w.Remaining <= 0 {
					d.PlanStatus = "exhausted"
				}
			}
		}
	}
	d.WalletFresh = o.Wallet.fresh(now, stale) || (s.WalletSource == "preflight" && o.Plan.fresh(now, stale))
	d.WalletFresh = d.WalletFresh && !s.WalletAt.IsZero() && now.Sub(s.WalletAt) < stale
	if amount, ok := ParseUSD(s.WalletUSD); d.WalletFresh && ok && !math.IsNaN(amount) && !math.IsInf(amount, 0) {
		d.WalletStatus = "positive"
		if amount <= 0 {
			d.WalletStatus = "empty"
		}
	} else {
		d.WalletFresh = false
	}
	d.BillingFresh = o.Billing.fresh(now, stale) && s.Summary != nil && !s.SummaryAt.IsZero() && now.Sub(s.SummaryAt) < stale
	if o.Plan.State == "ok" && !d.PlanFresh {
		details = append(details, "套餐读数已过期或窗口不完整，当前额度未知")
		if d.Status == "checked" {
			d.Status, d.Label = "partial", "套餐读数已过期或不完整，当前额度未知"
		}
	}
	d.Detail = strings.Join(details, "；")
	return d
}

// fetchOpenDesign performs independent, bounded GETs. It never sends inference
// or changes account enablement / pool isolation, even on a confirmed ban.
func (p *Poller) fetchOpenDesign(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	s := p.Get(a.ID)
	source := OpenDesignSource(cfg, a)
	if s == nil || (s.OpenDesign != nil && s.OpenDesign.Source != source) {
		s = &Snapshot{}
	}
	if s.OpenDesign == nil {
		s.OpenDesign = &OpenDesignState{Source: source}
	}
	o := s.OpenDesign
	now := time.Now()
	client := &Client{Base: cfg.Upstream.ControlBase, HTTP: opencode.NoRedirect(p.HTTPFor(a.ProxyURL))}
	bounded := func() (context.Context, context.CancelFunc) { return context.WithTimeout(ctx, 10*time.Second) }
	var catalog *Catalog
	var catalogErr, workspaceErr, planErr, walletErr, billingErr error
	var ws Workspace
	var pf Preflight
	var wallet Wallet
	var summary Summary
	catalogDue := force || o.Catalog.State != "ok" || catalogDue(s, now, a.RuntimeKey != "", false)
	workspaceDue := force || s.WorkspaceID == "" || now.Sub(s.WorkspaceAt) > 10*time.Minute
	var wg sync.WaitGroup
	wg.Go(func() {
		if catalogDue {
			c, cancel := bounded()
			defer cancel()
			catalog, catalogErr = FetchCatalogFull(c, client.HTTP, cfg.RuntimeBase(a), a.RuntimeKey, a.ID)
			o.Catalog = readResult(catalogErr)
		}
	})
	wg.Go(func() {
		if a.ControlKey == "" {
			r := ReadResult{State: "missing-key", CheckedAt: time.Now()}
			o.Workspace, o.Plan, o.Wallet, o.Billing = r, r, r, r
			return
		}
		ws.ID = s.WorkspaceID
		if workspaceDue {
			c, cancel := bounded()
			ws, workspaceErr = client.Workspace(c, a.ControlKey, a.ID)
			cancel()
			o.Workspace = readResult(workspaceErr)
		} else if o.Workspace.State == "" {
			o.Workspace = ReadResult{State: "ok", CheckedAt: s.WorkspaceAt}
		}
		var parts sync.WaitGroup
		parts.Go(func() {
			c, cancel := bounded()
			defer cancel()
			// The account billing endpoint remains readable without a confirmed
			// workspace; do not reuse a stale workspace after an auth failure.
			summary, billingErr = client.Summary(c, a.ControlKey, ws.WorkspaceID(), a.ID)
			if billingErr == nil {
				known := false
				for _, amount := range []string{summary.SubscriptionCredits, summary.SubscriptionCreditsUSD, summary.RechargeCredits, summary.RechargeCreditsUSD, summary.TotalAvailableCredits, summary.TotalAvailableCreditsUSD, summary.BalanceUSD} {
					if v, ok := ParseUSD(amount); ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
						known = true
					}
				}
				if !known {
					billingErr = errors.New("billing amounts missing")
				}
			}
			o.Billing = readResult(billingErr)
		})
		if workspaceErr == nil {
			parts.Go(func() {
				c, cancel := bounded()
				defer cancel()
				pf, planErr = client.Preflight(c, a.ControlKey, ws.WorkspaceID(), "", a.ID)
				if planErr == nil && !pf.PlanKnown {
					planErr = errors.New("coding plan missing")
				}
				o.Plan = readResult(planErr)
			})
			parts.Go(func() {
				c, cancel := bounded()
				defer cancel()
				wallet, walletErr = client.Wallet(c, a.ControlKey, ws.WorkspaceID(), a.ID)
				if v, ok := ParseUSD(wallet.BalanceUSD); walletErr == nil && (!ok || math.IsNaN(v) || math.IsInf(v, 0)) {
					walletErr = errors.New("wallet amount invalid")
				}
				o.Wallet = readResult(walletErr)
			})
		} else {
			r := ReadResult{State: "workspace-unconfirmed", CheckedAt: time.Now()}
			o.Plan, o.Wallet = r, r
		}
		parts.Wait()
	})
	wg.Wait()

	if catalogDue {
		if catalogErr == nil {
			applyCatalog(s, catalog, o.Catalog.CheckedAt)
			o.RuntimeRestriction = nil
		} else {
			s.CatalogError = o.Catalog.Description()
			if o.Catalog.State == "banned" || (o.RuntimeRestriction == nil && (o.Catalog.State == "auth-failed" || o.Catalog.State == "permission-denied")) {
				r := o.Catalog
				o.RuntimeRestriction = &r
			}
		}
	}
	if a.ControlKey != "" {
		if workspaceErr == nil && workspaceDue {
			if s.WorkspaceID != "" && s.WorkspaceID != ws.WorkspaceID() {
				s.PlanAt, s.WalletAt, s.SummaryAt = time.Time{}, time.Time{}, time.Time{}
				s.Windows, s.Summary, s.Coverage = nil, nil, nil
				s.WalletUSD, s.WalletSource, s.Tier, s.MembershipTier = "", "", "", ""
			}
			s.WorkspaceID, s.WorkspaceMemberID = ws.WorkspaceID(), ws.MemberID
			s.WorkspaceName, s.WorkspaceType, s.MembershipTier = ws.Name, ws.Type, ws.PersonalMembershipTier
			s.WorkspaceAt = o.Workspace.CheckedAt
		}
		if workspaceErr == nil && planErr == nil {
			s.PlanAt, s.Eligible, s.Tier, s.Windows = o.Plan.CheckedAt, pf.Eligible, pf.Tier, pf.Windows
			s.Funding, s.ModelCovered, s.PreflightBalanceUSD = pf.Funding, pf.ModelCovered, pf.BalanceUSD
			s.AuthFailed = false
			// Keep the existing bounded model coverage refresh behavior.
			for _, model := range p.coverageDue(s, 6, now) {
				c, cancel := bounded()
				mp, err := client.Preflight(c, a.ControlKey, s.WorkspaceID, model, a.ID)
				cancel()
				if err != nil {
					break
				}
				if s.Coverage == nil {
					s.Coverage = map[string]*Coverage{}
				}
				s.Coverage[model] = &Coverage{Covered: mp.ModelCovered, Funding: mp.Funding, At: time.Now()}
			}
		} else {
			s.AuthFailed = IsAuth(workspaceErr) || IsAuth(planErr)
		}
		if workspaceErr == nil && walletErr == nil {
			s.WalletUSD, s.WalletSource, s.WalletAt = wallet.BalanceUSD, "wallet", o.Wallet.CheckedAt
		} else if workspaceErr == nil && planErr == nil {
			if _, ok := ParseUSD(pf.BalanceUSD); ok {
				s.WalletUSD, s.WalletSource, s.WalletAt = pf.BalanceUSD, "preflight", o.Plan.CheckedAt
			}
		}
		if billingErr == nil {
			s.Summary, s.SummaryAt = &summary, o.Billing.CheckedAt
		}
	}

	primaryFailed := (a.ControlKey != "" && (workspaceErr != nil || planErr != nil)) || (a.ControlKey == "" && catalogErr != nil)
	if primaryFailed {
		s.Failures++
		s.NextAt = time.Now().Add(backoff(s.Failures))
	} else {
		s.Failures = 0
		if a.ControlKey != "" {
			s.LastOKAt = o.Plan.CheckedAt
		} else if catalogDue && catalogErr == nil {
			s.LastOKAt = o.Catalog.CheckedAt
		}
		s.NextAt = time.Now().Add(time.Duration(cfg.Quota.IntervalSec) * time.Second)
	}
	var failures []string
	for _, part := range []struct {
		name string
		read ReadResult
	}{{"模型目录", o.Catalog}, {"工作区", o.Workspace}, {"套餐窗口", o.Plan}, {"钱包", o.Wallet}, {"账单", o.Billing}} {
		if part.read.State != "ok" && part.read.State != "missing-key" {
			failures = append(failures, part.name+"："+part.read.Description())
		}
	}
	s.LastError = strings.Join(failures, "；")
	if s.LastError != "" {
		s.LastErrorAt = time.Now()
	}
	p.store(a.ID, s)
	if s.LastError != "" {
		return errors.New(s.LastError)
	}
	return nil
}
