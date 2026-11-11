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
	// ProviderModelStudio is the Alibaba Cloud Model Studio (百炼 /
	// Bailian) pool. Its keys are dashscope API keys: "sk-" for
	// pay-as-you-go and "sk-sp-" for a Token Plan subscription.
	ProviderModelStudio = "modelstudio"

	// ModelStudioGroup is where Model Studio accounts land when no group is
	// named.
	ModelStudioGroup = ProviderModelStudio

	// ModelStudioKeyPrefix is the prefix of a pay-as-you-go (按量付费) key.
	ModelStudioKeyPrefix = "sk-"
	// ModelStudioTokenPlanKeyPrefix is the prefix of a Token Plan / Coding
	// Plan subscription key ("sk-sp-").
	ModelStudioTokenPlanKeyPrefix = "sk-sp-"
)

// ModelStudio regions. Each region has its own access host, its own API keys
// and its own model list; a key from one region does not work in another.
const (
	ModelStudioRegionBeijing   = "cn-beijing"
	ModelStudioRegionSingapore = "ap-southeast-1"
	ModelStudioRegionVirginia  = "us-east-1"
	ModelStudioRegionFrankfurt = "eu-central-1"
	ModelStudioRegionDefault   = ModelStudioRegionSingapore
)

// ModelStudio Plans. The plan decides which hosts serve the key; using the
// wrong pair of key and host is the documented cause of a 401 and can route a
// subscription key onto the metered (按量付费) channel.
const (
	// ModelStudioPlanPayAsYouGo is the "sk-" pay-as-you-go key: metered per
	// token against the Alibaba Cloud balance.
	ModelStudioPlanPayAsYouGo = "payg"
	// ModelStudioPlanTokenPlan is the "sk-sp-" Token Plan subscription:
	// monthly Credits measured by the console.
	ModelStudioPlanTokenPlan = "tokenplan"
)

// modelStudioEndpoint returns the OpenAI-compatible and Anthropic-compatible
// roots of a plan in a region.
//
// Measured 2026-11-11: a Token Plan key answers 200 on the
// /compatible-mode/v1 listing of token-plan.ap-southeast-1.maas.aliyuncs.com
// and accepts both Chat Completions and /apps/anthropic/v1/messages, while
// the same key is rejected (401) by dashscope(-intl).aliyuncs.com — the
// metered hosts. The metered hosts are only used for sk- keys.
func modelStudioEndpoint(plan, region string) (openAI, anthropic string) {
	switch plan {
	case ModelStudioPlanTokenPlan:
		switch region {
		case ModelStudioRegionBeijing:
			return "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", "https://token-plan.cn-beijing.maas.aliyuncs.com/apps/anthropic/v1"
		default:
			return "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic/v1"
		}
	}
	// pay-as-you-go
	switch region {
	case ModelStudioRegionBeijing:
		return "https://dashscope.aliyuncs.com/compatible-mode/v1", "https://dashscope.aliyuncs.com/apps/anthropic/v1"
	case ModelStudioRegionVirginia:
		return "https://dashscope-us.aliyuncs.com/compatible-mode/v1", "https://dashscope-us.aliyuncs.com/apps/anthropic/v1"
	case ModelStudioRegionFrankfurt:
		return "https://dashscope-eu.aliyuncs.com/compatible-mode/v1", "https://dashscope-eu.aliyuncs.com/apps/anthropic/v1"
	default:
		return "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", "https://dashscope-intl.aliyuncs.com/apps/anthropic/v1"
	}
}

// ModelStudioRegions are the regions the pool models. Each is an independent
// access host, key set and catalogue.
func ModelStudioRegions() []string {
	return []string{ModelStudioRegionBeijing, ModelStudioRegionSingapore, ModelStudioRegionVirginia, ModelStudioRegionFrankfurt}
}

// ModelStudioPlans are the plans the pool serves.
func ModelStudioPlans() []string {
	return []string{ModelStudioPlanPayAsYouGo, ModelStudioPlanTokenPlan}
}

