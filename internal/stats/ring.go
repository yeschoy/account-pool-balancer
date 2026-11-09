package stats

import "sort"

// Slots are allocated only for observed minutes, not 1440 buckets on the first
// request of every account. Totals make per-account summaries O(accounts), not
// O(accounts*1440). All mutation stays under Stats.mu.
type ring struct {
	slots  map[int64]*Bucket
	total  Bucket
	pruned int64
}

func (b *Bucket) subtract(o Bucket) {
	b.Requests -= o.Requests
	b.Success -= o.Success
	b.Failed -= o.Failed
	b.Cancelled -= o.Cancelled
	b.Input -= o.Input
	b.Output -= o.Output
	b.Cached -= o.Cached
	b.CacheCreation -= o.CacheCreation
	b.Prompt -= o.Prompt
	b.CachePrompt -= o.CachePrompt
	b.CacheCached -= o.CacheCached
	b.CacheKnownRequests -= o.CacheKnownRequests
	b.CacheWrite -= o.CacheWrite
	b.CacheWriteKnownRequests -= o.CacheWriteKnownRequests
	b.InputKnownRequests -= o.InputKnownRequests
	b.OutputKnownRequests -= o.OutputKnownRequests
	b.DurationCount -= o.DurationCount
	b.BindingHits -= o.BindingHits
	b.AffinityRequests -= o.AffinityRequests
	b.CacheAffinityHits -= o.CacheAffinityHits
	b.DurationMs -= o.DurationMs
	b.TTFTMs -= o.TTFTMs
	b.TTFTCount -= o.TTFTCount
	b.CostCNY -= o.CostCNY
	b.CostUSD -= o.CostUSD
}
func (r *ring) prune(cut int64) {
	if r.pruned == cut {
		return
	}
	for k, b := range r.slots {
		if b.Minute <= cut {
			r.total.subtract(*b)
			delete(r.slots, k)
		}
	}
	r.pruned = cut
	if len(r.slots) == 0 {
		r.total = Bucket{}
	}
}
func (r *ring) slot(m int64) *Bucket {
	r.prune(m - 1440)
	if r.slots == nil {
		r.slots = make(map[int64]*Bucket)
	}
	k := m % 1440
	b := r.slots[k]
	if b == nil || b.Minute != m {
		if b != nil {
			r.total.subtract(*b)
		}
		b = &Bucket{Minute: m}
		r.slots[k] = b
	}
	return b
}
func (r *ring) record(m int64, d Bucket) { r.slot(m).add(d); r.total.add(d) }
func (r *ring) set(b Bucket) {
	if r.slots == nil {
		r.slots = map[int64]*Bucket{}
	}
	k := b.Minute % 1440
	if old := r.slots[k]; old != nil {
		r.total.subtract(*old)
	}
	r.slots[k] = &b
	r.total.add(b)
	r.pruned = 0
}
func (r *ring) pick(cut int64) []Bucket {
	r.prune(cut)
	var keys []int64
	for k := range r.slots {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var out []Bucket
	for _, k := range keys {
		b := r.slots[k]
		if b.Requests > 0 {
			out = append(out, *b)
		}
	}
	return out
}
func sameRing(a, b *ring) bool {
	if a == nil || b == nil || len(a.slots) != len(b.slots) {
		return false
	}
	for k, v := range a.slots {
		if x := b.slots[k]; x == nil || *v != *x {
			return false
		}
	}
	return true
}
func cloneRing(r *ring) *ring {
	out := &ring{}
	for _, b := range r.slots {
		out.set(*b)
	}
	return out
}
