package config

import (
	"fmt"
	"net/url"
	"strings"

	"account-pool-balancer/internal/pricing"
	"account-pool-balancer/internal/rules"
)

const (
	ProviderFactory     = "factory"
	FactoryKeyPrefix    = "fk-"
	FactoryDefaultBase  = "https://api.factory.ai"
	FactoryDefaultModel = "deepseek-v4.1-flash"
	FactoryReference    = "magpie-community/plugins@d24fd5d1824e1078e7c8c56732813f8263fcf728/packages/factory"
)

// FactoryModel describes routing, not this account's entitlement. No prompt or
// client-identity rewrite is performed. Unknown models require an explicit entry.
type FactoryModel struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"` // chat | responses | messages
	Upstream string `json:"upstream"` // Factory's x-api-provider
}

type Factory struct {
	BaseURL       string          `json:"baseUrl"`
	Routing       *Routing        `json:"routing,omitempty"`
	Quota         *Quota          `json:"quota,omitempty"`
	Pricing       *pricing.Config `json:"pricing,omitempty"`
	DetailLog     bool            `json:"detailLog"`
	StandardOnly  bool            `json:"standardOnly"`
	Compatibility bool            `json:"compatibility"` // strict, buffered cross-protocol conversion
	Models        []FactoryModel  `json:"models"`
}

func DefaultFactory() Factory {
	return Factory{BaseURL: FactoryDefaultBase, Models: DefaultFactoryModels(), Compatibility: true}
}

func DefaultFactoryModels() []FactoryModel {
	var out []FactoryModel
	add := func(protocol, upstream string, ids ...string) {
		for _, id := range ids {
			out = append(out, FactoryModel{ID: id, Protocol: protocol, Upstream: upstream})
		}
	}
	// Routing metadata only, reviewed against FactoryReference. Fast mode is
	// explicit client intent, never a paid tier chosen by the gateway.
	add("messages", "anthropic", "claude-fable-5.2", "claude-fable-5.1", "claude-fable-5", "claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-sonnet-5-5", "claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-5-5", "claude-haiku-4-5-20251001")
	add("responses", "openai", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-astra", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.3-codex")
	add("responses", "xai", "grok-4.7", "grok-4.6")
	add("chat", "fireworks", "glm-5.3", "glm-5.3-flash", "kimi-k3.1-pro", "kimi-k3", "deepseek-v4.1-flash", "deepseek-v4.1-flash-1017", "qwen3.8-max", "minimax-m3.1-flash", "minimax-m3", "inkling", "muse-spark-1.2")
	add("chat", "baseten", "glm-5.2", "glm-5.2-fast", "nemotron-3.5-super", "nemotron-3-ultra", "beam-1.0")
	add("chat", "mistral", "mistral-large-4", "mistral-medium-3.5")
	add("messages", "fireworks", "minimax-m2.7")
	add("messages", "anthropic", "claude-opus-5-5-fast", "claude-opus-5-fast", "claude-opus-4-8-fast")
	add("responses", "openai", "gpt-5.5-fast", "gpt-5.4-fast", "gpt-5.3-codex-fast", "gpt-6-sol-fast", "gpt-6-luna-fast", "gpt-5.6-sol-fast")
	return out
}

var factoryRoutingDefaults, factoryQuotaDefaults = func() (Routing, Quota) {
	c := Default()
	c.Routing.Strategy, c.Routing.WalletFallback = "least-connections", false
	c.Routing.OnBindingBusy = "reject"
	c.Quota.IntervalSec = 600
	return c.Routing, c.Quota
}()

func (f Factory) EffectiveRouting() Routing {
	if f.Routing != nil {
		return *f.Routing
	}
	return factoryRoutingDefaults
}
func (f Factory) EffectiveQuota() Quota {
	if f.Quota != nil {
		return *f.Quota
	}
	return factoryQuotaDefaults
}
func (f Factory) EffectivePricing() pricing.Config {
	if f.Pricing != nil {
		return *f.Pricing
	}
	return pricing.Config{Models: []pricing.Entry{}, Holidays: []string{}}
}

// Core models use a separate subscription meter. Hosting vendor alone is
// insufficient: Muse Spark is Fireworks-hosted but in the Standard pool.
func (f Factory) CoreModel(id string) bool {
	if _, ok := f.Model(id); !ok {
		return false
	}
	switch id {
	case "glm-5.3", "glm-5.3-flash", "glm-5.2", "glm-5.2-fast", "kimi-k3.1-pro", "kimi-k3", "deepseek-v4.1-flash", "deepseek-v4.1-flash-1017", "qwen3.8-max", "minimax-m3.1-flash", "minimax-m3", "minimax-m2.7", "mistral-large-4", "mistral-medium-3.5", "nemotron-3.5-super", "nemotron-3-ultra", "inkling", "beam-1.0":
		return true
	}
	return false
}

func (f Factory) Model(id string) (FactoryModel, bool) {
	for _, m := range f.Models {
		if m.ID == id {
			return m, true
		}
	}
	return FactoryModel{}, false
}

func (c *Config) normalizeFactory() error {
	f := &c.Factory
	if f.BaseURL == "" {
		f.BaseURL = FactoryDefaultBase
	}
	f.BaseURL = strings.TrimRight(strings.TrimSpace(f.BaseURL), "/")
	u, err := url.Parse(f.BaseURL)
	if err != nil || len(f.BaseURL) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" {
		return fmt.Errorf("factory.baseUrl must be an HTTP(S) origin without path, credentials, query or fragment")
	}
	if f.Quota != nil {
		if err := normalizeQuota(f.Quota); err != nil {
			return fmt.Errorf("factory: %w", err)
		}
	}
	if f.Pricing != nil {
		if err := f.Pricing.Normalize(); err != nil {
			return fmt.Errorf("factory: %w", err)
		}
	}
	if f.Models == nil {
		f.Models = DefaultFactoryModels()
	}
	if len(f.Models) > 1000 {
		return fmt.Errorf("factory: at most 1000 model routes")
	}
	seen := map[string]bool{}
	for _, m := range f.Models {
		if m.ID == "" || len(m.ID) > 200 || strings.ContainsAny(m.ID, " \t\r\n") || seen[m.ID] {
			return fmt.Errorf("factory: invalid or duplicate model id")
		}
		seen[m.ID] = true
		if m.Protocol != "chat" && m.Protocol != "responses" && m.Protocol != "messages" {
			return fmt.Errorf("factory: unsupported model protocol")
		}
		switch m.Upstream {
		case "anthropic", "openai", "xai", "fireworks", "baseten", "mistral":
		default:
			return fmt.Errorf("factory: unsupported x-api-provider")
		}
	}
	if c.FactoryErrorRules == nil {
		c.FactoryErrorRules = rules.DefaultFactoryErrorRules()
	}
	if c.FactoryRetryRules == nil {
		c.FactoryRetryRules = rules.DefaultFactoryRetryRules()
	}
	for _, r := range c.FactoryErrorRules {
		if r.Scope == rules.ScopeFunding {
			return fmt.Errorf("factory: funding scope unsupported; use account or model")
		}
	}
	if err := rules.ValidateErrorRules(c.FactoryErrorRules); err != nil {
		return fmt.Errorf("factory: %w", err)
	}
	return rules.ValidateRetryRules(c.FactoryRetryRules)
}

// NativeGateway identifies byte-preserving native pools sharing replay and
// privacy safety, not their protocols, settings, credentials or funding rules.
func NativeGateway(provider string) bool {
	return NativeChatOnly(provider) || provider == ProviderFactory
}
