package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"account-pool-balancer/internal/pricing"
	"account-pool-balancer/internal/rules"
)

const (
	ProviderFactory    = "factory"
	FactoryKeyPrefix   = "fk-"
	FactoryDefaultBase = "https://api.factory.ai"
	// FactoryDefaultInferenceBase is where droid sends model calls. The
	// 1.14.0 capture separates planes: api.factory.ai serves whoami, quota,
	// settings and tools, while prem.factory.ai serves every /api/llm/* path.
	FactoryDefaultInferenceBase = "https://prem.factory.ai"
	// FactoryDefaultModel is the model the console's "real request" test
	// sends when the operator leaves the field empty. It is the capture's
	// current DeepSeek Flash route (baseten, chat), not the deprecated
	// DeepSeek V4.1 Flash entry droid lists as LEGACY.
	FactoryDefaultModel = "deepseek-v4.1-flash-1017"
	// FactorySurfaceTUI/Exec are droid's X-Factory-Request-Surface values.
	FactorySurfaceTUI  = "cli_tui"
	FactorySurfaceExec = "cli_exec"
	FactoryReference   = "magpie-community/plugins@d24fd5d1824e1078e7c8c56732813f8263fcf728/packages/factory"
)

// FactoryModel describes routing, not this account's entitlement. Unknown
// models require an explicit entry. A request's task prompts, history and tool
// schema are never rewritten; the only body change is droid's fixed system
// opening, and the client identity becomes droid's own (see
// internal/factory/droid.go and docs/factory-pool.md).
type FactoryModel struct {
	ID       string `json:"id"`
	Protocol string `json:"protocol"` // chat | responses | messages
	Upstream string `json:"upstream"` // Factory's x-api-provider
}

type Factory struct {
	// BaseURL is the management plane: whoami, billing, settings and tools.
	BaseURL string `json:"baseUrl"`
	// InferenceURL is the model plane. Empty derives it from BaseURL so a
	// custom or mock host keeps working; the default pair is api→prem.
	InferenceURL string `json:"inferenceUrl,omitempty"`
	// RequestSurface is droid's X-Factory-Request-Surface. cli_tui is the
	// interactive client; cli_exec also sends X-Factory-Exec-Output-Format.
	RequestSurface string `json:"requestSurface,omitempty"`
	// OpenAIPlatform is the optional `org-…` value droid sends on the OpenAI
	// route. The capture's token belongs to one organization and whoami does
	// not expose it, so it is configuration rather than a fabricated constant.
	OpenAIPlatform string          `json:"openaiPlatform,omitempty"`
	Routing        *Routing        `json:"routing,omitempty"`
	Quota          *Quota          `json:"quota,omitempty"`
	Pricing        *pricing.Config `json:"pricing,omitempty"`
	DetailLog      bool            `json:"detailLog"`
	StandardOnly   bool            `json:"standardOnly"`
	Compatibility  bool            `json:"compatibility"` // strict, buffered cross-protocol conversion
	// DroidBody opens every request's system prompt with droid's fixed line,
	// as droid itself does. Factory serves a subscription's models to its own
	// client, so this is the default; turning it off sends the caller's body
	// unchanged and relies on the credential alone.
	DroidBody *bool `json:"droidBody,omitempty"`
	// DroidVersion is the droid release the requests claim to be
	// (factory-cli/<version> and X-Client-Version). droid reads a server-side
	// min_cli_version and Factory refuses an old release even when every other
	// header matches, so this is meant to stay current. Empty uses the
	// built-in default.
	DroidVersion string `json:"droidVersion,omitempty"`
	// SystemMode is how the caller's system channel is presented:
	// "keep" (unchanged), "open" (droid's line prepended, the default), or
	// "fold" (moved into the first user turn, system channel removed).
	SystemMode string `json:"systemMode,omitempty"`
	// EscapeClientPhrases escapes the first character of the phrases Factory's
	// usage policy refuses, so a conversation that quotes another agent's
	// prompt is not rejected. The model still reads the same characters after
	// JSON decoding; only the wire bytes differ.
	EscapeClientPhrases *bool `json:"escapeClientPhrases,omitempty"`
	// RenameAllTools renames every declared tool Factory has not been measured
	// to accept, not only the names already known to be refused. The refused
	// list grows without notice and a missed name refuses the whole request,
	// while a renamed name is restored in the reply. droid's own names and
	// mcp__ tools are kept. Unset means yes.
	RenameAllTools *bool          `json:"renameAllTools,omitempty"`
	Models         []FactoryModel `json:"models"`
	// ProxyPool and UserAgents provide distinct per-account egress and
	// inference identity. They are assignment sources only; an account's own
	// ProxyURL/UserAgent always wins. Empty pools preserve the old behavior.
	ProxyPool           []string `json:"proxyPool,omitempty"`
	UserAgents          []string `json:"userAgents,omitempty"`
	UserAgent           string   `json:"userAgent,omitempty"`
	ManagementUserAgent string   `json:"managementUserAgent,omitempty"`
}

