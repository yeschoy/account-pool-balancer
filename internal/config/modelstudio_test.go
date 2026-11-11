package config

import (
	"strings"
	"testing"
)

// The two plans answer on different hosts, and using the wrong pair is the
// documented cause of a 401 that looks like a dead key.
func TestModelStudioPlanEndpoints(t *testing.T) {
	m := DefaultModelStudio()
	openAI, anthropic := m.EndpointsFor(ModelStudioPlanTokenPlan)
	if openAI != "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("token plan openai root = %q", openAI)
	}
	if anthropic != "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic/v1" {
		t.Fatalf("token plan anthropic root = %q", anthropic)
	}
	openAI, _ = m.EndpointsFor(ModelStudioPlanPayAsYouGo)
	if openAI != "https://dashscope-intl.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("payg openai root = %q", openAI)
	}
	// a metered key must never be sent to a Token Plan host, nor the reverse
	m.Region = ModelStudioRegionBeijing
	tp, _ := m.EndpointsFor(ModelStudioPlanTokenPlan)
	payg, _ := m.EndpointsFor(ModelStudioPlanPayAsYouGo)
	if !strings.Contains(tp, "token-plan.cn-beijing") || !strings.Contains(payg, "dashscope.aliyuncs.com") {
		t.Fatalf("beijing endpoints: %q %q", tp, payg)
	}
}

// "sk-sp-" must be recognised as the subscription key before the generic
// "sk-" rule, or a Token Plan key would be treated as a metered key.
func TestModelStudioKeyPrefixOrder(t *testing.T) {
	if got := ModelStudioKeyPlan("sk-sp-H.abc.def"); got != ModelStudioPlanTokenPlan {
		t.Fatalf("sk-sp plan = %q", got)
	}
	if got := ModelStudioKeyPlan("sk-plainkey"); got != ModelStudioPlanPayAsYouGo {
		t.Fatalf("sk plan = %q", got)
	}
	if got := ProviderForKey("sk-sp-H.abc.def"); got != ProviderModelStudio {
		t.Fatalf("sk-sp provider = %q, want %q", got, ProviderModelStudio)
	}
	// OpenDesign's own keys keep their pool
	if got := ProviderForKey("sk-amr-something"); got != ProviderOpenDesign {
		t.Fatalf("sk-amr provider = %q", got)
	}
	if !ModelStudioBareSKKey("sk-sp-x") || ModelStudioBareSKKey("sk-amr-x") {
		t.Fatal("bare-sk classification must exclude sk-amr-")
	}
	if !ValidRuntimeKeyFor(ProviderModelStudio, "sk-sp-synthetic-plan-key") {
		t.Fatal("token plan key rejected")
	}
	if !ValidRuntimeKeyFor(ProviderModelStudio, "sk-plain") {
		t.Fatal("payg key rejected")
	}
	if ValidRuntimeKeyFor(ProviderModelStudio, "thk_live_x") {
		t.Fatal("foreign key accepted by modelstudio")
	}
}

// A "sk-sp-" key belongs to Model Studio even on a fresh deployment, and a
// bare "sk-" key is only inferred once the deployment runs the pool.
func TestModelStudioKeyOwnership(t *testing.T) {
	c := Default()
	if p, ok := c.ModelStudioKeyOwnership("", "sk-sp-H.x"); !ok || p != ProviderModelStudio {
		t.Fatalf("fresh sk-sp: %q %v", p, ok)
	}
	if p, ok := c.ModelStudioKeyOwnership("", "sk-bare"); ok || p != "" {
		t.Fatalf("fresh bare sk auto-claimed: %q %v", p, ok)
	}
	c.Accounts = []Account{{ID: "a", Provider: ProviderModelStudio, Group: ModelStudioGroup, RuntimeKey: "sk-sp-a", Enabled: true}}
	if p, ok := c.ModelStudioKeyOwnership("", "sk-bare"); !ok || p != ProviderModelStudio {
		t.Fatalf("opted-in bare sk: %q %v", p, ok)
	}
	// an explicit other pool is never overridden
	if p, ok := c.ModelStudioKeyOwnership(ProviderRiver, "sk-bare"); ok || p != ProviderRiver {
		t.Fatalf("declared river overridden: %q %v", p, ok)
	}
}

func TestModelStudioAccountPlanResolution(t *testing.T) {
	c := Default()
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	sp := Account{ID: "sp", Provider: ProviderModelStudio, Group: ModelStudioGroup, RuntimeKey: "sk-sp-abc", Enabled: true}
	if got := c.ModelStudioPlan(sp); got != ModelStudioPlanTokenPlan {
		t.Fatalf("sk-sp account plan = %q", got)
	}
	bare := Account{ID: "bare", Provider: ProviderModelStudio, Group: ModelStudioGroup, RuntimeKey: "sk-bare", Enabled: true}
	if got := c.ModelStudioPlan(bare); got != ModelStudioPlanPayAsYouGo {
		t.Fatalf("bare account plan = %q", got)
	}
	// an override can pin a bare key that really is a subscription key
	c.ModelStudio.PlanOverrides = map[string]string{"bare": ModelStudioPlanTokenPlan}
	if got := c.ModelStudioPlan(bare); got != ModelStudioPlanTokenPlan {
		t.Fatalf("override ignored: %q", got)
	}
	// the prefix still wins over a contradictory override, because the
	// "sk-sp-" prefix is unambiguous
	c.ModelStudio.PlanOverrides = map[string]string{"sp": ModelStudioPlanPayAsYouGo}
	if got := c.ModelStudioPlan(sp); got != ModelStudioPlanTokenPlan {
		t.Fatalf("override beat a definitive prefix: %q", got)
	}
}

