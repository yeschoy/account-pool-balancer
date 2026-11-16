package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"

	"account-pool-balancer/internal/pricing"
	"account-pool-balancer/internal/rules"
)

const (
	// ProviderALTAPI is altapi.ai (TokenPower, a customized new-api fork).
	ProviderALTAPI = "altapi"
	// ALTAPIDefaultBase is the OpenAI-compatible API root. The console
	// (/api/*) lives on the same origin, so BaseURL's origin doubles as the
	// control plane root.
	ALTAPIDefaultBase = "https://altapi.ai/v1"
	// ALTAPIDefaultModel is a model verified to answer on the default group.
	ALTAPIDefaultModel = "claude-sonnet-5"

	altapiKeyMinLen = 8
)

// ALTAPI is a new-api pool. Unlike Deploy, its console exposes a real balance,
// so a session-cookie binding is meaningful: /api/user/self reports `quota`,
// and `quota / quota_per_unit` is exactly the number the web UI shows as
// "Current balance".
//
// QuotaPerUnit is a fallback only: the live value is read from /api/status and
// wins whenever that call succeeds.
type ALTAPI struct {
	BaseURL      string          `json:"baseUrl"`
	QuotaPerUnit float64         `json:"quotaPerUnit,omitempty"`
	Routing      *Routing        `json:"routing,omitempty"`
	Quota        *Quota          `json:"quota,omitempty"`
	ModelSyncSec int             `json:"modelSyncSec"`
	DetailLog    bool            `json:"detailLog"`
	Pricing      *pricing.Config `json:"pricing,omitempty"`
	// LowBalanceDisable gates scheduling, never the manually managed Enabled flag.
	LowBalanceDisable bool    `json:"lowBalanceDisable"`
	MinBalanceUSD     float64 `json:"minBalanceUsd"`
}

func DefaultALTAPI() ALTAPI {
	return ALTAPI{BaseURL: ALTAPIDefaultBase, QuotaPerUnit: 500000, ModelSyncSec: 600, MinBalanceUSD: 0.5}
}

// Cache the immutable baseline once, like Deploy: Default builds every
// provider's rule and price tables and must not run on the polling hot path.
var altapiRoutingDefaults, altapiQuotaDefaults = func() (Routing, Quota) {
	c := Default()
	// Balance is readable, so least-remaining is a real signal — but a fresh
	// account holds a flat $10, so least-connections avoids tie-breaking noise.
	c.Routing.Strategy, c.Routing.WalletFallback = "least-connections", false
	c.Quota.IntervalSec = 600
	return c.Routing, c.Quota
}()

func (a ALTAPI) EffectiveRouting() Routing {
	if a.Routing != nil {
		return *a.Routing
	}
	return altapiRoutingDefaults
}

func (a ALTAPI) EffectiveQuota() Quota {
	if a.Quota != nil {
		return *a.Quota
	}
	return altapiQuotaDefaults
}

// DefaultALTAPIPricing is an estimate table only; upstream bills the account,
// not this balancer. Values are USD per 1M tokens as published by the console
// price page, kept ordered cheapest-first so the UI has a sane default.
func DefaultALTAPIPricing() pricing.Config {
	return pricing.Config{Models: []pricing.Entry{
		{Model: "claude-sonnet-5", Currency: "USD", Input: 3.00, CachedInput: 0.30, Output: 15.00, Note: "altapi 页面价（估算，非账单）"},
		{Model: "gpt-5.6", Currency: "USD", Input: 1.25, CachedInput: 0.125, Output: 10.00, Note: "altapi 页面价（估算，非账单）"},
	}, Holidays: []string{}}
}

func (a ALTAPI) EffectivePricing() pricing.Config {
	if a.Pricing != nil {
		return *a.Pricing
	}
	return DefaultALTAPIPricing()
}

// ALTAPIOrigin returns the console origin derived from BaseURL (".../v1" →
// "https://host"), used for /api/* reads and the registrar.
func ALTAPIOrigin(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return "https://altapi.ai"
	}
	return u.Scheme + "://" + u.Host
}

