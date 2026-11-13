package quota

import (
	"context"
	"errors"
	"net/http"
	"time"

	"account-pool-balancer/internal/config"
)

// An inference secret has no documented balance read. The organization credit
// control plane uses a separate portal session; a public /models 200 is neither
// balance nor activation proof. Do not substitute the registration promotion.
const DeployQuotaReason = "当前仅绑定 Deploy 推理 secret；官方推理文档未提供可用此 Key 读取组织余额的接口。组织 credit 控制面需要独立登录会话，尚未绑定；余额未知（不是 0），目录成功不代表 Key 已 active 或推理授权。"

// DeployQuotaHint 说明想看到真实余额需要做什么，不拿注册赠送额度冒充推理授权。
const DeployQuotaHint = "余额要读组织控制台才有数：普通账号用注册号池里的组织会话（hive-portal-production cookie）请求 GET /api/organization/{org_id}/credit；只有推理 secret 的账号只能显示未知，不能当 0。"

type DeployDiagnosis struct {
	QuotaState   string    `json:"quotaState"`
	QuotaReason  string    `json:"quotaReason"`
	CatalogState string    `json:"catalogState"`
	CatalogFresh bool      `json:"catalogFresh"`
	CatalogAt    time.Time `json:"catalogAt,omitzero"`
	CatalogError string    `json:"catalogError,omitempty"`
	Models       int       `json:"models"`
}

func (s *Snapshot) DeployDiagnosis(now time.Time, stale time.Duration) *DeployDiagnosis {
	d := &DeployDiagnosis{QuotaState: "not-connected", QuotaReason: DeployQuotaReason, CatalogState: "unknown"}
	if s == nil {
		return d
	}
	d.CatalogAt, d.CatalogError = s.CatalogAt, s.CatalogError
	for _, enabled := range s.Catalog {
		if enabled {
			d.Models++
		}
	}
	d.CatalogFresh = !s.CatalogAt.IsZero() && now.Sub(s.CatalogAt) < stale && s.CatalogError == ""
	switch {
	case s.CatalogError != "":
		d.CatalogState = "failed"
	case d.CatalogFresh:
		d.CatalogState = "ok"
	case !s.CatalogAt.IsZero():
		d.CatalogState = "stale"
	}
	return d
}

func (p *Poller) fetchDeploy(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	current := p.Cfg()
	next, ok := current.Account(a.ID)
	if !ok || next.Provider != config.ProviderDeploy || ResponseBindingSource(cfg, a) != ResponseBindingSource(current, next) {
		return errors.New("Deploy account source changed; refresh again")
	}
	source := ResponseBindingSource(cfg, a)
	p.mu.Lock()
	s := p.snaps[a.ID].clone()
	p.mu.Unlock()
	if s == nil || s.CatalogSource != source {
		s = &Snapshot{CatalogSource: source}
	}
	now := time.Now()
	every := time.Duration(cfg.Deploy.ModelSyncSec) * time.Second
	if force || s.CatalogAt.IsZero() || (every > 0 && now.Sub(s.CatalogAt) >= every) {
		hc := p.HTTPFor(a.ProxyURL)
		if p.DeployHTTPFor != nil {
			hc = p.DeployHTTPFor(cfg, a.ProxyURL)
		}
		client := *hc
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		cat, err := FetchCatalogFull(ctx, &client, cfg.RuntimeBase(a), a.RuntimeKey, a.ID)
		now = time.Now()
		if err != nil {
			s.CatalogError, s.LastError, s.LastErrorAt = err.Error(), err.Error(), now
			s.AuthFailed = IsAuth(err)
			s.Failures++
			s.NextAt = now.Add(backoff(s.Failures))
			p.store(a.ID, s)
			return err
		}
		// Deploy publishes native IDs; never import another provider's aliases.
		for id, m := range cat.Models {
			m.Base = ""
			cat.Models[id] = m
		}
		applyCatalog(s, cat, now)
		s.AuthFailed = false
		s.Failures = 0
		s.LastError, s.LastErrorAt = "", time.Time{}
		s.LastOKAt = now
	}
	s.NextAt = now.Add(time.Duration(cfg.ForProvider(config.ProviderDeploy).Quota.IntervalSec) * time.Second)
	p.store(a.ID, s)
	return nil
}