func TestModelStudioInferenceBasePerProtocol(t *testing.T) {
	c := Default()
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	a := Account{ID: "sp", Provider: ProviderModelStudio, Group: ModelStudioGroup, RuntimeKey: "sk-sp-abc", Enabled: true}
	if got := c.InferenceBase(a, "messages"); got != "https://token-plan.ap-southeast-1.maas.aliyuncs.com/apps/anthropic/v1" {
		t.Fatalf("messages base = %q", got)
	}
	if got := c.InferenceBase(a, "openai"); got != "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1" {
		t.Fatalf("chat base = %q", got)
	}
	// RuntimeBase (used by /v1/models and the account test) is the OpenAI root
	if got := c.RuntimeBase(a); got != c.InferenceBase(a, "chat") {
		t.Fatalf("RuntimeBase = %q", got)
	}
}

// The console reader is not implemented; enabling it must fail loudly rather
// than promise a balance that can never be read.
func TestModelStudioConsoleRefusedUntilImplemented(t *testing.T) {
	c := Default()
	c.ModelStudio.Console.Enabled = true
	if err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("enabling the console reader must be refused, got %v", err)
	}
	c = Default()
	if err := c.Normalize(); err != nil {
		t.Fatalf("default config must be valid: %v", err)
	}
	if c.ModelStudio.Console.Endpoint != ModelStudioConsoleEndpoint {
		t.Fatalf("console endpoint = %q", c.ModelStudio.Console.Endpoint)
	}
}

func TestModelStudioRegionAndSettingsValidation(t *testing.T) {
	c := Default()
	c.ModelStudio.Region = "Mars"
	if err := c.Normalize(); err != nil {
		t.Fatalf("unknown region must fall back, not fail: %v", err)
	}
	if c.ModelStudio.Region != ModelStudioRegionDefault {
		t.Fatalf("region = %q", c.ModelStudio.Region)
	}
	c = Default()
	c.ModelStudio.PlanOverrides = map[string]string{"a": "bogus"}
	if err := c.Normalize(); err == nil {
		t.Fatal("invalid plan override accepted")
	}
	c = Default()
	c.ModelStudio.ModelSyncSec = 5
	if err := c.Normalize(); err == nil {
		t.Fatal("too-short modelSyncSec accepted")
	}
	if err := ValidateModelStudioRegion("eu-central-1"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateModelStudioRegion("nowhere"); err == nil {
		t.Fatal("unknown region accepted by the API validator")
	}
}

// Pool isolation: Model Studio rules and routing must never be read for, or
// leak into, another pool.
func TestModelStudioPoolIsolation(t *testing.T) {
	c := Default()
	c.Accounts = []Account{
		{ID: "ms", Provider: ProviderModelStudio, Group: ModelStudioGroup, RuntimeKey: "sk-sp-abc", Enabled: true},
		{ID: "th", Provider: ProviderTokenHarbor, Group: TokenHarborGroup, RuntimeKey: "thk_live_abc", Enabled: true},
	}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	msErr, _ := c.RulesFor(ProviderModelStudio)
	if len(msErr) == 0 || msErr[0].ID != "modelstudio-quota-exhausted" {
		t.Fatalf("modelstudio rules = %+v", msErr)
	}
	thErr, _ := c.RulesFor(ProviderTokenHarbor)
	for _, r := range thErr {
		if strings.HasPrefix(r.ID, "modelstudio-") {
			t.Fatal("modelstudio rule leaked into tokenharbor")
		}
	}
	// an independent routing block stays independent
	ms := c.ForProvider(ProviderModelStudio)
	if ms.Routing.MaxAccounts == 0 || ms.Quota.IntervalSec == 0 {
		t.Fatal("modelstudio view has no routing/quota")
	}
	c.ModelStudio.Routing = &Routing{MaxAccounts: 1, Strategy: "ordered"}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if c.ForProvider(ProviderModelStudio).Routing.MaxAccounts != 1 {
		t.Fatal("modelstudio routing override ignored")
	}
	if c.Routing.MaxAccounts == 1 {
		t.Fatal("modelstudio routing override leaked into the global block")
	}
	// each pool keeps its own group: a modelstudio account cannot join the
	// tokenharbor group
	bad := Default()
	bad.Accounts = []Account{
		{ID: "x", Provider: ProviderModelStudio, Group: TokenHarborGroup, RuntimeKey: "sk-sp-x", Enabled: true},
		{ID: "y", Provider: ProviderTokenHarbor, Group: TokenHarborGroup, RuntimeKey: "thk_live_y", Enabled: true},
	}
	if err := bad.Normalize(); err == nil {
		t.Fatal("modelstudio account joined another pool's group")
	}
}

// A Clone must compare equal to its source, including the new block.
func TestModelStudioCloneRoundTrip(t *testing.T) {
	c := Default()
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	cp := c.Clone()
	if cp.ModelStudio.Region != c.ModelStudio.Region || cp.ModelStudio.ModelSyncSec != c.ModelStudio.ModelSyncSec {
		t.Fatal("clone changed modelstudio settings")
	}
	if len(cp.ModelStudioErrorRules) != len(c.ModelStudioErrorRules) {
		t.Fatal("clone dropped rules")
	}
}
