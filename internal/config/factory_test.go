package config

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestFactoryConfigIsolationAndRoundTrip(t *testing.T) {
	c := Default()
	c.Routing.Strategy = "ordered"
	c.Quota.Enabled = false
	if !KnownProvider(ProviderFactory) || GroupForProvider(ProviderFactory) != "factory" || !WriteOnlyCredentials(ProviderFactory) {
		t.Fatal("provider registration")
	}
	got := c.ForProvider(ProviderFactory)
	if got.Routing.Strategy != "least-connections" || got.Routing.OnBindingBusy != "reject" || !got.Quota.Enabled {
		t.Fatal("inherited foreign settings")
	}
	// Factory ships its own Google rates; they must price its models and none
	// of the other pools'.
	if _, ok := got.Pricing.Lookup("gemini-4-carbon", time.Now()); !ok {
		t.Fatal("factory pricing defaults missing")
	}
	if _, ok := got.Pricing.Lookup("claude-sonnet-5-5", time.Now()); ok {
		t.Fatal("foreign price inherited")
	}
	c.Accounts = []Account{{ID: "f", Provider: ProviderFactory, RuntimeKey: "fk-mock_test_key_1", Enabled: true}}
	r := c.Factory.EffectiveRouting()
	r.Strategy = "weighted"
	r.SessionHeaders = []string{"X-Factory-Session"}
	c.Factory.Routing = &r
	c.FactoryErrorRules = c.FactoryErrorRules[:0]
	c.FactoryRetryRules = c.FactoryRetryRules[:0]
	c.Factory.DetailLog = true
	c.Factory.InferenceURL = "https://prem.example.test"
	c.Factory.RequestSurface = FactorySurfaceExec
	c.Factory.OpenAIPlatform = "org-abc_DEF-123"
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(c)
	out := Default()
	if json.Unmarshal(b, out) != nil {
		t.Fatal("decode")
	}
	if e := out.Normalize(); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(c.Factory, out.Factory) || len(out.FactoryErrorRules) != 0 || out.Accounts[0].Group != "factory" {
		t.Fatal("roundtrip")
	}
	clone := out.Clone()
	clone.Factory.Models[0].ID = "changed"
	clone.Factory.Routing.SessionHeaders[0] = "X-Changed"
	if reflect.DeepEqual(out.Factory, clone.Factory) {
		t.Fatal("clone alias")
	}
	if !ValidRuntimeKeyFor(ProviderFactory, "fk-mock_test_key") || ValidRuntimeKeyFor(ProviderDeploy, "fk-mock_test_key") {
		t.Fatal("key boundary")
	}
	for _, g := range []string{"deploy", "default", "mistral"} {
		x := Default()
		x.Accounts = []Account{{ID: "f", Provider: ProviderFactory, Group: g, RuntimeKey: "fk-mock_test_key"}}
		if x.Normalize() == nil {
			t.Fatal("foreign group accepted", g)
		}
	}
	for _, base := range []string{"https://u:p@example.com", "https://example.com/v1", "https://example.com?x=1", "https://example.com?", "ftp://example.com"} {
		x := Default()
		x.Factory.BaseURL = base
		if x.Normalize() == nil {
			t.Fatal("bad base", base)
		}
	}
	x := Default()
	rr := x.Factory.EffectiveRouting()
	rr.MaxAccounts = 6
	x.Factory.Routing = &rr
	if x.Normalize() == nil {
		t.Fatal("unvalidated nested routing")
	}
	// The management origin defaults to prem for inference; a custom or mock
	// origin is reused, and an explicit inference origin wins.
	if got := Default().Factory.EffectiveInferenceBase(); got != FactoryDefaultInferenceBase {
		t.Fatal("default inference host", got)
	}
	custom := Default()
	custom.Factory.BaseURL = "http://127.0.0.1:9999"
	if got := custom.Factory.EffectiveInferenceBase(); got != "http://127.0.0.1:9999" {
		t.Fatal("custom base not reused", got)
	}
	custom.Factory.InferenceURL = "https://prem.example.test/"
	custom.Factory.RequestSurface = ""
	custom.Factory.OpenAIPlatform = "abc"
	if err := custom.Normalize(); err != nil {
		t.Fatal(err)
	}
	if custom.Factory.EffectiveInferenceBase() != "https://prem.example.test" || custom.Factory.EffectiveRequestSurface() != FactorySurfaceTUI || custom.Factory.OpenAIPlatform != "org-abc" {
		t.Fatal("defaults/prefix", custom.Factory)
	}
	for _, bad := range []string{"https://example.com/v1", "ftp://example.com"} {
		y := Default()
		y.Factory.InferenceURL = bad
		if y.Normalize() == nil {
			t.Fatal("bad inference origin", bad)
		}
	}
	y := Default()
	y.Factory.RequestSurface = "desktop"
	if y.Normalize() == nil {
		t.Fatal("unsupported surface accepted")
	}
	y = Default()
	y.Factory.OpenAIPlatform = "org-<bad>"
	if y.Normalize() == nil {
		t.Fatal("bad openai platform accepted")
	}
	if got := (Factory{}).EffectiveRequestSurface(); got != FactorySurfaceTUI {
		t.Fatal("empty surface", got)
	}
}

