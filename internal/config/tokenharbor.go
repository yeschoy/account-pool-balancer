package config

import (
	"fmt"
	"net/url"
	"strings"

	"account-pool-balancer/internal/egressid"
	"account-pool-balancer/internal/rules"
)

// TokenHarborRegistrationMax is shared by API, settings and worker admission.
const TokenHarborRegistrationMax = 5

func (c *Config) normalizeTokenHarbor() error {
	t := &c.TokenHarbor
	// Legacy configs could carry 0..10 while the worker was fixed at one.
	if t.RegisterConcurrency <= 0 {
		t.RegisterConcurrency = 1
	} else if t.RegisterConcurrency > TokenHarborRegistrationMax {
		t.RegisterConcurrency = TokenHarborRegistrationMax
	}
	if t.BaseURL == "" {
		t.BaseURL = TokenHarborDefaults().BaseURL
	}
	t.BaseURL = strings.TrimRight(t.BaseURL, "/")
	u, err := url.Parse(t.BaseURL)
	if err != nil || len(t.BaseURL) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || !strings.HasSuffix(u.Path, "/v1") {
		return fmt.Errorf("tokenHarbor.baseUrl: HTTP(S) API base must include /v1, without credentials, query or fragment")
	}
	if t.ModelSyncSec != 0 {
		if err := clampInt(&t.ModelSyncSec, 60, 86400, "tokenHarbor.modelSyncSec"); err != nil {
			return err
		}
	}
	if t.AccountSyncSec != nil && *t.AccountSyncSec != 0 {
		if err := clampInt(t.AccountSyncSec, 60, 300, "tokenHarbor.accountSyncSec"); err != nil {
			return err
		}
	}
	// A region the zone table does not model cannot make the timezone more
	// believable, so it is rejected rather than silently ignored: an operator
	// who typed "us " or "USA" should learn that, not ship a UTC pool again.
	if region := strings.ToUpper(strings.TrimSpace(t.EgressRegion)); region != "" {
		known := false
		for _, r := range egressid.Regions() {
			if r == region {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("tokenHarbor.egressRegion: %q is not a modelled egress region (want one of %s, or empty)", t.EgressRegion, strings.Join(egressid.Regions(), ", "))
		}
		t.EgressRegion = region
	} else {
		t.EgressRegion = ""
	}
	if c.TokenHarborErrorRules == nil {
		c.TokenHarborErrorRules = rules.DefaultTokenHarborErrorRules()
	}
	if c.TokenHarborRetryRules == nil {
		c.TokenHarborRetryRules = rules.DefaultTokenHarborRetryRules()
	}
	if err := rules.ValidateErrorRules(c.TokenHarborErrorRules); err != nil {
		return fmt.Errorf("tokenharbor: %w", err)
	}
	if err := rules.ValidateRetryRules(c.TokenHarborRetryRules); err != nil {
		return fmt.Errorf("tokenharbor: %w", err)
	}
	return nil
}

func (t TokenHarbor) AccountInterval() int {
	if t.AccountSyncSec == nil {
		return 300
	}
	return *t.AccountSyncSec
}

// StrictResponses pools persist confirmed, credential-bound response IDs and
// refuse to guess an account for a server-side continuation.
func StrictResponses(provider string) bool {
	return IsOpenCode(provider) || provider == ProviderTokenHarbor
}
