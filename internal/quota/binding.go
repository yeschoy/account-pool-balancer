package quota

import "account-pool-balancer/internal/config"

// ResponseBindingSource pins server-side state to the credential and dial target.
// Keep the existing OpenCode hash stable for persisted bindings on upgrades.
func ResponseBindingSource(cfg *config.Config, a config.Account) string {
	if config.IsOpenCode(a.Provider) {
		return OpenCodeSource(cfg, a)
	}
	return config.HashKey(a.Provider + "\x00" + a.RuntimeKey + "\x00" + a.ProxyURL + "\x00" + cfg.RuntimeBase(a))
}
