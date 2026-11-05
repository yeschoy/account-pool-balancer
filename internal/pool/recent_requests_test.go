package pool

import (
	"context"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func recentTestClock() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) }

func TestRecentRequestsIncludeStickyHitsAndExcludeUnsent(t *testing.T) {
	p := New(nil)
	now := recentTestClock()
	p.SetClock(func() time.Time { return now })
	cfg := testConfig(config.Account{ID: "a"}, config.Account{ID: "b"})
	cfg.Routing.Strategy = "least-requests-1h"
	var owner string
	for range 4 {
		l := acquire(t, p, cfg, Request{Affinity: "conversation", Explicit: true})
		if owner == "" {
			owner = l.Account.ID
		} else if l.Account.ID != owner {
			t.Fatal("recent usage moved an existing session")
		}
		l.MarkSent()
		l.MarkSent()
		l.Release()
		l.MarkSent()
	}
	v := p.Views(cfg)[owner]
	if v.Selections != 1 || v.RecentRequests1H != 4 || v.PendingRequests != 0 {
		t.Fatalf("sticky sent accounting: %+v", v)
	}
	l := acquire(t, p, cfg, Request{})
	if l.Account.ID == owner {
		t.Fatal("unbound request ignored recent sent usage")
	}
	other := l.Account.ID
	if p.Views(cfg)[other].PendingRequests != 1 {
		t.Fatal("unlimited-RPM lease missing pending reservation")
	}
	l.Release()
	l.Release()
	l.MarkSent()
	if v := p.Views(cfg)[other]; v.RecentRequests1H != 0 || v.PendingRequests != 0 {
		t.Fatalf("unsent request was counted: %+v", v)
	}
	now = now.Add(59*time.Minute + 59*time.Second)
	if p.Views(cfg)[owner].RecentRequests1H != 4 {
		t.Fatal("recent minute expired too early")
	}
	now = recentTestClock().Add(time.Hour)
	if p.Views(cfg)[owner].RecentRequests1H != 0 {
		t.Fatal("old minute remained in rolling hour")
	}
}

func TestRecentPendingReservationsBalanceConcurrentNewRequests(t *testing.T) {
	p := New(nil)
	now := recentTestClock()
	p.SetClock(func() time.Time { return now })
	cfg := testConfig(config.Account{ID: "a"}, config.Account{ID: "b"})
	cfg.Routing.Strategy = "least-requests-1h"
	p.counters.Accounts["a"] = &Counter{RecentRequests: []RequestMinute{{Minute: now.Unix() / 60, Requests: 3}}}
	p.counters.Accounts["b"] = &Counter{RecentRequests: []RequestMinute{{Minute: now.Unix() / 60, Requests: 4}}}
	const n = 80
	type result struct {
		lease *Lease
		err   error
	}
	out := make(chan result, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, err := p.Acquire(context.Background(), cfg, Request{Group: "default"})
			out <- result{l, err}
		}()
	}
	wg.Wait()
	close(out)
	var leases []*Lease
	for r := range out {
		if r.err != nil {
			t.Fatal(r.err)
		}
		leases = append(leases, r.lease)
	}
	views := p.Views(cfg)
	a, b := views["a"], views["b"]
	x, y := a.RecentRequests1H+int64(a.PendingRequests), b.RecentRequests1H+int64(b.PendingRequests)
	if x-y > 1 || y-x > 1 || a.PendingRequests+b.PendingRequests != n {
		t.Fatalf("pending herd: %+v %+v", a, b)
	}
	for _, l := range leases {
		wg.Add(1)
		go func(l *Lease) { defer wg.Done(); l.MarkSent(); l.MarkSent(); l.Release() }(l)
	}
	wg.Wait()
	views = p.Views(cfg)
	a, b = views["a"], views["b"]
	if a.RecentRequests1H+b.RecentRequests1H != n+7 || a.PendingRequests+b.PendingRequests != 0 || a.Active+b.Active != 0 {
		t.Fatal("concurrent commit/release lost requests or leaked reservations")
	}
}

