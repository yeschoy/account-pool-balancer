package server

import (
	"strings"
	"time"

	"account-pool-balancer/internal/config"
)

func goPolicyRefusal(status int, body string) bool {
	if status != 403 {
		return false
	}
	lower := strings.ToLower(body)
	for _, code := range []string{"policy_violation", "account_suspended", "account disabled", "access has been suspended", "terms of service violation"} {
		if strings.Contains(lower, code) {
			return true
		}
	}
	return false
}

func goNetworkAbuse(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "abusive traffic") || strings.Contains(lower, "abuse traffic") || strings.Contains(lower, "abuse of traffic")
}

func sharesGoEgress(a, b config.Account) bool {
	return a.ProxyURL == b.ProxyURL || a.EgressIP != "" && a.EgressIP == b.EgressIP
}

// Reuse persisted pool cooldowns, not a second ephemeral breaker. Explicit
// network abuse pauses Go accounts sharing the known pin/configured egress;
// credential-specific policy refusals pause only that account.
func (s *Server) pauseGoPolicy(cfg *config.Config, a config.Account, body, rule string) time.Duration {
	d := time.Duration(cfg.OpenCode.GoPolicyCooldownSec) * time.Second
	until := time.Now().Add(d)
	shared := goNetworkAbuse(body)
	for _, peer := range cfg.Accounts {
		if peer.Provider != config.ProviderOpenCodeGo || (peer.ID != a.ID && (!shared || !sharesGoEgress(peer, a))) {
			continue
		}
		s.pool.Cooldown(peer.ID, "", until, 403, "Go policy refusal; manual review recommended", rule)
	}
	return d
}