// modelStudioPlanForKey infers the plan from the key's prefix. A Token Plan
// key always starts "sk-sp-", so the longer prefix is tested first: "sk-sp-…"
// must never be classified as a metered "sk-" key.
func modelStudioPlanForKey(key string) string {
	switch {
	case strings.HasPrefix(key, ModelStudioTokenPlanKeyPrefix):
		return ModelStudioPlanTokenPlan
	case strings.HasPrefix(key, ModelStudioKeyPrefix):
		return ModelStudioPlanPayAsYouGo
	}
	return ""
}

// ModelStudioConsoleAPIKeyPrefixOnly reports a Model Studio key that is
// unmistakable: both the metered and the subscription keys start "sk-",
// which no other pool claims in the same way (OpenDesign's own keys are
// "sk-amr-"). Callers use this to avoid silently routing a dashscope key into
// the OpenDesign pool.
func ModelStudioBareSKKey(key string) bool {
	return strings.HasPrefix(key, ModelStudioKeyPrefix) && !strings.HasPrefix(key, "sk-amr-")
}

// DefaultModelStudio is the pool's stock settings.
func DefaultModelStudio() ModelStudio {
	// PlanOverrides stays nil: an empty map would round-trip through JSON as
	// nil (omitempty) and make a Clone() compare unequal to its source.
	return ModelStudio{
		Region:       ModelStudioRegionDefault,
		ModelSyncSec: 600,
		DetailLog:    false,
	}
}

// ModelStudio holds the pool's settings. It is deliberately small: the
// existing scheduler, leases and rules engine serve both protocols, so only
// what the upstream actually distinguishes is configurable here.
type ModelStudio struct {
	// Region selects the access hosts and the console the pool talks to.
	Region string `json:"region"`
	// Routing is this pool's independent routing parameters. nil inherits the
	// shared global routing block.
	Routing *Routing `json:"routing,omitempty"`
	// Quota is this pool's polling settings. nil inherits the shared block.
	Quota *Quota `json:"quota,omitempty"`
	// ModelSyncSec is how often each account's model catalogue is re-read; 0
	// disables the periodic refresh (the first verification still reads it).
	ModelSyncSec int `json:"modelSyncSec"`
	// DetailLog independently controls this pool's request/header/body
	// detail capture; false (there is no legacy config to inherit) means off.
	DetailLog bool `json:"detailLog"`
	// PlanOverrides pins the plan of an account whose key prefix cannot tell
	// the two plans apart (both are "sk-"). Keyed by account ID. The plan is
	// only ever used to pick the access host, never to read a balance.
	PlanOverrides map[string]string `json:"planOverrides,omitempty"`
	// Console holds the console-credit reader, which is not wired yet.
	Console ModelStudioConsole `json:"console"`
	// BaseOverride replaces the region's plan host for every account. It
	// exists for local/mock verification and self-hosted gateways; empty means
	// the documented hosts. It is validated as an HTTP(S) origin and is
	// deliberately not exposed by the admin settings page.
	BaseOverride string `json:"baseOverride,omitempty"`
}

// hostRoot is the origin the plan's paths hang off, honouring BaseOverride.
func (m ModelStudio) hostRoot(plan string) string {
	if m.BaseOverride != "" {
		return strings.TrimRight(m.BaseOverride, "/")
	}
	openAI, _ := m.EndpointsFor(plan)
	if i := strings.Index(openAI, "://"); i > 0 {
		rest := openAI[i+3:]
		if j := strings.Index(rest, "/"); j > 0 {
			return openAI[:i+3+j]
		}
	}
	return openAI
}

