package config

import (
	"fmt"
	"net/url"
	"reflect"
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
	BaseURL      string   `json:"baseUrl"`
	Routing      *Routing `json:"routing,omitempty"`
	Quota        *Quota   `json:"quota,omitempty"` // nil inherits the shared polling settings
	ModelSyncSec *int     `json:"modelSyncSec,omitempty"`
	DetailLog    *bool    `json:"detailLog,omitempty"` // nil retains the shared legacy switch
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
	if m.Quota != nil {
		if err := normalizeQuota(m.Quota); err != nil {
			return fmt.Errorf("mistral: %w", err)
		}
	}
	if m.ModelSyncSec != nil && (*m.ModelSyncSec < 0 || *m.ModelSyncSec > 86400 || *m.ModelSyncSec > 0 && *m.ModelSyncSec < 60) {
		return fmt.Errorf("mistral.modelSyncSec must be 0 or 60–86400")
	}
	errors, retries := rules.DefaultMistralErrorRules(), rules.DefaultMistralRetryRules()
	// Upgrade only the exact previous factory defaults. Any administrator
	// edits (including notes/order/empty lists) remain authoritative.
	if c.MistralErrorRules == nil || reflect.DeepEqual(c.MistralErrorRules, errors[1:]) {
		c.MistralErrorRules = errors
	}
	if c.MistralRetryRules == nil || reflect.DeepEqual(c.MistralRetryRules, retries[1:]) {
		c.MistralRetryRules = retries
	}
	if err := rules.ValidateErrorRules(c.MistralErrorRules); err != nil {
		return fmt.Errorf("mistral: %w", err)
	}
	if err := rules.ValidateRetryRules(c.MistralRetryRules); err != nil {
		return fmt.Errorf("mistral: %w", err)
	}
	return nil
}

func (m Mistral) ModelInterval() int {
	if m.ModelSyncSec == nil {
		return 600
	}
	return *m.ModelSyncSec
}

// WriteOnlyCredentials retains the native-key import isolation/deduplication
// marker. It no longer governs administrator display, which the user explicitly
// enabled; Mistral Responses remains stateless and has no server-side bindings.
func WriteOnlyCredentials(provider string) bool {
	return StrictResponses(provider) || provider == ProviderMistral
}
