package config

import (
	"fmt"
	"net/url"
	"reflect"
	"slices"
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
	BaseURL         string              `json:"baseUrl"`
	Routing         *Routing            `json:"routing,omitempty"`
	Quota           *Quota              `json:"quota,omitempty"` // nil inherits the shared polling settings
	ModelSyncSec    *int                `json:"modelSyncSec,omitempty"`
	DetailLog       *bool               `json:"detailLog,omitempty"`       // nil retains the shared legacy switch
	BindingCapacity *int                `json:"bindingCapacity,omitempty"` // nil/0: legacy shared4096; positive: independent ordinary sessions
	Performance     *MistralPerformance `json:"performance,omitempty"`
}

// MistralPerformance bounds local resources, not upstream token throughput.
// An absent object preserves full detail sampling and supplies safety defaults.
type MistralPerformance struct {
	MaxBufferedMB       int `json:"maxBufferedMB"`
	MaxWaitingRequests  int `json:"maxWaitingRequests"`
	UploadTimeoutSec    int `json:"uploadTimeoutSec"`
	WriteTimeoutSec     int `json:"writeTimeoutSec"`
	DetailSamplePercent int `json:"detailSamplePercent"`
}

func (m Mistral) EffectivePerformance() MistralPerformance {
	if m.Performance != nil {
		return *m.Performance
	}
	return MistralPerformance{MaxBufferedMB: 2048, MaxWaitingRequests: 1024, UploadTimeoutSec: 60, WriteTimeoutSec: 30, DetailSamplePercent: 100}
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
	if m.BindingCapacity != nil && (*m.BindingCapacity < 0 || *m.BindingCapacity > 1000000) {
		return fmt.Errorf("mistral.bindingCapacity must be 0 (legacy shared) or 1–1000000")
	}
	if p := m.Performance; p != nil {
		for _, v := range []struct {
			n, lo, hi int
			name      string
		}{
			{p.MaxBufferedMB, 1, 65536, "maxBufferedMB"}, {p.MaxWaitingRequests, 0, 100000, "maxWaitingRequests"},
			{p.UploadTimeoutSec, 1, 3600, "uploadTimeoutSec"}, {p.WriteTimeoutSec, 1, 600, "writeTimeoutSec"}, {p.DetailSamplePercent, 0, 100, "detailSamplePercent"},
		} {
			if v.n < v.lo || v.n > v.hi {
				return fmt.Errorf("mistral.performance.%s must be %d–%d", v.name, v.lo, v.hi)
			}
		}
	}
	errors, retries := rules.DefaultMistralErrorRules(), rules.DefaultMistralRetryRules()
	// The two shipped factory versions differed only by the leading 3800
	// service rule. Remove just this upgrade's additions to recognize both;
	// any administrator edit (notes/order/disabled/empty included) still wins.
	previousErrors := slices.DeleteFunc(slices.Clone(errors), func(r rules.ErrorRule) bool {
		return r.ID == "mistral-tier-not-allowed" || r.ID == "mistral-backend-out-of-capacity"
	})
	previousRetries := slices.DeleteFunc(slices.Clone(retries), func(r rules.RetryRule) bool {
		return r.ID == "mistral-backend-out-of-capacity"
	})
	if c.MistralErrorRules == nil || reflect.DeepEqual(c.MistralErrorRules, previousErrors) || reflect.DeepEqual(c.MistralErrorRules, previousErrors[1:]) {
		c.MistralErrorRules = errors
	}
	if c.MistralRetryRules == nil || reflect.DeepEqual(c.MistralRetryRules, previousRetries) || reflect.DeepEqual(c.MistralRetryRules, previousRetries[1:]) {
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
