package config

import (
	"fmt"
	"net/url"
	"strings"

	"account-pool-balancer/internal/rules"
)

func (c *Config) normalizeTokenHarbor() error {
	t := &c.TokenHarbor
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

// StrictResponses pools persist confirmed, credential-bound response IDs and
// refuse to guess an account for a server-side continuation.
func StrictResponses(provider string) bool {
	return IsOpenCode(provider) || provider == ProviderTokenHarbor
}
