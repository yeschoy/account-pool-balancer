package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"account-pool-balancer/internal/rules"
)

const (
	ProviderOpenCodeZen = "opencode-zen"
	ProviderOpenCodeGo  = "opencode-go"
)

func IsOpenCode(p string) bool { return p == ProviderOpenCodeZen || p == ProviderOpenCodeGo }

// OpenCode is additive to version 1. Anonymous access is deliberately absent.
// Bases include /v1, like every other API-key provider in this application.
type OpenCode struct {
	Enabled             bool              `json:"enabled"`
	Compatibility       bool              `json:"compatibility"`
	ZenBase             string            `json:"zenBase"`
	GoBase              string            `json:"goBase"`
	GoEgressCheckURL    string            `json:"goEgressCheckUrl"`
	GoEgressCheckSec    int               `json:"goEgressCheckSec"`
	GoPolicyCooldownSec int               `json:"goPolicyCooldownSec"`
	CapabilitiesURL     string            `json:"capabilitiesUrl"`
	ZenDocsURL          string            `json:"zenDocsUrl"`
	GoDocsURL           string            `json:"goDocsUrl"`
	RefreshSec          int               `json:"refreshSec"`
	ZenRouting          *Routing          `json:"zenRouting,omitempty"`
	GoRouting           *Routing          `json:"goRouting,omitempty"`
	ZenFallbackGroup    string            `json:"zenFallbackGroup,omitempty"`
	GoFallbackGroup     string            `json:"goFallbackGroup,omitempty"`
	ZenProtocols        map[string]string `json:"zenProtocols"`
	GoProtocols         map[string]string `json:"goProtocols"`
	ZenProtocolBases    map[string]string `json:"zenProtocolBases"`
	GoProtocolBases     map[string]string `json:"goProtocolBases"`
	ZenErrorRules       []rules.ErrorRule `json:"zenErrorRules"`
	ZenRetryRules       []rules.RetryRule `json:"zenRetryRules"`
	GoErrorRules        []rules.ErrorRule `json:"goErrorRules"`
	GoRetryRules        []rules.RetryRule `json:"goRetryRules"`
}

func DefaultOpenCode() OpenCode {
	return OpenCode{
		ZenBase: "https://opencode.ai/zen/v1", GoBase: "https://opencode.ai/zen/go/v1",
		CapabilitiesURL: "https://models.opencode.ai/api.json", RefreshSec: 600,
		GoEgressCheckSec: 60, GoPolicyCooldownSec: 300,
		ZenProtocols: map[string]string{}, GoProtocols: map[string]string{},
		ZenProtocolBases: map[string]string{}, GoProtocolBases: map[string]string{},
		ZenErrorRules: DefaultErrorRulesFor(ProviderOpenCodeZen), GoErrorRules: DefaultErrorRulesFor(ProviderOpenCodeGo),
		ZenRetryRules: DefaultRetryRulesFor(ProviderOpenCodeZen), GoRetryRules: DefaultRetryRulesFor(ProviderOpenCodeGo),
	}
}

