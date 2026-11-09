package pool

import (
	"container/heap"
	"container/list"
	"time"

	"account-pool-balancer/internal/config"
)

type bindingExpiry []*binding

func (h bindingExpiry) Len() int           { return len(h) }
func (h bindingExpiry) Less(i, j int) bool { return h[i].expiresAt.Before(h[j].expiresAt) }
func (h bindingExpiry) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].expiryIndex = i
	h[j].expiryIndex = j
}
func (h *bindingExpiry) Push(v any) { b := v.(*binding); b.expiryIndex = len(*h); *h = append(*h, b) }
func (h *bindingExpiry) Pop() any {
	a := *h
	b := a[len(a)-1]
	a[len(a)-1] = nil
	*h = a[:len(a)-1]
	b.expiryIndex = -1
	return b
}

func removeBindingExpiry(h *bindingExpiry, b *binding) { heap.Remove(h, b.expiryIndex) }

func (p *Pool) budgetLocked(b *binding) *list.List {
	if b.mistralPool && p.mistralCapacity > 0 {
		return p.mistralLRU
	}
	return p.sharedLRU
}

func (p *Pool) addBindingLocked(b *binding, back bool) {
	budget := p.budgetLocked(b)
	if back {
		p.bindings[b.key] = p.lru.PushBack(b)
		b.budgetElement = budget.PushBack(b)
	} else {
		p.bindings[b.key] = p.lru.PushFront(b)
		b.budgetElement = budget.PushFront(b)
	}
	heap.Push(&p.expiry, b)
	if !b.cache {
		p.bindingCounts[b.accountID]++
	}
}

func (p *Pool) moveBindingLocked(b *binding) {
	p.lru.MoveToFront(p.bindings[b.key])
	p.budgetLocked(b).MoveToFront(b.budgetElement)
	heap.Fix(&p.expiry, b.expiryIndex)
}

// Bound pruning work on inference. Admin/export can finish pruning so their
// current counts are exact without scanning every retained binding.
func (p *Pool) expireBindingsLocked(now time.Time, limit int) {
	for n := 0; len(p.expiry) > 0 && !now.Before(p.expiry[0].expiresAt) && (limit <= 0 || n < limit); n++ {
		p.deleteBindingLocked(p.expiry[0].key)
	}
}

// ConfigureBindings is called only by startup/config publication, never by an
// in-flight request's old Config snapshot. Migration preserves recency and does
// not cancel leases or change strict owner semantics.
func (p *Pool) ConfigureBindings(cfg *config.Config) {
	capacity := 0
	if cfg.Mistral.BindingCapacity != nil {
		capacity = *cfg.Mistral.BindingCapacity
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bindingConfig != nil && cfg.Revision < p.bindingConfig.Revision {
		return
	}
	p.bindingConfig = cfg
	if capacity == p.mistralCapacity {
		return
	}
	// Returning to the legacy budget may discard Mistral preferences, never
	// make its former independent entries evict another pool's strict owners.
	if p.mistralCapacity > 0 && capacity == 0 {
		for p.sharedLRU.Len()+p.mistralLRU.Len() > maxBindings && p.mistralLRU.Len() > 0 {
			p.deleteBindingLocked(p.mistralLRU.Back().Value.(*binding).key)
		}
	}
	p.mistralCapacity = capacity
	p.sharedLRU.Init()
	p.mistralLRU.Init()
	for el := p.lru.Front(); el != nil; el = el.Next() {
		b := el.Value.(*binding)
		b.budgetElement = p.budgetLocked(b).PushBack(b)
	}
	p.expireBindingsLocked(p.now(), 0)
	p.trimBindingsLocked()
}

// BindingCapacityFor reports either the Mistral independent ordinary-session
// allowance or the unchanged shared allowance. It is never per-account capacity.
func (p *Pool) BindingCapacityFor(provider string) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if provider == config.ProviderMistral && p.mistralCapacity > 0 {
		return p.mistralCapacity, true
	}
	return maxBindings, false
}

func (p *Pool) SharedBindingCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireBindingsLocked(p.now(), 0)
	return p.sharedLRU.Len()
}
