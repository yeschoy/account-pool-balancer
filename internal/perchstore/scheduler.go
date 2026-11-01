package perchstore

import (
	"context"
	"sync"
	"time"
)

// Scheduler keeps every bound session alive: once a tick it looks at the
// sessions that are due and renews them with bounded concurrency. It is the
// piece that makes "import once, keep working" true after a restart too.
type Scheduler struct {
	k *Keeper
	// Tick is how often due sessions are looked for.
	Tick time.Duration
	// Concurrency bounds how many accounts renew at once.
	Concurrency int
	// OnResult is told about each attempt (nil is fine).
	OnResult func(accountID string, res RefreshResult)

	wake chan struct{}
	mu   sync.Mutex
	// paused is set when a batch refresh is running, so the loop does not
	// pick up the same accounts twice.
	paused bool
}

// NewScheduler returns a scheduler over k.
func NewScheduler(k *Keeper) *Scheduler {
	return &Scheduler{k: k, Tick: 30 * time.Second, Concurrency: 4, wake: make(chan struct{}, 1)}
}

// Wake asks the loop to look for due sessions now.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run renews due sessions until ctx is done.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.wake:
		}
		s.RefreshDue(ctx)
	}
}

// RefreshDue renews every session that is due, at most Concurrency at a time.
// It returns how many were attempted.
func (s *Scheduler) RefreshDue(ctx context.Context) int {
	s.mu.Lock()
	if s.paused {
		s.mu.Unlock()
		return 0
	}
	s.mu.Unlock()
	due := s.k.DueAccounts()
	if len(due) == 0 {
		return 0
	}
	s.RefreshBlocks(ctx, due)
	return len(due)
}

// RefreshBlocks renews the named accounts now (whether or not they are due),
// with bounded concurrency. A failure of one account neither stops the others
// nor hides their results.
func (s *Scheduler) RefreshBlocks(ctx context.Context, accountIDs []string) {
	limit := max(s.Concurrency, 1)
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, id := range accountIDs {
		id := id
		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			continue
		}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res := s.k.Refresh(ctx, id, false)
			if s.OnResult != nil {
				s.OnResult(id, res)
			}
		}()
	}
	wg.Wait()
}

// Pause stops the loop from starting new work (a batch task holds it).
func (s *Scheduler) Pause() { s.mu.Lock(); s.paused = true; s.mu.Unlock() }

// Resume lets the loop work again.
func (s *Scheduler) Resume() { s.mu.Lock(); s.paused = false; s.mu.Unlock() }

// DueAccounts lists the account ids whose sessions want renewing.
func (k *Keeper) DueAccounts() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	cfg := k.cfg()
	now := time.Now()
	out := make([]string, 0, len(k.byAcc))
	for accID, uid := range k.byAcc {
		sec, ok := k.recs[uid]
		if !ok || sec.RefreshToken == "" {
			continue
		}
		if sec.session().Due(now, cfg.RefreshMargin) {
			out = append(out, accID)
		}
	}
	return out
}

// ExpiringWithin lists accounts whose session ends within d, whether or not
// they are due yet — the console's "expiring soon" count.
func (k *Keeper) ExpiringWithin(d time.Duration) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	now := time.Now()
	out := []string{}
	for accID, uid := range k.byAcc {
		sec, ok := k.recs[uid]
		if !ok {
			continue
		}
		exp, ok := sec.session().Expires()
		if ok && exp.Before(now.Add(d)) {
			out = append(out, accID)
		}
	}
	return out
}

// Stats is what the console's dashboard reads about sessions.
type Stats struct {
	// Bound accounts with a session.
	Bound int `json:"bound"`
	// WithRefreshToken: renewals are possible at all.
	WithRefreshToken int `json:"withRefreshToken"`
	// Due: due for renewal right now.
	Due int `json:"due"`
	// ExpiringSoon: ends within an hour (the scheduler's window).
	ExpiringSoon int `json:"expiringSoon"`
	// Stored: sessions on disk (one per Supabase user id).
	Stored int `json:"stored"`
	// Users: the same count as Stored, named for the dashboard.
	Users int `json:"users"`
}

// Stats summarises the store.
func (k *Keeper) Stats() Stats {
	k.mu.Lock()
	defer k.mu.Unlock()
	cfg := k.cfg()
	now := time.Now()
	out := Stats{Stored: len(k.recs), Users: len(k.recs)}
	for _, uid := range k.byAcc {
		sec, ok := k.recs[uid]
		if !ok {
			continue
		}
		out.Bound++
		if sec.RefreshToken != "" {
			out.WithRefreshToken++
		}
		if sec.session().Due(now, cfg.RefreshMargin) {
			out.Due++
		}
		if exp, ok := sec.session().Expires(); ok && exp.Before(now.Add(time.Hour)) {
			out.ExpiringSoon++
		}
	}
	return out
}