func (o *OpenCode) normalize() error {
	d := DefaultOpenCode()
	for _, f := range []struct {
		p    *string
		def  string
		name string
	}{
		{&o.ZenBase, d.ZenBase, "zenBase"}, {&o.GoBase, d.GoBase, "goBase"},
		{&o.CapabilitiesURL, d.CapabilitiesURL, "capabilitiesUrl"},
		{&o.ZenDocsURL, "", "zenDocsUrl"}, {&o.GoDocsURL, "", "goDocsUrl"},
		{&o.GoEgressCheckURL, "", "goEgressCheckUrl"},
	} {
		if *f.p == "" {
			*f.p = f.def
		}
		if *f.p == "" {
			continue
		}
		*f.p = strings.TrimRight(*f.p, "/")
		if err := validateOpenCodeURL(*f.p, f.name); err != nil {
			return err
		}
	}
	if o.GoEgressCheckSec == 0 {
		o.GoEgressCheckSec = d.GoEgressCheckSec
	}
	if o.GoPolicyCooldownSec == 0 {
		o.GoPolicyCooldownSec = d.GoPolicyCooldownSec
	}
	if err := clampInt(&o.GoEgressCheckSec, 5, 3600, "opencode.goEgressCheckSec"); err != nil {
		return err
	}
	if err := clampInt(&o.GoPolicyCooldownSec, 1, 86400, "opencode.goPolicyCooldownSec"); err != nil {
		return err
	}
	for _, bases := range []*map[string]string{&o.ZenProtocolBases, &o.GoProtocolBases} {
		if *bases == nil {
			*bases = map[string]string{}
		}
		for protocol, base := range *bases {
			if protocol != "chat" && protocol != "responses" && protocol != "messages" {
				return errors.New("opencode: unknown protocol base")
			}
			base = strings.TrimRight(base, "/")
			if err := validateOpenCodeURL(base, "protocolBases"); err != nil {
				return err
			}
			(*bases)[protocol] = base
		}
	}
	if o.RefreshSec == 0 {
		o.RefreshSec = d.RefreshSec
	}
	if err := clampInt(&o.RefreshSec, 60, 86400, "opencode.refreshSec"); err != nil {
		return err
	}
	for _, p := range []*map[string]string{&o.ZenProtocols, &o.GoProtocols} {
		if *p == nil {
			*p = map[string]string{}
		}
		if len(*p) > 10000 {
			return errors.New("opencode: too many protocol overrides")
		}
		for m, v := range *p {
			if strings.TrimSpace(m) == "" || len(m) > 256 || (v != "chat" && v != "responses" && v != "messages") {
				return errors.New("opencode: protocol override requires a model and chat, responses or messages")
			}
		}
	}
	if o.ZenErrorRules == nil {
		o.ZenErrorRules = d.ZenErrorRules
	}
	if o.GoErrorRules == nil {
		o.GoErrorRules = d.GoErrorRules
	}
	if o.ZenRetryRules == nil {
		o.ZenRetryRules = d.ZenRetryRules
	}
	if o.GoRetryRules == nil {
		o.GoRetryRules = d.GoRetryRules
	}
	for _, p := range []*[]rules.ErrorRule{&o.ZenErrorRules, &o.GoErrorRules} {
		if err := rules.ValidateErrorRules(*p); err != nil {
			return fmt.Errorf("opencode: %w", err)
		}
	}
	for _, p := range []*[]rules.RetryRule{&o.ZenRetryRules, &o.GoRetryRules} {
		if err := rules.ValidateRetryRules(*p); err != nil {
			return fmt.Errorf("opencode: %w", err)
		}
	}
	return nil
}

func validateOpenCodeURL(raw, field string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("opencode.%s: invalid URL (credentials, query and fragment are forbidden)", field)
	}
	if (field == "zenBase" || field == "goBase" || field == "protocolBases") && !strings.HasSuffix(u.Path, "/v1") {
		return fmt.Errorf("opencode.%s: API base must include /v1", field)
	}
	return nil
}

func (o OpenCode) ProtocolBases(provider string) map[string]string {
	if provider == ProviderOpenCodeGo {
		return o.GoProtocolBases
	}
	return o.ZenProtocolBases
}

// InferenceBase uses only administrator-validated addresses. Public model
// metadata must never redirect credential-bearing requests to arbitrary URLs.
func (c *Config) InferenceBase(a Account, protocol string) string {
	if IsOpenCode(a.Provider) {
		if base := c.OpenCode.ProtocolBases(a.Provider)[protocol]; base != "" {
			return base
		}
	}
	return c.RuntimeBase(a)
}

// ForProvider keeps immutable account/config slices but selects independent
// routing settings. Nil means inherit the existing global routing contract.
func (c *Config) ForProvider(provider string) *Config {
	var r *Routing
	switch provider {
	case ProviderOpenCodeZen:
		r = c.OpenCode.ZenRouting
	case ProviderOpenCodeGo:
		r = c.OpenCode.GoRouting
	case ProviderTokenHarbor:
		r = c.TokenHarbor.Routing
		if r == nil {
			out := *c
			out.Routing.Sticky = true
			out.Routing.OnBindingBusy = "reject"
			return &out
		}
	}
	if r == nil {
		return c
	}
	out := *c
	out.Routing = *r
	return &out
}

// FallbackGroup is an explicit administrator-granted cross-tier route. Empty
// means strict tier isolation; caller-supplied group names are never used.
func (o OpenCode) FallbackGroup(provider string) string {
	if provider == ProviderOpenCodeZen {
		return o.ZenFallbackGroup
	}
	if provider == ProviderOpenCodeGo {
		return o.GoFallbackGroup
	}
	return ""
}

func (o OpenCode) Protocol(provider, model string) string {
	if provider == ProviderOpenCodeGo {
		return o.GoProtocols[model]
	}
	return o.ZenProtocols[model]
}
