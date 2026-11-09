package config

// Derived indexes belong to an immutable published snapshot. They are neither
// serialized nor shared with Clone's mutable update copy. Provider views share
// them because their account/credential slices and upstream sources are equal.
type configIndex struct {
	accounts     map[string]int
	groups       map[string]int
	clients      map[string]int
	clientHashes map[string]int
	imports      map[string]int
	sources      map[string]accountSource
}

type accountSource struct {
	provider, key, proxy, base, hash string
}

func (c *Config) buildIndex() {
	x := &configIndex{accounts: map[string]int{}, groups: map[string]int{}, clients: map[string]int{}, clientHashes: map[string]int{}, imports: map[string]int{}, sources: map[string]accountSource{}}
	for i, a := range c.Accounts {
		x.accounts[a.ID] = i
		if _, ok := x.groups[a.Group]; !ok {
			x.groups[a.Group] = i
		}
		base := c.RuntimeBase(a)
		x.sources[a.ID] = accountSource{a.Provider, a.RuntimeKey, a.ProxyURL, base, HashKey(a.Provider + "\x00" + a.RuntimeKey + "\x00" + a.ProxyURL + "\x00" + base)}
	}
	for i, k := range c.ClientKeys {
		x.clients[k.ID] = i
		x.clientHashes[k.KeyHash] = i
	}
	for i, k := range c.ImportKeys {
		x.imports[k.KeyHash] = i
	}
	c.index = x
}

func (c *Config) ClientKey(id string) (ClientKey, bool) {
	if c.index != nil {
		if i, ok := c.index.clients[id]; ok && i < len(c.ClientKeys) && c.ClientKeys[i].ID == id {
			return c.ClientKeys[i], true
		}
	}
	for _, k := range c.ClientKeys {
		if k.ID == id {
			return k, true
		}
	}
	return ClientKey{}, false
}

// NativeBindingSource is the existing non-OpenCode credential/source hash. A
// value from an unpublished/test-mutated config is computed normally, never
// trusted merely because the account ID matched a previously indexed entry.
func (c *Config) NativeBindingSource(a Account) string {
	base := c.RuntimeBase(a)
	if c.index != nil {
		if v, ok := c.index.sources[a.ID]; ok && v.provider == a.Provider && v.key == a.RuntimeKey && v.proxy == a.ProxyURL && v.base == base {
			return v.hash
		}
	}
	return HashKey(a.Provider + "\x00" + a.RuntimeKey + "\x00" + a.ProxyURL + "\x00" + base)
}