// ModelStudioConsole describes the console Credits source.
//
// Model Studio Token Plan Credits are only shown on the console page
// (modelstudio.console.alibabacloud.com "我的订阅"); there is no public
// balance API. The private gateway the console calls
// (zeldaHttp.apikeyMgr./tokenplan/personal/api/v2/subscription through
// /data/api.json) answers ConsoleNeedLogin without an Alibaba Cloud console
// session cookie, and an API key cannot be mapped back to the account that
// owns it, so a bound session is the only possible source.
//
// The user's decision on 2026-11-11: leave the frame in place and supply the
// session later. Until then every quota projection must report "not
// connected" — never a zero, never a guessed balance.
type ModelStudioConsole struct {
	// Endpoint is the console gateway origin; it is recorded rather than
	// editable so a future reader cannot be pointed at another host by a
	// config edit alone.
	Endpoint string `json:"endpoint"`
	// Enabled turns the reader on once a session is bound.
	Enabled bool `json:"enabled"`
}

// ModelStudioConsoleEndpoint is the console gateway the Credits reader would
// call once sessions exist.
const ModelStudioConsoleEndpoint = "https://modelstudio.console.alibabacloud.com"

func (m ModelStudio) ModelInterval() int { return m.ModelSyncSec }

// Endpoints is the plan×region access pair, resolved from the settings.
func (m ModelStudio) Endpoints() (openAI, anthropic string) {
	return modelStudioEndpoint(ModelStudioPlanTokenPlan, normalizeModelStudioRegion(m.Region))
}

// EndpointsFor resolves the access hosts of one account's plan.
func (m ModelStudio) EndpointsFor(plan string) (openAI, anthropic string) {
	if plan == "" {
		plan = ModelStudioPlanTokenPlan
	}
	openAI, anthropic = modelStudioEndpoint(plan, normalizeModelStudioRegion(m.Region))
	if m.BaseOverride != "" {
		// Same path shapes, different origin: keeps Chat on
		// /compatible-mode/v1 and Messages on /apps/anthropic.
		base := strings.TrimRight(m.BaseOverride, "/")
		oi, ai := strings.Index(openAI, "://"), strings.Index(anthropic, "://")
		openAI = base + openAI[oi+3+strings.Index(openAI[oi+3:], "/"):]
		anthropic = base + anthropic[ai+3+strings.Index(anthropic[ai+3:], "/"):]
	}
	return openAI, anthropic
}

func normalizeModelStudioRegion(region string) string {
	r := strings.ToLower(strings.TrimSpace(region))
	for _, known := range ModelStudioRegions() {
		if r == known {
			return r
		}
	}
	return ModelStudioRegionDefault
}

func (c *Config) normalizeModelStudio() error {
	m := &c.ModelStudio
	m.Region = normalizeModelStudioRegion(m.Region)
	if m.ModelSyncSec != 0 {
		if m.ModelSyncSec < 60 || m.ModelSyncSec > 86400 {
			return fmt.Errorf("modelstudio.modelSyncSec must be 0 or 60–86400")
		}
	}
	if m.Quota != nil {
		if err := normalizeQuota(m.Quota); err != nil {
			return fmt.Errorf("modelstudio: %w", err)
		}
	}
	// m.Routing is normalized by Normalize's nested routing pass once this
	// function has validated the rest, so a per-pool override goes through
	// exactly the same checks as the global block.
	if m.PlanOverrides != nil {
		for id, plan := range m.PlanOverrides {
			if strings.TrimSpace(id) == "" {
				return fmt.Errorf("modelstudio.planOverrides: empty account id")
			}
			if plan != ModelStudioPlanPayAsYouGo && plan != ModelStudioPlanTokenPlan {
				return fmt.Errorf("modelstudio.planOverrides[%s]: plan must be %s or %s", id, ModelStudioPlanPayAsYouGo, ModelStudioPlanTokenPlan)
			}
		}
	}
	if m.Console.Endpoint == "" {
		m.Console.Endpoint = ModelStudioConsoleEndpoint
	}
	if m.BaseOverride != "" {
		m.BaseOverride = strings.TrimRight(strings.TrimSpace(m.BaseOverride), "/")
		u, err := url.Parse(m.BaseOverride)
		if err != nil || !modelStudioURLValid(m.BaseOverride) || u.Path != "" {
			return fmt.Errorf("modelstudio.baseOverride must be a bare HTTP(S) origin without a path")
		}
	}
	// The console reader has no implementation yet. Enabled without a
	// session would promise credits and then fail every poll, so it is
	// refused rather than silently doing nothing.
	if m.Console.Enabled {
		return fmt.Errorf("modelstudio.console: the console Credits reader is not implemented yet (set enabled=false)")
	}
	errors, retries := rules.DefaultModelStudioErrorRules(), rules.DefaultModelStudioRetryRules()
	if c.ModelStudioErrorRules == nil || reflect.DeepEqual(c.ModelStudioErrorRules, []rules.ErrorRule{}) {
		c.ModelStudioErrorRules = errors
	}
	if c.ModelStudioRetryRules == nil || reflect.DeepEqual(c.ModelStudioRetryRules, []rules.RetryRule{}) {
		c.ModelStudioRetryRules = retries
	}
	if err := rules.ValidateErrorRules(c.ModelStudioErrorRules); err != nil {
		return fmt.Errorf("modelstudio: %w", err)
	}
	if err := rules.ValidateRetryRules(c.ModelStudioRetryRules); err != nil {
		return fmt.Errorf("modelstudio: %w", err)
	}
	return nil
}

