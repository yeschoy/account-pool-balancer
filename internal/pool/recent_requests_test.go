package pool

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/quota"
)

func recentTestClock() time.Time { return time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC) }

func recentGoEligibilityFixture(t *testing.T) (*Pool, *config.Config, map[string]*quota.Snapshot) {
	t.Helper()
	now := recentTestClock()
	cfg := config.Default()
	cfg.OpenCode.Enabled = true
	cfg.Accounts = []config.Account{
		{ID: "low", Provider: config.ProviderOpenCodeGo, RuntimeKey: "fake-go-low", Enabled: true},
		{ID: "high", Provider: config.ProviderOpenCodeGo, RuntimeKey: "fake-go-high", Enabled: true},
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	cfg.Routing.Strategy = "least-requests-1h"
	cfg.Routing.HealthFilter = false // this strategy must enforce health even with the optional switch off
	cfg.Routing.MinimumHealth = 0.5
	snaps := map[string]*quota.Snapshot{}
	for _, a := range cfg.Accounts {
		usage := opencode.Usage{}
		for _, name := range []string{"rolling", "weekly", "monthly"} {
			usage[name] = opencode.Window{Status: "ok", UsedPercent: 10, RemainingPercent: 90, ResetsAt: now.Add(time.Hour)}
		}
		snaps[a.ID] = &quota.Snapshot{Catalog: map[string]bool{"m": true}, CatalogAt: now,
			OpenCode: &opencode.State{Source: quota.OpenCodeSource(cfg, a), UsageAt: now, Usage: usage}}
	}
	p := New(func(id string) *quota.Snapshot { return snaps[id] })
	p.SetClock(func() time.Time { return now })
	p.counters.Accounts["high"] = &Counter{RecentRequests: []RequestMinute{{Minute: now.Unix() / 60, Requests: 100}}}
	for range MinHealthSamples {
		p.Sample("high", true)
	}
	return p, cfg, snaps
}

func TestRecentStrategyFiltersBeforeRankingAndAffinity(t *testing.T) {
	for _, tc := range []struct{ name, why, window string }{
		{name: "disabled", why: "disabled"}, {name: "quarantined", why: "quarantined"},
		{name: "cooldown", why: "cooldown"}, {name: "model-cooldown", why: "model-cooldown"},
		{name: "policy-paused", why: "policy-paused"}, {name: "not-entitled", why: "not-entitled"},
		{name: "unhealthy", why: "health"},
		{name: "rolling-empty", why: "quota-exhausted", window: "rolling"},
		{name: "weekly-empty", why: "quota-exhausted", window: "weekly"},
		{name: "monthly-empty", why: "quota-exhausted", window: "monthly"},
	} {
		for _, affinity := range []string{"fresh", "session", "cache", "stateful"} {
			t.Run(tc.name+"/"+affinity, func(t *testing.T) {
				p, cfg, snaps := recentGoEligibilityFixture(t)
				now := recentTestClock()
				req := Request{Group: config.ProviderOpenCodeGo, Model: "m"}
				switch affinity {
				case "session", "stateful":
					p.Bind(cfg, "session", "low")
					req.Affinity, req.Explicit = "session", true
					req.RequireBinding = affinity == "stateful"
				case "cache":
					p.RememberCache(cfg, "cache", "low", "m")
					req.CacheAffinity = "cache"
				}
				switch tc.name {
				case "disabled":
					cfg.Accounts[0].Enabled = false
				case "quarantined":
					p.Quarantine("low", 401, "test", "test")
				case "cooldown":
					p.Cooldown("low", "", now.Add(time.Minute), 429, "test", "test")
				case "model-cooldown":
					p.Cooldown("low", "m", now.Add(time.Minute), 429, "test", "test")
				case "policy-paused":
					p.Cooldown("low", "", now.Add(time.Minute), 403, "test", "opencode-go-policy-refusal")
				case "not-entitled":
					delete(snaps["low"].Catalog, "m")
				case "unhealthy":
					for range MinHealthSamples {
						p.Sample("low", false)
					}
				default:
					w := snaps["low"].OpenCode.Usage[tc.window]
					w.UsedPercent, w.RemainingPercent = 100, 0
					snaps["low"].OpenCode.Usage[tc.window] = w
				}
				order, excluded := p.Preview(cfg, req.Group, req.Model)
				if len(order) != 1 || order[0] != "high" || excluded["low"] != tc.why {
					t.Fatalf("ineligible low-count account in preview: %v %v", order, excluded)
				}
				l, err := p.Acquire(context.Background(), cfg, req)
				mustBlock := req.RequireBinding || tc.name == "policy-paused" && affinity == "session"
				if mustBlock {
					if l != nil {
						l.Release()
						t.Fatal("stateful/policy-bound session escaped its unavailable owner")
					}
					if err == nil {
						t.Fatal("unavailable bound account did not block")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				defer l.Release()
				if l.Account.ID != "high" || l.Decision.Candidates != 1 {
					t.Fatalf("low recent count or affinity bypassed eligibility: %+v", l.Decision)
				}
			})
		}
	}
}

func TestRecentStrategyNoHealthyFundedAccountDoesNotFailOpen(t *testing.T) {
	for _, reason := range []string{"health", "quota"} {
		t.Run(reason, func(t *testing.T) {
			p, cfg, snaps := recentGoEligibilityFixture(t)
			for _, a := range cfg.Accounts {
				if reason == "quota" {
					w := snaps[a.ID].OpenCode.Usage["rolling"]
					w.Status = "rate-limited"
					snaps[a.ID].OpenCode.Usage["rolling"] = w
				} else {
					delete(p.health, a.ID)
					for range MinHealthSamples {
						p.Sample(a.ID, false)
					}
				}
			}
			l, err := p.Acquire(context.Background(), cfg, Request{Group: config.ProviderOpenCodeGo, Model: "m"})
			if l != nil {
				l.Release()
				t.Fatal("all ineligible accounts fell back to least-used")
			}
			var unavailable *UnavailableError
			if !errors.As(err, &unavailable) {
				t.Fatalf("want unavailable, got %v", err)
			}
		})
	}
}

func TestRecentMandatoryHealthDoesNotChangeOtherStrategiesOrUnknowns(t *testing.T) {
	p, cfg, snaps := recentGoEligibilityFixture(t)
	for range MinHealthSamples {
		p.Sample("low", false)
	}
	cfg.Routing.Strategy = "least-used"
	cfg.Accounts = cfg.Accounts[:1] // only the low-health account remains
	l := acquire(t, p, cfg, Request{Group: config.ProviderOpenCodeGo, Model: "m"})
	l.Release() // other strategies still honor the disabled health-filter switch
	cfg.Routing.HealthFilter = true
	if l, err := p.Acquire(context.Background(), cfg, Request{Group: config.ProviderOpenCodeGo, Model: "m"}); err == nil {
		l.Release()
		t.Fatal("existing opt-in health filtering stopped working")
	}
	cfg.Routing.HealthFilter = false
	cfg.Routing.Strategy = "least-requests-1h"
	delete(p.health, "low")                     // fewer than 20 samples is unknown, not proven unhealthy
	snaps["low"].OpenCode.UsageAt = time.Time{} // unknown is not measured exhaustion
	l = acquire(t, p, cfg, Request{Group: config.ProviderOpenCodeGo, Model: "m"})
	if l.Account.ID != "low" {
		l.Release()
		t.Fatal("unknown health/quota silently reclassified as unhealthy/exhausted")
	}
	l.Release()
	for i := range MinHealthSamples {
		p.Sample("low", i%2 == 0)
	}
	l = acquire(t, p, cfg, Request{Group: config.ProviderOpenCodeGo, Model: "m"})
	l.Release() // exactly at the configured 0.5 threshold is allowed
	cfg.Routing.MinimumHealth = 0.6
	if l, err := p.Acquire(context.Background(), cfg, Request{Group: config.ProviderOpenCodeGo, Model: "m"}); err == nil {
		l.Release()
		t.Fatal("recent strategy did not honor the configured health threshold")
	}
}

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