const (
	// factoryDefaultDroidVersion is the release the requests claim to be when
	// nothing is configured. Factory enforces a server-side minimum client
	// version, so this must stay at the current droid release.
	factoryDefaultDroidVersion        = "0.242.0"
	FactoryDefaultInferenceUserAgent  = "factory-cli/" + factoryDefaultDroidVersion
	FactoryDefaultManagementUserAgent = "Bun/1.4.4"
)

func DefaultFactory() Factory {
	return Factory{BaseURL: FactoryDefaultBase, RequestSurface: FactorySurfaceTUI, Models: DefaultFactoryModels(), Compatibility: true}
}

// EffectiveInferenceBase returns the origin model calls go to. An explicit
// inference URL wins; the stock management origin maps to the captured prem
// host, and any other configured origin (EU, mock, proxy) is reused verbatim.
func (f Factory) EffectiveInferenceBase() string {
	if s := strings.TrimRight(strings.TrimSpace(f.InferenceURL), "/"); s != "" {
		return s
	}
	b := strings.TrimRight(strings.TrimSpace(f.BaseURL), "/")
	if b == "" || b == FactoryDefaultBase {
		return FactoryDefaultInferenceBase
	}
	return b
}

// EffectiveRequestSurface validates to the two surfaces droid emits. Unknown
// or empty values fall back to the interactive one; Normalize rejects them on
// save so a typo never reaches the wire.
func (f Factory) EffectiveRequestSurface() string {
	if f.RequestSurface == FactorySurfaceExec {
		return FactorySurfaceExec
	}
	return FactorySurfaceTUI
}

// System handling modes for Factory.SystemMode.
const (
	// SystemKeep sends the caller's system channel unchanged.
	SystemKeep = "keep"
	// SystemOpen prepends droid's line to the system channel.
	SystemOpen = "open"
	// SystemFold moves the system channel into the first user turn and removes
	// the system field, which is what Factory accepts from a foreign client.
	SystemFold = "fold"
	// SystemMagpie rewrites the caller's prompts the way magpie's Factory
	// plugin does: fixed client identity lines become droid's, pi's opening
	// and compaction sentences are reworded, "harness" in a client's own
	// description says "runtime", and tool output quoting a refused phrase is
	// wrapped. Task instructions are kept.
	SystemMagpie = "magpie"
)

// EffectiveSystemMode resolves SystemMode, then the older DroidBody flag, then
// the default. Unknown values fall back to the default rather than failing a
// request.
func (f Factory) EffectiveSystemMode() string {
	switch f.SystemMode {
	case SystemKeep, SystemOpen, SystemFold, SystemMagpie:
		return f.SystemMode
	}
	if f.DroidBody != nil && !*f.DroidBody {
		return SystemKeep
	}
	return SystemOpen
}

// EffectiveDroidBody reports whether the droid system-prompt opening is
// injected, i.e. the resolved mode is open.
func (f Factory) EffectiveDroidBody() bool {
	return f.EffectiveSystemMode() == SystemOpen
}

// EffectiveRenameAllTools reports whether every tool name Factory has not been
// measured to accept is renamed. Unset means yes.
func (f Factory) EffectiveRenameAllTools() bool {
	return f.RenameAllTools == nil || *f.RenameAllTools
}

