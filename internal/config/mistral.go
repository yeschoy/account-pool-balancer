package config

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"time"

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
	BaseURL         string   `json:"baseUrl"`
	Routing         *Routing `json:"routing,omitempty"`
	Quota           *Quota   `json:"quota,omitempty"` // nil inherits the shared polling settings
	ModelSyncSec    *int     `json:"modelSyncSec,omitempty"`
	DetailLog       *bool    `json:"detailLog,omitempty"`       // nil retains the shared legacy switch
	BindingCapacity *int     `json:"bindingCapacity,omitempty"` // nil/0: legacy shared4096; positive: independent ordinary sessions
	// WorkspaceQuotaTTLSec is how long one successful budget read serves every
	// key of the same workspace. Mistral bills per workspace, not per key, so a
	// second key's read would return the same numbers while still costing
	// requests: keys sharing a workspace coalesce onto one read for this long.
	// 0 uses the default; it is never unbounded, so usage still tracks.
	WorkspaceQuotaTTLSec *int                `json:"workspaceQuotaTtlSec,omitempty"`
	Performance          *MistralPerformance `json:"performance,omitempty"`
	// ChatMetadata (plan-key mode) makes Chat generation calls that carry no
	// metadata object of their own look like a first-party Vibe client, so a
	// subscription key's usage is routed to the workspace's Vibe allowance
	// instead of a spent developer API budget. "off" (default) sends exactly
	// what the caller sent. Keys that already send a metadata object are never
	// touched. Values are constrained so a typo cannot silently send an
	// unknown call_source that Mistral might bill as API traffic.
	ChatMetadata *MistralChatMetadata `json:"chatMetadata,omitempty"`
}

// MistralChatMetadata is the plan-key metadata template. CallSource must be
// "vibe_code" (Mistral routes any present metadata.call_source to the Vibe
// allowance; the observed first-party value is preserved rather than
// invented) and ClientName must be empty or "vibe_programmatic".
type MistralChatMetadata struct {
	Mode       string `json:"mode"`
	CallSource string `json:"callSource,omitempty"`
	ClientName string `json:"clientName,omitempty"`
}

// Mistral Chat metadata modes. "chat-key" only prepares calls actually sent
// with a CHAT (Vibe) key; "all-keys" prepares every key of the pool. Both are
// prepared before admission so the prepared body identifies the payload.
const (
	MistralChatMetadataOff     = "off"
	MistralChatMetadataChatKey = "chat-key"
	MistralChatMetadataAllKeys = "all-keys"
)

const (
	MistralCallSourceVibeCode       = "vibe_code"
	MistralClientNameVibeProgrammat = "vibe_programmatic"
)

// EffectiveChatMetadata validates the template and reports whether it may be
// applied. "off"/absent means no injection at all.
func (m Mistral) EffectiveChatMetadata() (MistralChatMetadata, bool) {
	c := m.ChatMetadata
	if c == nil || c.Mode == "" || c.Mode == MistralChatMetadataOff {
		return MistralChatMetadata{}, false
	}
	return *c, true
}

// PlanKeyForVibe reports whether the pool sends the Vibe client marker to
// Chat/Responses with Vibe keys, i.e. whether a CHAT key's usage is billed to
// vibe_budget rather than api_budget. Ranking, totals and the console read this
// one function so they cannot disagree about which window is the real balance.
func (m Mistral) PlanKeyForVibe() bool {
	_, on := m.EffectiveChatMetadata()
	return on
}

