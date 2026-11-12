package config

import (
	"fmt"
	"net/url"
	"strings"

	"account-pool-balancer/internal/pricing"
	"account-pool-balancer/internal/rules"
)

const (
	ProviderDeploy     = "deploy"
	DeployDefaultBase  = "https://api.deploy.com/api/v1"
	DeployDefaultModel = "deepseek-ai/deepseek-v4.1-flash-1017"
)

// Deploy is an explicitly selected opaque-key pool. Its API root includes
// /api/v1, not /v1. No website session or payment setting belongs here.
type Deploy struct {
	BaseURL      string          `json:"baseUrl"`
	Routing      *Routing        `json:"routing,omitempty"`
	Quota        *Quota          `json:"quota,omitempty"`
	ModelSyncSec int             `json:"modelSyncSec"`
	DetailLog    bool            `json:"detailLog"`
	Pricing      *pricing.Config `json:"pricing,omitempty"`
}

func DefaultDeploy() Deploy {
	return Deploy{BaseURL: DeployDefaultBase, ModelSyncSec: 600}
}

// Cache the immutable baseline once: Default builds every provider's rule and
// price tables and must not be called for each account on polling/hot paths.
var deployRoutingDefaults, deployQuotaDefaults = func() (Routing, Quota) {
	c := Default()
	c.Routing.Strategy, c.Routing.WalletFallback = "least-connections", false
	c.Quota.IntervalSec = 600
	return c.Routing, c.Quota
}()

// Missing legacy blocks use Deploy defaults, never another pool's settings.
func (d Deploy) EffectiveRouting() Routing {
	if d.Routing != nil {
		return *d.Routing
	}
	return deployRoutingDefaults
}
func (d Deploy) EffectiveQuota() Quota {
	if d.Quota != nil {
		return *d.Quota
	}
	return deployQuotaDefaults
}

func DefaultDeployPricing() pricing.Config {
	return pricing.Config{Models: []pricing.Entry{
		{Model: DeployDefaultModel, Currency: "USD", Input: 0.10, CachedInput: 0.003, Output: 0.35, Note: "Deploy 官方价格表（2026-11-13），仅估算，非账单"},
		{Model: "zai-org/glm-5.3-flash", Currency: "USD", Input: 0.05, CachedInput: 0.01, Output: 0.17, Note: "Deploy 官方价格表（2026-11-13），仅估算，非账单"},
	}, Holidays: []string{}}
}
func (d Deploy) EffectivePricing() pricing.Config {
	if d.Pricing != nil {
		return *d.Pricing
	}
	return DefaultDeployPricing()
}

func (c *Config) normalizeDeploy() error {
	d := &c.Deploy
	if d.BaseURL == "" {
		d.BaseURL = DeployDefaultBase
	}
	d.BaseURL = strings.TrimRight(strings.TrimSpace(d.BaseURL), "/")
	u, err := url.Parse(d.BaseURL)
	if err != nil || len(d.BaseURL) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasSuffix(u.Path, "/v1") {
		return fmt.Errorf("deploy.baseUrl must be an HTTP(S) API root ending in /v1 without credentials, query or fragment (official root includes /api/v1)")
	}
	if d.ModelSyncSec != 0 && (d.ModelSyncSec < 60 || d.ModelSyncSec > 86400) {
		return fmt.Errorf("deploy.modelSyncSec must be 0 or 60–86400")
	}
	if d.Quota != nil {
		if err := normalizeQuota(d.Quota); err != nil {
			return fmt.Errorf("deploy: %w", err)
		}
	}
	if d.Pricing != nil {
		if err := d.Pricing.Normalize(); err != nil {
			return fmt.Errorf("deploy: %w", err)
		}
	}
	if c.DeployErrorRules == nil {
		c.DeployErrorRules = rules.DefaultDeployErrorRules()
	}
	if c.DeployRetryRules == nil {
		c.DeployRetryRules = rules.DefaultDeployRetryRules()
	}
	for _, rule := range c.DeployErrorRules {
		if rule.Scope == rules.ScopeFunding {
			return fmt.Errorf("deploy: funding scope needs a proven shared billing source; use account or model")
		}
	}
	if err := rules.ValidateErrorRules(c.DeployErrorRules); err != nil {
		return fmt.Errorf("deploy: %w", err)
	}
	if err := rules.ValidateRetryRules(c.DeployRetryRules); err != nil {
		return fmt.Errorf("deploy: %w", err)
	}
	return nil
}
