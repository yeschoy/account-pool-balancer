package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"account-pool-balancer/internal/rules"
)

func TestDeployIdentityAndDefaults(t *testing.T) {
	c := Default()
	c.Routing.Strategy = "ordered"
	c.Quota.Enabled = false
	for _, key := range []string{"opaque+/secret==", "sk-overlap-secret", "rv_overlap_secret"} {
		if ResolveProvider(ProviderDeploy, key) != ProviderDeploy || !ValidRuntimeKeyFor(ProviderDeploy, key) {
			t.Fatal("explicit Deploy must preserve opaque key ownership")
		}
	}
	for _, key := range []string{"short", "has space secret", "secret\nnewline"} {
		if ValidRuntimeKeyFor(ProviderDeploy, key) {
			t.Fatal("invalid key accepted")
		}
	}
	if !KnownProvider(ProviderDeploy) || GroupForProvider(ProviderDeploy) != "deploy" || c.GroupPool("deploy") != ProviderDeploy || !WriteOnlyCredentials(ProviderDeploy) {
		t.Fatal("pool not registered or isolated")
	}
	got := c.ForProvider(ProviderDeploy)
	if got.Routing.Strategy != "least-connections" || got.Routing.WalletFallback || !got.Quota.Enabled || got.Quota.IntervalSec != 600 {
		t.Fatal("Deploy inherited global settings")
	}
	if c.RuntimeBase(Account{Provider: ProviderDeploy}) != DeployDefaultBase || len(got.Pricing.Models) != 2 {
		t.Fatal("wrong root/pricing")
	}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
}
func TestDeployConfigRoundTripAndIsolation(t *testing.T) {
	c := Default()
	r := c.Deploy.EffectiveRouting()
	r.Strategy = "weighted"
	r.MaxAccounts = 3
	r.DirectMaxConns = 7
	r.ProxyMaxConns = 2
	r.SessionHeaders = []string{"X-Deploy-Session"}
	q := c.Deploy.EffectiveQuota()
	q.Enabled = false
	q.Concurrency = 5
	q.IntervalSec = 120
	c.Deploy.Routing, c.Deploy.Quota = &r, &q
	c.Deploy.DetailLog = true
	c.Deploy.ModelSyncSec = 0
	c.DeployErrorRules = []rules.ErrorRule{}
	c.DeployRetryRules = []rules.RetryRule{}
	c.Accounts = []Account{{ID: "d", Provider: ProviderDeploy, RuntimeKey: "opaque+/example==", Enabled: true}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c)
	out := Default()
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	if err := out.Normalize(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Deploy, out.Deploy) || len(out.DeployErrorRules) != 0 || len(out.DeployRetryRules) != 0 || out.Accounts[0].Group != "deploy" {
		t.Fatal("round trip lost fields or reinstated cleared rules")
	}
	if out.Routing.Strategy == "weighted" || out.Quota.Concurrency == 5 || !reflect.DeepEqual(out.ErrorRules, c.ErrorRules) {
		t.Fatal("Deploy changed another pool")
	}
	clone := out.Clone()
	clone.Deploy.Routing.SessionHeaders[0] = "X-Changed"
	if out.Deploy.Routing.SessionHeaders[0] == "X-Changed" {
		t.Fatal("clone alias")
	}
}
func TestDeployValidationAndReservedGroups(t *testing.T) {
	funding := Default()
	funding.DeployErrorRules = []rules.ErrorRule{{ID: "invalid-funding", Scope: rules.ScopeFunding, Action: rules.ActionCooldown, When: rules.Match{Statuses: []int{402}}, Reset: &rules.Reset{Fallback: "1m"}}}
	if funding.Normalize() == nil {
		t.Fatal("unproven funding scope accepted")
	}
	for _, base := range []string{"https://example.test", "https://u:p@example.test/api/v1", "https://example.test/api/v1?key=secret", "ftp://example.test/v1"} {
		c := Default()
		c.Deploy.BaseURL = base
		if c.Normalize() == nil {
			t.Fatal("unsafe root accepted")
		}
	}
	for _, sync := range []int{-1, 1, 59, 86401} {
		c := Default()
		c.Deploy.ModelSyncSec = sync
		if c.Normalize() == nil {
			t.Fatal("bad sync accepted")
		}
	}
	for _, group := range []string{"default", "mistral", "modelstudio", "opencode-go"} {
		c := Default()
		c.Accounts = []Account{{ID: "d", Group: group, Provider: ProviderDeploy, RuntimeKey: "opaque_deploy_secret"}}
		if c.Normalize() == nil {
			t.Fatal("foreign reserved group accepted", group)
		}
	}
	c := Default()
	c.Accounts = []Account{{ID: "od", Group: "deploy", RuntimeKey: "sk-amr-example"}}
	if err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "group") {
		t.Fatal("foreign account claimed Deploy reserved group")
	}
}
