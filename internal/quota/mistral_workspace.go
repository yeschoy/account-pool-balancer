package quota

import (
	"context"
	"time"

	"account-pool-balancer/internal/config"
)

// mistralBudgetFor returns this account's budget, reading the workspace only
// when the shared result cannot answer for it.
//
// A Mistral budget belongs to a workspace: every key in it sees the same
// api_budget/vibe_budget, and each read costs three upstream requests
// (whoami, budget, subscription). Reading once per key multiplies the request
// rate by the number of keys without learning anything new, and many keys per
// workspace is the normal case here. So the first key of a workspace reads,
// and its siblings adopt that result for mistral.workspaceQuotaTtlSec.
//
// The rules are deliberately conservative:
//   - a sibling only adopts a read performed by a *different* key that is newer
//     than its own last attempt, so a forced refresh never appears to reuse an
//     older value and an account's own interval is never shortened;
//   - a failed read is never shared: the error belongs to the key that got it,
//     and nothing marks the workspace as read;
//   - a manual refresh asks the same question of the same workspace, so it may
//     reuse a recent sibling read too, but never one older than this key's own
//     last attempt; the key that actually reads refreshes it for the others.
func (p *Poller) mistralBudgetFor(ctx context.Context, cfg *config.Config, a config.Account, source string) (*MistralState, error) {
	ttl := cfg.Mistral.WorkspaceQuotaTTL()
	ws := p.mistralWorkspace(a.ID, cfg, source)

	// Serialise per workspace. A key that does not know its workspace yet
	// serialises on its egress instead, so keys of one proxy+base never read
	// the same budget concurrently; after waiting it may learn the workspace.
	for waiting := true; waiting; {
		key := ws
		if key == "" {
			key = "egress:" + a.ProxyURL + "\x00" + cfg.RuntimeBase(a)
		}
		done, mine := p.wsFlight(key)
		if mine {
			defer p.wsLand(key)
			break
		}
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if ws = p.mistralWorkspace(a.ID, cfg, source); ws == "" {
			waiting = false // nobody reported a workspace yet: read for myself
		}
	}

	// A force (manual refresh) may reuse the workspace's recent read too: the
	// numbers are the workspace's, and the sibling that wrote them asked the
	// same question moments ago. What it does not do is reuse a value older
	// than this key's own last attempt, which workspaceBudget already refuses.
	if shared, ok := p.workspaceBudget(a.ID, ws, ttl, true); ok {
		shared.Source = source // the adopting key's own binding, not the reader's
		return shared, nil
	}
	m, err := FetchMistralBudget(ctx, p.HTTPFor(a.ProxyURL), cfg.RuntimeBase(a), a.RuntimeKey, a.ID)
	if err != nil || m.Org == "" {
		return m, err // an error or an unidentified workspace is this key's own
	}
	p.rememberWorkspaceBudget(a.ID, source, m, time.Now())
	return m, nil
}

// workspaceBudget returns a copy of a workspace read another key performed
// when that read is newer than this account's own last attempt. afterFlight
// means this caller waited for the workspace's flight to land, so the record
// it now sees was written by that read rather than being stale history.
func (p *Poller) workspaceBudget(id, ws string, ttl time.Duration, afterFlight bool) (*MistralState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ws == "" {
		return nil, false
	}
	r, ok := p.wsQuota[ws]
	if !ok || r.state == nil || r.owner == id || time.Since(r.at) >= ttl {
		return nil, false
	}
	// A key that never read a budget of its own has nothing to protect from an
	// older value; any recorded read of its workspace is new to it.
	var own *MistralState
	if s := p.snaps[id]; s != nil {
		own = s.Mistral
	}
	if own != nil && own.Source != "" && own.Source != r.source {
		return nil, false // this key's binding changed; let it read for itself
	}
	if own != nil && r.owner != id && !afterFlight && !r.at.After(own.CheckedAt) {
		return nil, false // nothing newer than what this key already holds
	}
	c := r.state.clone()
	c.Source, c.CheckedAt = r.source, time.Now()
	return c, true
}

// rememberWorkspaceBudget publishes a successful read for the workspace's
// other keys.
func (p *Poller) rememberWorkspaceBudget(id, source string, m *MistralState, at time.Time) {
	if m == nil || m.Org == "" {
		return
	}
	stored := m.clone()
	stored.Source = source
	p.mu.Lock()
	defer p.mu.Unlock()
	if prev, ok := p.wsQuota[m.Org]; ok && prev.state != nil && prev.at.After(at) {
		return // an older read never replaces a newer one
	}
	p.wsQuota[m.Org] = wsQuotaRead{state: stored, owner: id, source: source, at: at}
}