// ModelStudioPlan is the plan an account is served under.
//
// Precedence matters because both plans share the "sk-" prefix:
//   - "sk-sp-" is unambiguous and always wins (a Token Plan key must never be
//     sent to a metered host, whatever an override says);
//   - a bare "sk-" key could be either, so an explicit override wins — that is
//     the whole point of the override;
//   - otherwise a bare "sk-" key is pay-as-you-go.
func (c *Config) ModelStudioPlan(a Account) string {
	if !strings.HasPrefix(a.RuntimeKey, ModelStudioTokenPlanKeyPrefix) && c.ModelStudio.PlanOverrides != nil {
		if p := c.ModelStudio.PlanOverrides[a.ID]; p == ModelStudioPlanTokenPlan || p == ModelStudioPlanPayAsYouGo {
			return p
		}
	}
	if p := modelStudioPlanForKey(a.RuntimeKey); p != "" {
		return p
	}
	return ModelStudioPlanPayAsYouGo
}

// ModelStudioKeyPlan is the plan a raw key implies, for import previews and
// validation before an account exists.
func ModelStudioKeyPlan(key string) string {
	if p := modelStudioPlanForKey(key); p != "" {
		return p
	}
	return ModelStudioPlanPayAsYouGo
}

// ModelStudioInferenceBase resolves an account's access root per protocol.
// Unlike OpenCode there is no per-model override: the plan and the region
// alone decide the host.
func (c *Config) ModelStudioInferenceBase(a Account, protocol string) string {
	openAI, anthropic := c.ModelStudio.EndpointsFor(c.ModelStudioPlan(a))
	// Callers name the protocol either way: the proxy passes "anthropic"
	// (the downstream protocol) while OpenCode's override passes "messages"
	// (the endpoint's own name). Both must land on the Anthropic root.
	if protocol == "messages" || protocol == "anthropic" {
		return anthropic
	}
	return openAI
}

// ValidateModelStudioRegion reports an unknown region before it reaches the
// normalizer, so an API save fails instead of silently falling back.
func ValidateModelStudioRegion(region string) error {
	r := strings.ToLower(strings.TrimSpace(region))
	if r == "" {
		return nil
	}
	if !slices.Contains(ModelStudioRegions(), r) {
		return fmt.Errorf("region must be one of %s", strings.Join(ModelStudioRegions(), ", "))
	}
	return nil
}

// modelStudioURLValid keeps the configured endpoint shapes honest.
func modelStudioURLValid(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return true
}