// chatMetadataError validates a submitted template. It is separate from
// normalization so a bad value is reported to the administrator rather than
// silently reset to off.
func chatMetadataError(c *MistralChatMetadata) error {
	if c == nil {
		return nil
	}
	switch c.Mode {
	case "", MistralChatMetadataOff:
		return nil
	case MistralChatMetadataChatKey, MistralChatMetadataAllKeys:
	default:
		return fmt.Errorf("mistral.chatMetadata.mode must be off, chat-key or all-keys")
	}
	if s := c.CallSource; s != "" && s != MistralCallSourceVibeCode {
		return fmt.Errorf("mistral.chatMetadata.callSource must be empty or %q", MistralCallSourceVibeCode)
	}
	if s := c.ClientName; s != "" && s != MistralClientNameVibeProgrammat {
		return fmt.Errorf("mistral.chatMetadata.clientName must be empty or %q", MistralClientNameVibeProgrammat)
	}
	return nil
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

// DefaultWorkspaceQuotaTTLSec is how long one workspace's successful budget
// read is reused by its other keys when nothing is configured.
const DefaultWorkspaceQuotaTTLSec = 600

// WorkspaceQuotaTTL is the effective coalescing window for workspace budgets.
func (m Mistral) WorkspaceQuotaTTL() time.Duration {
	if v := m.WorkspaceQuotaTTLSec; v != nil && *v > 0 {
		return time.Duration(*v) * time.Second
	}
	return time.Duration(DefaultWorkspaceQuotaTTLSec) * time.Second
}

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
	switch v := m.WorkspaceQuotaTTLSec; {
	case v == nil || *v == 0:
	case *v < 0 || *v > 86400:
		return fmt.Errorf("mistral.workspaceQuotaTtlSec must be 0 (default) or 1–86400")
	}
	if c := m.ChatMetadata; c != nil {
		c.Mode = strings.TrimSpace(c.Mode)
		c.CallSource = strings.TrimSpace(c.CallSource)
		c.ClientName = strings.TrimSpace(c.ClientName)
		if c.Mode == "" {
			c.Mode = MistralChatMetadataOff
		}
		// An unset call_source means the documented default, not the empty
		// string: an empty key would produce a metadata object that sends the
		// request back to the API budget it was meant to escape.
		if c.CallSource == "" {
			c.CallSource = MistralCallSourceVibeCode
		}
	}
	if err := chatMetadataError(m.ChatMetadata); err != nil {
		return err
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
	// Four factory error lists have shipped: the original one (no 3800
	// service rule), the tier/capacity one (3800 first), the one that also
	// cools a spent api_budget to its reset (2303), and now the same list with
	// that rule in its final place. Only an exact, unmodified older list is
	// replaced; any administrator edit (notes/order/items/disabled/empty
	// included) is kept as it is.
	if c.MistralErrorRules == nil || !reflect.DeepEqual(c.MistralErrorRules, errors) && isShippedMistralErrorList(c.MistralErrorRules) {
		c.MistralErrorRules = errors
	}
	if c.MistralRetryRules == nil || isShippedMistralRetryList(c.MistralRetryRules) {
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
	return StrictResponses(provider) || provider == ProviderMistral || provider == ProviderDeploy
}

// The factory lists that have shipped, written out rather than derived from
// the current defaults: a derived copy would silently follow a new default
// and upgrade itself. Each was captured from the release that shipped it.
func shippedMistralErrorLists() [][]rules.ErrorRule {
	// The original list, and the same list once the service rule was
	// prepended (shipping as shippedMistralRules "before-service").
	original := []rules.ErrorRule{
		{ID: "mistral-invalid-key", Scope: rules.ScopeAccount, Action: rules.ActionHardQuarantine, DownstreamStatus: 502,
			When: rules.Match{Statuses: []int{401}}, Note: "Mistral API Key 无效或已撤销，更换后恢复"},
		{ID: "mistral-insufficient-credits", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, DownstreamStatus: 503,
			When: rules.Match{Statuses: []int{402}}, Reset: &rules.Reset{Fallback: "10m", Max: "6h", Escalate: true}, Note: "余额或支出上限不足，冷却后恢复；余额未知"},
		{ID: "mistral-rate-limited", Scope: rules.ScopeAccount, Action: rules.ActionCooldown,
			When: rules.Match{Statuses: []int{429}}, Reset: &rules.Reset{Header: "Retry-After", Format: "retry-after", Fallback: "30s"}, Note: "遵守完整 Retry-After；同工作区的 Key 可能共享限额"},
		{ID: "mistral-server-error", Scope: rules.ScopeAccount, Action: rules.ActionCooldown,
			When: rules.Match{Statuses: []int{500, 502, 503, 504}}, Reset: &rules.Reset{Header: "Retry-After", Format: "retry-after", Fallback: "5s"}, Note: "上游暂时故障，短暂冷却后换号"},
	}
	service := rules.ErrorRule{ID: "mistral-service-unavailable", Scope: rules.ScopeModel, Action: rules.ActionIgnore,
		When: rules.Match{Statuses: []int{503}, BodyCodes: []string{"3800"}}, Note: "上游模型服务暂不可用，不扣账号健康；停止无收益换号，由客户端稍后重试"}
	tier := rules.ErrorRule{ID: "mistral-tier-not-allowed", Scope: rules.ScopeModel, Action: rules.ActionCooldown, DownstreamStatus: 503,
		When: rules.Match{Statuses: []int{403}, BodyCodes: []string{"1910"}}, Reset: &rules.Reset{Fallback: "24h"}, Note: "套餐不包含此模型：仅该账号×模型冷却24小时后复查，不隔离整号、不扣全局健康；资格变更后可手动恢复"}
	capacity := rules.ErrorRule{ID: "mistral-backend-out-of-capacity", Scope: rules.ScopeModel, Action: rules.ActionIgnore,
		When: rules.Match{Statuses: []int{429}, BodyCodes: []string{"3505"}}, Note: "上游后端容量不足，不是账号RPM或余额不足；不扣健康、不换号，保留429及Retry-After，由客户端退避"}
	// The tier/capacity release put 3800, 1910 and 3505 first. The api_budget
	// rule then shipped first (DefaultMistralErrorRules before the list was
	// ordered as it is now). Both orders of the four leading rules shipped.
	tierFirst := append([]rules.ErrorRule{service, tier, capacity}, original...)
	budgetFirst := append([]rules.ErrorRule{
		{ID: "mistral-api-budget-exhausted", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, DownstreamStatus: 503,
			When:  rules.Match{Statuses: []int{402}, BodyCodes: []string{"2303"}},
			Reset: &rules.Reset{Fallback: "10m", Max: "24h", BudgetReset: true},
			Note:  "api_budget 用尽（402/2303）：冷却到该工作区 api_budget 的重置时间后自动恢复，不隔离账号、不扣全局健康；未读到重置时间时按最长 24 小时冷却并标注为估计值"},
	}, tierFirst...)
	return [][]rules.ErrorRule{{service, original[0], original[1], original[2], original[3]}, original, tierFirst, budgetFirst}
}

func isShippedMistralErrorList(rs []rules.ErrorRule) bool {
	for _, shipped := range shippedMistralErrorLists() {
		if reflect.DeepEqual(rs, shipped) {
			return true
		}
	}
	return false
}

func isShippedMistralRetryList(rs []rules.RetryRule) bool {
	request := rules.RetryRule{ID: "mistral-request-error", Decision: rules.DecisionStop,
		When: rules.Match{Statuses: []int{400, 403, 404, 405, 409, 413, 422}}, Note: "请求、模型或权限错误停止，不遍历账号"}
	service := rules.RetryRule{ID: "mistral-service-unavailable", Decision: rules.DecisionStop,
		When: rules.Match{Statuses: []int{503}, BodyCodes: []string{"3800"}}, Note: "明确的上游服务故障停止换号，避免同一失败重复等待；429保留原策略"}
	capacity := rules.RetryRule{ID: "mistral-backend-out-of-capacity", Decision: rules.DecisionStop,
		When: rules.Match{Statuses: []int{429}, BodyCodes: []string{"3505"}}, Note: "明确的后端容量不足停止本次换号；保留其他账号限流429的原有恢复策略"}
	for _, shipped := range [][]rules.RetryRule{{request}, {service, request}, {service, capacity, request}} {
		if reflect.DeepEqual(rs, shipped) {
			return true
		}
	}
	return false
}