// EffectiveEscapeClientPhrases reports whether refused client phrases are
// escaped. Unset means yes: it only rewrites text Factory already refuses.
func (f Factory) EffectiveEscapeClientPhrases() bool {
	return f.EscapeClientPhrases == nil || *f.EscapeClientPhrases
}

// EffectiveUserAgent resolves the inference UA: account override, pool
// default, then droid's captured CLI identity.
func (f Factory) EffectiveUserAgent(a Account) string {
	if ua := strings.TrimSpace(a.UserAgent); ua != "" {
		return ua
	}
	if ua := strings.TrimSpace(f.UserAgent); ua != "" {
		return ua
	}
	return "factory-cli/" + f.EffectiveDroidVersion()
}

// EffectiveDroidVersion returns the release to claim, defaulting to the
// built-in one.
func (f Factory) EffectiveDroidVersion() string {
	if v := strings.TrimSpace(f.DroidVersion); v != "" {
		return v
	}
	return factoryDefaultDroidVersion
}

func (f Factory) EffectiveManagementUserAgent() string {
	if ua := strings.TrimSpace(f.ManagementUserAgent); ua != "" {
		return ua
	}
	return FactoryDefaultManagementUserAgent
}

// FactoryClientVersion returns the X-Client-Version matching a
// factory-cli/<version> UA, or droid's captured version for any other UA.
func FactoryClientVersion(ua string) string {
	if strings.HasPrefix(ua, "factory-cli/") {
		if v := strings.TrimSpace(strings.TrimPrefix(ua, "factory-cli/")); v != "" {
			return v
		}
	}
	return factoryDefaultDroidVersion
}