func (c *Config) normalizeALTAPI() error {
	a := &c.ALTAPI
	if a.BaseURL == "" {
		a.BaseURL = ALTAPIDefaultBase
	}
	a.BaseURL = strings.TrimRight(strings.TrimSpace(a.BaseURL), "/")
	u, err := url.Parse(a.BaseURL)
	if err != nil || len(a.BaseURL) > 4096 ||
		(u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!strings.HasSuffix(u.Path, "/v1") {
		return fmt.Errorf("altapi.baseUrl must be an HTTP(S) API root ending in /v1 without credentials, query or fragment")
	}
	if math.IsNaN(a.MinBalanceUSD) || math.IsInf(a.MinBalanceUSD, 0) || a.MinBalanceUSD < 0 || a.MinBalanceUSD > 1e9 {
		return fmt.Errorf("altapi.minBalanceUsd must be finite and 0..1e9 USD")
	}
	if a.QuotaPerUnit == 0 {
		a.QuotaPerUnit = 500000
	}
	if a.QuotaPerUnit < 1 || a.QuotaPerUnit > 1e9 {
		return fmt.Errorf("altapi.quotaPerUnit must be 1..1e9 (altapi.ai 实测 500000)")
	}
	if a.ModelSyncSec != 0 && (a.ModelSyncSec < 60 || a.ModelSyncSec > 86400) {
		return fmt.Errorf("altapi.modelSyncSec must be 0 or 60–86400")
	}
	if a.Quota != nil {
		if err := normalizeQuota(a.Quota); err != nil {
			return fmt.Errorf("altapi: %w", err)
		}
	}
	if a.Pricing != nil {
		if err := a.Pricing.Normalize(); err != nil {
			return fmt.Errorf("altapi: %w", err)
		}
	}
	// Match only the complete, untouched factory tables shipped in 4cc0593
	// and aa9e9fb.
	// Hash canonical typed JSON (not input file bytes), so whitespace does
	// not matter; notes/order/disabled/empty/operator additions all do.
	// The frozen tables live in testdata/altapi_rules_v{1,2}.json for regression.
	if c.ALTAPIErrorRules == nil ||
		isShippedALTAPIRules(c.ALTAPIErrorRules, "c4f56f1ddd539472a5c603b9c419aec9cf69e0fbee8b2ae99291cdc78b9c2fac") ||
		isShippedALTAPIRules(c.ALTAPIErrorRules, "eff59fc1de9443003c746505036c3ea126b2ae1315c81d26696ba480c30f49f1") {
		c.ALTAPIErrorRules = rules.DefaultALTAPIErrorRules()
	}
	if c.ALTAPIRetryRules == nil || isShippedALTAPIRules(c.ALTAPIRetryRules, "bd87479ec4430cc57a7da083a311f054bc39844cad32d6bcc0bfe1ef7a8a90ac") {
		c.ALTAPIRetryRules = rules.DefaultALTAPIRetryRules()
	}
	for _, rule := range c.ALTAPIErrorRules {
		// altapi bills every model from one wallet: quota reaching zero is a
		// whole-account condition, so funding scope is genuinely shared.
		if rule.Scope == rules.ScopeFunding {
			return fmt.Errorf("altapi: funding scope needs a proven shared billing source; use account or model")
		}
	}
	if err := rules.ValidateErrorRules(c.ALTAPIErrorRules); err != nil {
		return fmt.Errorf("altapi: %w", err)
	}
	if err := rules.ValidateRetryRules(c.ALTAPIRetryRules); err != nil {
		return fmt.Errorf("altapi: %w", err)
	}
	return nil
}

func isShippedALTAPIRules(rs any, checksum string) bool {
	b, err := json.Marshal(rs)
	return err == nil && fmt.Sprintf("%x", sha256.Sum256(b)) == checksum
}
