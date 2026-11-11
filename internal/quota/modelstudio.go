package quota

import (
	"context"
	"time"

	"account-pool-balancer/internal/config"
)

// ModelStudioQuotaState names what the pool knows about an account's Credits.
//
// Model Studio Token Plan Credits are shown only on the console page
// (modelstudio.console.alibabacloud.com "我的订阅"), which is served by a
// private gateway:
//
//	POST /data/api.json?action=zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/subscription
//	     &product=bailian
//
// Measured 2026-11-11: that gateway answers ConsoleNeedLogin without an
// Alibaba Cloud console session cookie, and the sibling call
// api-keys/getKeyByUid returns the console user's *own* key. An inference API
// key cannot be mapped back to the account that owns it, so a Credits figure
// can only be attached by binding a console session to the account that owns
// the key.
//
// The user's decision (2026-11-11) was to leave the frame in place and supply
// the session later, so this reader reports "not connected" rather than a
// number. It must never be mistaken for a zero balance: a zero would read as
// "exhausted" in the console and the scheduler.
const (
	// ModelStudioQuotaNotConnected is the only state the pool can report for
	// Credits today.
	ModelStudioQuotaNotConnected = "not-connected"
)

// ModelStudioQuotaReason is the operator-facing explanation shown next to
// the unknown balance. It is a fixed string, not a provider error, so the
// console can show it verbatim.
const ModelStudioQuotaReason = "百炼 Token Plan 的 Credits 只显示在控制台「我的订阅」页面，" +
	"私有网关需要控制台登录会话，且 API Key 无法反查所属账号。额度读取尚未接入，余额记为未知（绝非 0）。"

// fetchModelStudio refreshes what Model Studio genuinely exposes: the model
// catalogue of the account's plan and region. It deliberately reads no
// balance, because the upstream has no authenticated balance endpoint for an
// API key.
//
// The catalogue read doubles as the pool's free credential check: /models
// authenticates the key before listing, so a revoked or wrong-plan key is
// detected without spending Credits.
func (p *Poller) fetchModelStudio(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	p.mu.Lock()
	prev := p.snaps[a.ID].clone()
	p.mu.Unlock()
	s := prev
	if s == nil {
		s = &Snapshot{}
	}
	now := time.Now()

	// The plan and region decide the host, and both can change under us
	// (an account's key, the pool's region), so a catalogue read from a
	// different source must not be trusted as this account's.
	want := modelStudioCatalogSource(cfg, a)
	if s.CatalogSource != "" && s.CatalogSource != want {
		s = &Snapshot{CatalogSource: want}
	} else {
		s.CatalogSource = want
	}

	every := time.Duration(cfg.ModelStudio.ModelInterval()) * time.Second
	due := s.CatalogAt.IsZero() || every > 0 && now.Sub(s.CatalogAt) >= every
	if force {
		due = true
	}
	if due {
		// RuntimeBase resolves plan×region for this account, which is
		// exactly the host /models must be read from.
		cat, err := FetchCatalogFull(ctx, p.HTTPFor(a.ProxyURL), cfg.RuntimeBase(a), a.RuntimeKey, a.ID)
		switch {
		case err == nil:
			applyCatalog(s, cat, now)
			s.AuthFailed = false
			s.CatalogError = ""
			s.Failures = 0
			s.LastError, s.LastErrorAt = "", time.Time{}
			s.NextAt = time.Time{}
		case IsAuth(err):
			s.AuthFailed, s.CatalogError = true, err.Error()
			s.LastError, s.LastErrorAt = err.Error(), now
			s.Failures++
			s.NextAt = now.Add(backoff(s.Failures))
			p.store(a.ID, s)
			return err
		default:
			// keep the last good catalogue and try again next interval
			s.CatalogError = err.Error()
			s.LastError, s.LastErrorAt = err.Error(), now
			s.Failures++
			s.NextAt = now.Add(backoff(s.Failures))
		}
	}
	p.store(a.ID, s)
	return nil
}

// modelStudioCatalogSource fingerprints the host an account's catalogue is
// read from, so a plan/region/key change invalidates a stale catalogue.
func modelStudioCatalogSource(cfg *config.Config, a config.Account) string {
	return "modelstudio\x00" + cfg.ModelStudioPlan(a) + "\x00" + cfg.RuntimeBase(a) + "\x00" + fingerprintKey(a.RuntimeKey)
}

// fingerprintKey keeps the catalogue source stable without storing a
// credential: the last 8 characters are enough to tell two keys apart in
// config memory and never leave this process.
func fingerprintKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12] + "…" + key[len(key)-4:]
}
