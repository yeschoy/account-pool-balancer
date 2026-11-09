package stats

// Relink equal sole-provider account rings after reading legacy JSON, which
// intentionally still contains both projections. Mixed-provider account IDs
// keep independent aggregates and never alias a provider's private counters.
func (s *Stats) relinkAccountsLocked() {
	owners := map[string]string{}
	for provider, sc := range s.prov {
		for id := range sc.account {
			if _, ok := owners[id]; ok {
				owners[id] = "\x00multiple"
			} else {
				owners[id] = provider
			}
		}
	}
	for id, r := range s.all.account {
		owner, ok := owners[id]
		if ok && owner != "\x00multiple" && sameRing(r, s.prov[owner].account[id]) {
			s.prov[owner].account[id] = r
			s.accountOwner[id] = owner
		} else {
			s.accountOwner[id] = "\x00multiple"
		}
	}
}
