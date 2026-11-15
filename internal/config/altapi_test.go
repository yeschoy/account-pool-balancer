package config

import "testing"

// altapi.ai is an opaque-key pool: the key has no provider prefix, so the
// declaration must win and must be accepted as a legitimate provider.
func TestALTAPIProviderRegistration(t *testing.T) {
	if !KnownProvider(ProviderALTAPI) {
		t.Fatal("altapi is not a known provider")
	}
	if got := GroupForProvider(ProviderALTAPI); got != ProviderALTAPI {
		t.Fatalf("group %q, want %q", got, ProviderALTAPI)
	}
	if got := ResolveProvider(ProviderALTAPI, "1AgxNApHSVWO2ofXIladoNLnCIEMOCT9gF5CZibnQAeeHAHF"); got != ProviderALTAPI {
		t.Fatalf("resolved %q, want altapi (declaration must win over an opaque key)", got)
	}
	// The 48-char bare value and the sk- form are both what altapi issues.
	for _, key := range []string{
		"1AgxNApHSVWO2ofXIladoNLnCIEMOCT9gF5CZibnQAeeHAHF",
		"sk-1AgxNApHSVWO2ofXIladoNLnCIEMOCT9gF5CZibnQAeeHAHF",
	} {
		if !ValidRuntimeKeyFor(ProviderALTAPI, key) {
			t.Fatalf("key %q rejected", key)
		}
	}
	if ValidRuntimeKeyFor(ProviderALTAPI, "short") {
		t.Fatal("a 5-char key was accepted")
	}
}

// The balance formula the pool reports must equal the console's own cards:
// Current balance = quota / quota_per_unit.
func TestALTAPIQuotaPerUnitGovernsTheConsoleFigure(t *testing.T) {
	c := Default()
	if c.ALTAPI.QuotaPerUnit != 500000 {
		t.Fatalf("quota_per_unit %v, want 500000 (measured on altapi.ai)", c.ALTAPI.QuotaPerUnit)
	}
	// A fresh account: 5000000 / 500000 = $10.00, which is the $10.00 the
	// console shows and what the registrar measured.
	if got := 5000000 / c.ALTAPI.QuotaPerUnit; got != 10 {
		t.Fatalf("fresh account balance %v USD, want 10", got)
	}
}

func TestALTAPIBaseURLMustEndInV1(t *testing.T) {
	c := Default()
	c.ALTAPI.BaseURL = "https://altapi.ai"
	if err := c.normalizeALTAPI(); err == nil {
		t.Fatal("a root without /v1 was accepted")
	}
	c.ALTAPI.BaseURL = "https://altapi.ai/v1"
	if err := c.normalizeALTAPI(); err != nil {
		t.Fatalf("valid root rejected: %v", err)
	}
	if got := ALTAPIOrigin(c.ALTAPI.BaseURL); got != "https://altapi.ai" {
		t.Fatalf("origin %q", got)
	}
}

// Funding scope needs a proven shared billing source. altapi has exactly one
// wallet per account, but the config still refuses the scope so a later edit
// cannot silently cool an entire account for one model's refusal.
func TestALTAPIRefusesFundingScope(t *testing.T) {
	c := Default()
	c.ALTAPIErrorRules[0].Scope = "funding"
	if err := c.normalizeALTAPI(); err == nil {
		t.Fatal("funding scope accepted")
	}
}

func TestALTAPIRulesAreWiredIntoTheConfig(t *testing.T) {
	c := Default()
	er, rr := c.RulesFor(ProviderALTAPI)
	if len(er) == 0 || len(rr) == 0 {
		t.Fatalf("rules missing: %d error, %d retry", len(er), len(rr))
	}
	if got := DefaultErrorRulesFor(ProviderALTAPI); len(got) == 0 {
		t.Fatal("defaults-for returns nothing")
	}
	if got := DefaultRetryRulesFor(ProviderALTAPI); len(got) == 0 {
		t.Fatal("retry defaults-for returns nothing")
	}
}
