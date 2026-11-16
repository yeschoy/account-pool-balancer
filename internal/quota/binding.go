package quota

import "account-pool-balancer/internal/config"

// ResponseBindingSource pins server-side state to the credential and dial target.
// Keep the existing OpenCode hash stable for persisted bindings on upgrades.
func ResponseBindingSource(cfg *config.Config, a config.Account) string {
	if a.Provider == config.ProviderALTAPI {
		// The balance belongs to the console session + uid, not just the
		// inference key. A changed session must not inherit an old low value.
		return config.HashKey(cfg.NativeBindingSource(a) + "\x00" + a.ControlKey + "\x00" + a.UserID)
	}
	if config.IsOpenCode(a.Provider) {
		return OpenCodeSource(cfg, a)
	}
	return cfg.NativeBindingSource(a)
}
