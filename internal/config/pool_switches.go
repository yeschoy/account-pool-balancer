package config

import "fmt"

// PoolSwitches is sparse for backwards compatibility: missing means enabled.
// It never changes an account's own Enabled flag or removes business data.
func (c *Config) PoolEnabled(provider string) bool {
	if c == nil {
		return false
	}
	v, ok := c.PoolSwitches[provider]
	return !ok || v
}
func ManagedPools() []string         { return append(append([]string(nil), Providers...), "telnyx") }
func KnownManagedPool(p string) bool { return KnownProvider(p) || p == "telnyx" }
func (c *Config) normalizePoolSwitches() error {
	for p := range c.PoolSwitches {
		if !KnownManagedPool(p) {
			return fmt.Errorf("poolSwitches: unknown pool %q", p)
		}
	}
	return nil
}