func TestRecentRequestCountersPersistenceValidationAndCopies(t *testing.T) {
	p := New(nil)
	now := recentTestClock()
	p.SetClock(func() time.Time { return now })
	cfg := testConfig(config.Account{ID: "a"})
	l := acquire(t, p, cfg, Request{})
	l.MarkSent()
	l.Release()
	pending := acquire(t, p, cfg, Request{})
	defer pending.Release()
	original, _ := p.ExportCounters(true)
	if original.Accounts["a"].pending != 0 {
		t.Fatal("ephemeral pending count exported")
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored Counters
	if err = json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	p2 := New(nil)
	p2.SetClock(func() time.Time { return now })
	if err = p2.ImportCounters(restored, map[string]bool{"a": true}); err != nil {
		t.Fatal(err)
	}
	restored.Accounts["a"].RecentRequests[0].Requests = 99
	original.Accounts["a"].RecentRequests[0].Requests = 88
	if p.Views(cfg)["a"].RecentRequests1H != 1 || p2.Views(cfg)["a"].RecentRequests1H != 1 || p2.Views(cfg)["a"].PendingRequests != 0 {
		t.Fatal("counter history aliases snapshots or restores reservations")
	}
	before, _ := p2.ExportCounters(true)
	for name, slots := range map[string][]RequestMinute{
		"negative-minute": {{Minute: -1, Requests: 1}}, "zero": {{Minute: 1}}, "negative-count": {{Minute: 1, Requests: -1}},
		"duplicate": {{Minute: 1, Requests: 1}, {Minute: 1, Requests: 2}}, "unordered": {{Minute: 2, Requests: 1}, {Minute: 1, Requests: 1}},
		"overflow": {{Minute: 1, Requests: math.MaxInt64}, {Minute: 2, Requests: 1}}, "unbounded": make([]RequestMinute, 61),
	} {
		t.Run(name, func(t *testing.T) {
			bad := Counters{Version: 1, Accounts: map[string]*Counter{"a": {RecentRequests: slots}}}
			if p2.ImportCounters(bad, map[string]bool{"a": true}) == nil {
				t.Fatal("invalid recent history accepted")
			}
		})
	}
	if p2.Views(cfg)["a"].RecentRequests1H != 1 {
		t.Fatal("invalid import changed live counters")
	}
	legacy := Counters{Version: 1, Sequence: 7, Accounts: map[string]*Counter{"a": {Count: 7, LastSelected: 7}}}
	if err = p2.ImportCounters(legacy, map[string]bool{"a": true}); err != nil {
		t.Fatal(err)
	}
	if v := p2.Views(cfg)["a"]; v.Selections != 7 || v.RecentRequests1H != 0 {
		t.Fatal("old snapshots not compatible")
	}
	now = now.Add(time.Hour)
	if err = p2.ImportCounters(before, map[string]bool{"a": true}); err != nil {
		t.Fatal(err)
	}
	if v := p2.Views(cfg)["a"]; v.RecentRequests1H != 0 {
		t.Fatal("restart revived expired recent history")
	}
}

func TestRecentHistoryBoundedPreviewPureAndLateCallbacks(t *testing.T) {
	now := recentTestClock()
	c := &Counter{}
	for i := 0; i < 180; i++ {
		c.recordRequest(now.Add(time.Duration(i) * time.Minute))
		if len(c.RecentRequests) > 60 {
			t.Fatal("unbounded hourly history")
		}
	}
	if c.recentCount(now.Add(179*time.Minute)) != 60 {
		t.Fatal("rolling minute count wrong")
	}
	if c.recentCount(now) != 0 {
		t.Fatal("future buckets counted after clock rollback")
	}
	p := New(nil)
	p.SetClock(func() time.Time { return now })
	cfg := testConfig(config.Account{ID: "a"})
	cfg.Routing.Strategy = "least-requests-1h"
	l := acquire(t, p, cfg, Request{})
	seq := p.counters.Sequence
	p.Preview(cfg, "default", "")
	p.Preview(cfg, "default", "")
	if p.counters.Sequence != seq || p.Views(cfg)["a"].RecentRequests1H != 0 {
		t.Fatal("preview mutated recent accounting")
	}
	p.ResetCounts()
	l.MarkSent()
	l.Release()
	if len(p.counters.Accounts) != 0 {
		t.Fatal("old lease revived counters after reset")
	}
	l = acquire(t, p, cfg, Request{})
	p.Invalidate("a", true)
	l.MarkSent()
	l.Release()
	if len(p.counters.Accounts) != 0 {
		t.Fatal("old lease revived deleted account counters")
	}
}