func DefaultFactoryModels() []FactoryModel {
	var out []FactoryModel
	add := func(protocol, upstream string, ids ...string) {
		for _, id := range ids {
			out = append(out, FactoryModel{ID: id, Protocol: protocol, Upstream: upstream})
		}
	}
	// Routing metadata, reviewed against FactoryReference and the droid
	// 1.14.0 capture. Fast mode is explicit client intent, never a paid tier
	// chosen by the gateway. The capture observed bedrock_anthropic for
	// claude-sonnet-5-5/claude-haiku-4-5, databricks for kimi-k3.1-pro and
	// baseten for deepseek-v4.1-flash-1017; Gemini's models are the captured
	// generate/google route (one body field and one upstream path).
	add("messages", "anthropic", "claude-fable-5.2", "claude-fable-5.1", "claude-fable-5", "claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-sonnet-5", "claude-sonnet-4-6", "claude-haiku-5-5")
	add("messages", "bedrock_anthropic", "claude-sonnet-5-5", "claude-haiku-4-5-20251001")
	add("responses", "openai", "gpt-6.2-sol", "gpt-6.1-sol", "gpt-6-sol", "gpt-6-astra", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.3-codex")
	add("responses", "xai", "grok-4.7", "grok-4.6")
	add("chat", "fireworks", "glm-5.3", "glm-5.3-flash", "kimi-k3", "deepseek-v4.1-flash", "qwen3.8-max", "minimax-m3.1-flash", "minimax-m3", "inkling", "muse-spark-1.2")
	add("chat", "databricks", "kimi-k3.1-pro")
	add("chat", "baseten", "glm-5.2", "glm-5.2-fast", "deepseek-v4.1-flash-1017", "nemotron-3.5-super", "nemotron-3-ultra", "beam-1.0")
	add("chat", "mistral", "mistral-large-4", "mistral-medium-3.5")
	add("generate", "google", "gemini-4-argon", "gemini-4-carbon", "gemini-4-hydrogen", "gemini-4-neon", "gemini-3.1-pro-preview", "gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.5-flash", "gemini-3-flash-preview")
	add("embeddings", "openai", "text-embedding-3-large")
	add("messages", "fireworks", "minimax-m2.7")
	add("messages", "anthropic", "claude-opus-5-5-fast", "claude-opus-5-fast", "claude-opus-4-8-fast")
	add("responses", "openai", "gpt-5.5-fast", "gpt-5.4-fast", "gpt-5.4-mini-fast", "gpt-5.4-mini", "gpt-5.3-codex-fast", "gpt-6-sol-fast", "gpt-6-luna-fast", "gpt-5.6-sol-fast")
	return out
}

var factoryRoutingDefaults, factoryQuotaDefaults = func() (Routing, Quota) {
	c := Default()
	c.Routing.Strategy, c.Routing.WalletFallback = "least-connections", false
	c.Routing.OnBindingBusy = "reject"
	// Factory's prompt cache lives per upstream account, so a conversation that
	// moves accounts loses its cache. Keep both the explicit session binding and
	// the conversation-fingerprint fallback for a day: a client that pauses
	// across a lunch break still lands on the account holding its cached prompt.
	c.Routing.ExplicitTTLSec, c.Routing.FallbackTTLSec = 24*3600, 24*3600
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
	// An empty saved table means "never priced yet" (the console saves the
	// block with the rest of the settings), so it must not silence the
	// defaults: only a table with entries replaces them.
	if f.Pricing != nil && len(f.Pricing.Models) > 0 {
		return *f.Pricing
	}
	return factoryPricingDefaults
}

// factoryPricingDefaults prices Factory's Google models in USD per 1M tokens
// from Google's published rates (ai.google.dev/gemini-api/docs/pricing,
// 2026-11-21). Google publishes nothing for the gemini-4-* tiers Factory
// serves, so those carry the rate of the same class of published model and a
// Note saying so; the settings page edits every rate, and a saved table
// replaces this one.
var factoryPricingDefaults = pricing.Config{
	Models: []pricing.Entry{
		{Model: "gemini-4-neon", Currency: "USD", Input: 2.50, CachedInput: 0.25, Output: 15.00, Note: "Factory 1.2x 倍率、Pro 档位估算"},
		{Model: "gemini-4-carbon", Currency: "USD", Input: 2.00, CachedInput: 0.20, Output: 12.00, Note: "Pro 档位（按 Gemini 3.1 Pro 官方价估算）"},
		{Model: "gemini-4-hydrogen", Currency: "USD", Input: 0.75, CachedInput: 0.075, Output: 3.75, Note: "Flash 档位（按 Gemini 3.8 Flash 官方价估算）"},
		{Model: "gemini-4-argon", Currency: "USD", Input: 0.50, CachedInput: 0.05, Output: 6.00, Note: "Flash-Lite 档位（按 Gemini 3.8 Flash-Lite 官方价估算）"},
		{Model: "gemini-3.1-pro-preview", Currency: "USD", Input: 2.00, CachedInput: 0.20, Output: 12.00},
		{Model: "gemini-3.8-flash", Currency: "USD", Input: 0.75, CachedInput: 0.075, Output: 3.75},
		{Model: "gemini-3.7-flash", Currency: "USD", Input: 0.75, CachedInput: 0.075, Output: 3.75},
		{Model: "gemini-3.6-flash", Currency: "USD", Input: 0.75, CachedInput: 0.075, Output: 3.75},
		{Model: "gemini-3.5-flash", Currency: "USD", Input: 0.75, CachedInput: 0.075, Output: 3.75},
		{Model: "gemini-3-flash-preview", Currency: "USD", Input: 0.50, CachedInput: 0.05, Output: 3.00},
	},
	Holidays: []string{},
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

// factoryOpenAIPlatformRE bounds the OpenAI organization token droid sends:
// the captured value is `org-` plus base64url characters. It is an
// identifier, not a credential, but an overlong or spaced value is rejected.
var factoryOpenAIPlatformRE = regexp.MustCompile(`^org-[A-Za-z0-9_-]{1,160}$`)

func validateFactoryOrigin(name, value string) error {
	u, err := url.Parse(value)
	if err != nil || len(value) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" {
		return fmt.Errorf("%s must be an HTTP(S) origin without path, credentials, query or fragment", name)
	}
	return nil
}

func (c *Config) normalizeFactory() error {
	f := &c.Factory
	if f.BaseURL == "" {
		f.BaseURL = FactoryDefaultBase
	}
	f.BaseURL = strings.TrimRight(strings.TrimSpace(f.BaseURL), "/")
	if err := validateFactoryOrigin("factory.baseUrl", f.BaseURL); err != nil {
		return err
	}
	f.InferenceURL = strings.TrimRight(strings.TrimSpace(f.InferenceURL), "/")
	if f.InferenceURL != "" {
		if err := validateFactoryOrigin("factory.inferenceUrl", f.InferenceURL); err != nil {
			return err
		}
	}
	if f.RequestSurface == "" {
		f.RequestSurface = FactorySurfaceTUI
	}
	if f.RequestSurface != FactorySurfaceTUI && f.RequestSurface != FactorySurfaceExec {
		return fmt.Errorf("factory.requestSurface must be %s or %s", FactorySurfaceTUI, FactorySurfaceExec)
	}
	f.OpenAIPlatform = strings.TrimSpace(f.OpenAIPlatform)
	if f.OpenAIPlatform != "" {
		if !strings.HasPrefix(f.OpenAIPlatform, "org-") {
			f.OpenAIPlatform = "org-" + f.OpenAIPlatform
		}
		if !factoryOpenAIPlatformRE.MatchString(f.OpenAIPlatform) {
			return fmt.Errorf("factory.openaiPlatform must be org- plus base64url characters")
		}
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
	if len(f.ProxyPool) > 500 {
		return fmt.Errorf("factory: proxy pool too large")
	}
	seenProxy := map[string]bool{}
	cleanProxy := make([]string, 0, len(f.ProxyPool))
	for _, raw := range f.ProxyPool {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if err := ValidateProxyURL(p); err != nil {
			return fmt.Errorf("factory: proxy pool: %w", err)
		}
		if !seenProxy[p] {
			seenProxy[p] = true
			cleanProxy = append(cleanProxy, p)
		}
	}
	if len(cleanProxy) == 0 {
		cleanProxy = nil
	}
	f.ProxyPool = cleanProxy
	if len(f.UserAgents) > 100 {
		return fmt.Errorf("factory: user-agent pool too large")
	}
	cleanUA := make([]string, 0, len(f.UserAgents))
	seenUA := map[string]bool{}
	for _, raw := range f.UserAgents {
		ua := strings.TrimSpace(raw)
		if ua == "" {
			continue
		}
		if err := ValidateClientUserAgent(ua); err != nil {
			return fmt.Errorf("factory: user-agent pool: %w", err)
		}
		if !seenUA[ua] {
			seenUA[ua] = true
			cleanUA = append(cleanUA, ua)
		}
	}
	if len(cleanUA) == 0 {
		cleanUA = nil
	}
	f.UserAgents = cleanUA
	for _, ua := range []string{f.UserAgent, f.ManagementUserAgent} {
		if strings.TrimSpace(ua) != "" {
			if err := ValidateClientUserAgent(strings.TrimSpace(ua)); err != nil {
				return fmt.Errorf("factory: %w", err)
			}
		}
	}
	if f.Models == nil {
		f.Models = DefaultFactoryModels()
	} else if len(f.Models) > 0 {
		// Existing custom lists predate the Gemini route. Add only the
		// captured Gemini generate entries; never overwrite or reorder a
		// caller's own routes.
		seen := make(map[string]bool, len(f.Models)+len(DefaultFactoryModels()))
		for _, m := range f.Models {
			seen[m.ID] = true
		}
		for _, d := range DefaultFactoryModels() {
			if (d.Protocol == "generate" || d.Protocol == "embeddings") && !seen[d.ID] {
				f.Models = append(f.Models, d)
				seen[d.ID] = true
			}
		}
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
		switch m.Protocol {
		case "chat", "responses", "messages", "generate", "embeddings":
		default:
			return fmt.Errorf("factory: unsupported model protocol")
		}
		switch m.Upstream {
		case "anthropic", "bedrock_anthropic", "openai", "xai", "fireworks", "baseten", "databricks", "google", "mistral":
		default:
			return fmt.Errorf("factory: unsupported x-api-provider")
		}
		if m.Protocol == "generate" && m.Upstream != "google" {
			return fmt.Errorf("factory: generate models require google")
		}
		if m.Protocol == "embeddings" && m.Upstream != "openai" {
			return fmt.Errorf("factory: embeddings models require openai")
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