// mistralWorkspace resolves the workspace this account belongs to: its own
// snapshot, a record it performed, or — for a key that has never read one — the
// single workspace whose recorded read was made under the same binding.
func (p *Poller) mistralWorkspace(id string, cfg *config.Config, source string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ws := p.mistralWorkspaceLocked(id); ws != "" {
		return ws
	}
	// The binding source hashes the key itself, so it can never match across
	// accounts. What a sibling may inherit instead is the same endpoint and the
	// same egress: a key moved to another proxy or base URL does not adopt a
	// budget read through the old one.
	caller, ok := cfg.Account(id)
	if !ok {
		return ""
	}
	var found string
	for ws, r := range p.wsQuota {
		owner, ok := cfg.Account(r.owner)
		if !ok || owner.Provider != config.ProviderMistral || caller.Provider != config.ProviderMistral ||
			owner.ProxyURL != caller.ProxyURL || cfg.RuntimeBase(owner) != cfg.RuntimeBase(caller) {
			continue
		}
		if found != "" && found != ws {
			return "" // ambiguous: let this key identify itself
		}
		found = ws
	}
	return found
}

// mistralWorkspaceLocked resolves the workspace this account belongs to, from
// its own snapshot or from a record it performed. Caller holds mu.
func (p *Poller) mistralWorkspaceLocked(id string) string {
	if s := p.snaps[id]; s != nil && s.Mistral != nil && s.Mistral.Org != "" {
		return s.Mistral.Org
	}
	for ws, r := range p.wsQuota {
		if r.owner == id {
			return ws
		}
	}
	return ""
}

// wsFlight claims the read slot for key. mine reports whether the caller now
// owns it; otherwise done is closed when the current holder finishes.
func (p *Poller) wsFlight(key string) (done <-chan struct{}, mine bool) {
	p.wsMu.Lock()
	defer p.wsMu.Unlock()
	if cur, busy := p.wsBusy[key]; busy {
		return cur, false
	}
	d := make(chan struct{})
	p.wsBusy[key] = d
	return d, true
}

func (p *Poller) wsLand(key string) {
	p.wsMu.Lock()
	done := p.wsBusy[key]
	delete(p.wsBusy, key)
	p.wsMu.Unlock()
	if done != nil {
		close(done)
	}
}

// MistralWorkspace is the workspace an account's snapshot names, if any.
func MistralWorkspace(s *Snapshot) string {
	if s == nil || s.Mistral == nil {
		return ""
	}
	return s.Mistral.Org
}

// workspaceFreshFor reports whether this account already holds a budget for
// its workspace that a queued second pass in the same round can reuse
// without another request: either the workspace record is newer than the
// value the key held before this pass, or the key already carries that very
// record because it adopted it moments ago.
func (p *Poller) workspaceFreshFor(id string, cfg *config.Config, source string, heldCheckedAt, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	ws := p.mistralWorkspaceLocked(id)
	if ws == "" {
		return false
	}
	r, ok := p.wsQuota[ws]
	if !ok || r.state == nil || now.Sub(r.at) >= cfg.Mistral.WorkspaceQuotaTTL() {
		return false
	}
	// The record's source belongs to whichever key performed the read, so it
	// is not compared with this caller's; the workspace and the TTL already
	// bound what may be reused.
	if r.at.After(heldCheckedAt) {
		return true // the record is newer than what this key had before
	}
	// The key already carries this very record: same workspace, same amounts and
	// a read at or after the record, so re-reading would return the same numbers.
	// The amounts must match: a newer timestamp on a *different* reading (another
	// key of the workspace, or a value adopted before a real read) is not this
	// record, and must not suppress a read of this key's own.
	s := p.snaps[id]
	return s != nil && s.Mistral != nil && s.Mistral.Budget != nil && r.state.Budget != nil &&
		s.Mistral.Org == r.state.Org && !s.Mistral.CheckedAt.Before(r.at) &&
		sameMistralBudget(s.Mistral.Budget, r.state.Budget)
}

// sameMistralBudget reports whether two readings are the same budget state.
func sameMistralBudget(a, b *MistralBudget) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Total == b.Total && a.Remaining == b.Remaining && a.UsedPercent == b.UsedPercent &&
		a.Currency == b.Currency && a.ResetsAt.Equal(b.ResetsAt)
}
