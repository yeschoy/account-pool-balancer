package config

import (
	"fmt"
	"net/url"
	"strings"

	"account-pool-balancer/internal/rules"
)

const (
	ProviderMistral  = "mistral"
	MistralGroup     = ProviderMistral
	MistralKeyPrefix = "mstrl_"
)

// Mistral uses the existing scheduler. An absent override inherits the same
// routing and busy-binding policy as OpenDesign, without sharing its rules.
type Mistral struct {
	BaseURL string   `json:"baseUrl"`
	Routing *Routing `json:"routing,omitempty"`
}

func DefaultMistral() Mistral { return Mistral{BaseURL: "https://api.mistral.ai/v1"} }

func (c *Config) normalizeMistral() error {
	m := &c.Mistral
	if m.BaseURL == "" {
		m.BaseURL = DefaultMistral().BaseURL
	}
	m.BaseURL = strings.TrimRight(strings.TrimSpace(m.BaseURL), "/")
	u, err := url.Parse(m.BaseURL)
	if err != nil || len(m.BaseURL) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !strings.HasSuffix(u.Path, "/v1") {
		return fmt.Errorf("mistral.baseUrl: HTTP(S) API base must include /v1, without credentials, query or fragment")
	}
	if c.MistralErrorRules == nil {
		c.MistralErrorRules = rules.DefaultMistralErrorRules()
	}
	if c.MistralRetryRules == nil {
		c.MistralRetryRules = rules.DefaultMistralRetryRules()
	}
	if err := rules.ValidateErrorRules(c.MistralErrorRules); err != nil {
		return fmt.Errorf("mistral: %w", err)
	}
	if err := rules.ValidateRetryRules(c.MistralRetryRules); err != nil {
		return fmt.Errorf("mistral: %w", err)
	}
	return nil
}

// WriteOnlyCredentials protects native-key pools without implying that
// Mistral's stateless Responses API supports server-side response bindings.
func WriteOnlyCredentials(provider string) bool {
	return StrictResponses(provider) || provider == ProviderMistral
}
