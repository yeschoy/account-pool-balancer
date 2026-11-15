package config

import (
	"fmt"
	"net/url"
	"strings"

	"account-pool-balancer/internal/pricing"
	"account-pool-balancer/internal/rules"
)

const (
	ProviderX50     = "x50"
	X50DefaultBase  = "https://api.ai.50x.energy/v1"
	X50DefaultModel = "deepseek-ai/DeepSeek-V4.1-Flash-1017"
)

// X50 inference configuration is independent of the registration settings.
// ControlKey on an account is the optional billing Token, never a Bearer key.
type X50 struct {
	BaseURL      string          `json:"baseUrl"`
	Routing      *Routing        `json:"routing,omitempty"`
	Quota        *Quota          `json:"quota,omitempty"`
	ModelSyncSec int             `json:"modelSyncSec"`
	DetailLog    bool            `json:"detailLog"`
	RequestLog   *bool           `json:"requestLog,omitempty"`
	ErrorLog     *bool           `json:"errorLog,omitempty"`
	Pricing      *pricing.Config `json:"pricing,omitempty"`
}

func (x X50) RequestsEnabled() bool { return x.RequestLog == nil || *x.RequestLog }
func (x X50) ErrorsEnabled() bool   { return x.ErrorLog == nil || *x.ErrorLog }
func DefaultX50() X50               { return X50{BaseURL: X50DefaultBase, ModelSyncSec: 600} }

var x50RoutingDefaults, x50QuotaDefaults = func() (Routing, Quota) {
	c := Default()
	c.Routing.Strategy = "least-connections"
	c.Routing.WalletFallback = false
	c.Quota.IntervalSec = 600
	return c.Routing, c.Quota
}()

func (x X50) EffectiveRouting() Routing {
	if x.Routing != nil {
		return *x.Routing
	}
	return x50RoutingDefaults
}
func (x X50) EffectiveQuota() Quota {
	if x.Quota != nil {
		return *x.Quota
	}
	return x50QuotaDefaults
}
func (x X50) EffectivePricing() pricing.Config {
	if x.Pricing != nil {
		return *x.Pricing
	}
	return pricing.Config{Models: []pricing.Entry{}, Holidays: []string{}}
}
func X50Origin(base string) string {
	u, _ := url.Parse(base)
	if u == nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
func (c *Config) normalizeX50() error {
	x := &c.X50
	if x.BaseURL == "" {
		x.BaseURL = X50DefaultBase
	}
	x.BaseURL = strings.TrimRight(strings.TrimSpace(x.BaseURL), "/")
	u, e := url.Parse(x.BaseURL)
	if e != nil || len(x.BaseURL) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "/v1" {
		return fmt.Errorf("x50.baseUrl must be an HTTP(S) origin + /v1 without credentials, query or fragment")
	}
	if x.ModelSyncSec != 0 && (x.ModelSyncSec < 60 || x.ModelSyncSec > 86400) {
		return fmt.Errorf("x50.modelSyncSec must be 0 or 60–86400")
	}
	if x.Quota != nil {
		if e := normalizeQuota(x.Quota); e != nil {
			return e
		}
	}
	if x.Pricing != nil {
		if e := x.Pricing.Normalize(); e != nil {
			return e
		}
	}
	if c.X50ErrorRules == nil {
		c.X50ErrorRules = rules.DefaultX50ErrorRules()
	}
	if c.X50RetryRules == nil {
		c.X50RetryRules = rules.DefaultX50RetryRules()
	}
	for _, r := range c.X50ErrorRules {
		if r.Scope == rules.ScopeFunding {
			return fmt.Errorf("x50: use account or model scope; shared billing identity is not proven")
		}
	}
	if e := rules.ValidateErrorRules(c.X50ErrorRules); e != nil {
		return e
	}
	return rules.ValidateRetryRules(c.X50RetryRules)
}

// NativeChatOnly pools share byte-preserving forwarding and replay safety,
// not credentials, pricing, quota or provider-specific error semantics.
func NativeChatOnly(provider string) bool {
	return provider == ProviderDeploy || provider == ProviderX50
}