func TestFactoryOAuthAccountValidationAndStableSource(t *testing.T) {
	c := Default()
	c.Accounts = []Account{{ID: "f", Provider: ProviderFactory, Group: "factory", RuntimeKey: "eyJhbGciOiJSUzI1NiJ9.e30.sig", FactoryRefreshToken: "refresh", UserID: "workos-user", Email: "a@example.com", FactoryOrgID: "org"}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	a := c.Accounts[0]
	src := c.NativeBindingSource(a)
	if src == "" {
		t.Fatal("oauth source empty")
	}
	a.RuntimeKey = "another-access"
	if got := c.NativeBindingSource(a); got != src {
		t.Fatal("access token rotation changed the account source")
	}
	a.FactoryOrgID = "org2"
	if got := c.NativeBindingSource(a); got != src {
		t.Fatal("organization changed the account source")
	}
	c.Accounts[0].FactoryRefreshToken = " bad token"
	if c.Normalize() == nil {
		t.Fatal("invalid refresh token accepted")
	}
}

func TestFactoryCustomListGetsMissingGeminiRoutes(t *testing.T) {
	c := Default()
	c.Factory.Models = []FactoryModel{{ID: "mine", Protocol: "chat", Upstream: "openai"}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	found, gemini := false, false
	for _, m := range c.Factory.Models {
		if m.ID == "mine" {
			found = true
		}
		if m.ID == "gemini-4-carbon" && m.Protocol == "generate" && m.Upstream == "google" {
			gemini = true
		}
	}
	if !found || !gemini {
		t.Fatalf("custom list not merged: %+v", c.Factory.Models)
	}
}

func TestFactoryProxyPoolAndUserAgentValidation(t *testing.T) {
	c := Default()
	c.Factory.ProxyPool = []string{"http://p1:8080", "http://p1:8080", "socks5://p2:1080"}
	c.Factory.UserAgents = []string{"factory-cli/1.14.0", "factory-cli/1.15.0"}
	c.Accounts = []Account{{ID: "f", Provider: ProviderFactory, Group: "factory", RuntimeKey: "fk-valid_key_123", UserAgent: "factory-cli/1.15.0"}}
	if err := c.Normalize(); err != nil {
		t.Fatal(err)
	}
	if len(c.Factory.ProxyPool) != 2 || len(c.Factory.UserAgents) != 2 {
		t.Fatalf("dedupe: %+v %+v", c.Factory.ProxyPool, c.Factory.UserAgents)
	}
	c.Factory.ProxyPool = []string{"ftp://bad:21"}
	if c.Normalize() == nil {
		t.Fatal("bad proxy scheme accepted")
	}
	c = Default()
	c.Factory.ProxyPool = []string{"http://p1:8080"}
	c.Factory.UserAgents = []string{"bad\nua"}
	if c.Normalize() == nil {
		t.Fatal("newline UA accepted")
	}
}

// Factory keeps a conversation on one account for a day, because the prompt
// cache lives per account and moving loses it.
func TestFactorySessionBindingTTL(t *testing.T) {
	c := Default()
	r := c.Factory.EffectiveRouting()
	if r.ExplicitTTLSec != 24*3600 || r.FallbackTTLSec != 24*3600 {
		t.Fatalf("factory TTLs = explicit %d fallback %d", r.ExplicitTTLSec, r.FallbackTTLSec)
	}
	// The saved settings must pass the generic bounds check too.
	c2 := Default()
	c2.Factory.Routing = &r
	if err := c2.Normalize(); err != nil {
		t.Fatal(err)
	}
}
