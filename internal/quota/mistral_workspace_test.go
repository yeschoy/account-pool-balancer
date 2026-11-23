package quota

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

// wsUpstream counts budget requests and answers as one workspace per key token.
type wsUpstream struct {
	calls  atomic.Int32
	whoami atomic.Int32
	budget atomic.Int32
	reset  string
	mu     sync.Mutex
	fail   bool
}

func (u *wsUpstream) start(t *testing.T) *httptest.Server {
	t.Helper()
	u.reset = time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		failing := u.fail
		u.mu.Unlock()
		if failing {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		u.calls.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/vibe/whoami"):
			u.whoami.Add(1)
			fmt.Fprint(w, `{"plan_type":"API","plan_name":"EDU","customer_id":"11111111-2222-3333-4444-555555555555"}`)
		case r.URL.Path == "/v1/billing/budget":
			u.budget.Add(1)
			fmt.Fprintf(w, `{"api_budget":{"initial_budget":30,"usage_percentage":25,"currency":"USD","reset_at":%q,"payg_enabled":false}}`, u.reset)
		case r.URL.Path == "/v1/billing/subscription":
			fmt.Fprint(w, `{"plan_name":"EDU","payg_enabled":false}`)
		default:
			fmt.Fprintf(w, `{"data":[{"id":"mistral-large-4-0","enabled":true}],"base_model_id":"mistral-large-4-0","currency":"USD","reset_at":%q}`, u.reset)
		}
	}))
	return srv
}

func wsConfig(t *testing.T, base string, ids ...string) *config.Config {
	cfg := config.Default()
	cfg.Mistral.BaseURL = base + "/v1"
	cfg.Accounts = nil
	for _, id := range ids {
		cfg.Accounts = append(cfg.Accounts, config.Account{ID: id, Provider: config.ProviderMistral, Group: config.MistralGroup, RuntimeKey: "mstrl_" + id, Enabled: true})
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Sibling keys of one workspace must not spend their own three requests on
// numbers another key just fetched; the read is shared for the TTL.
func TestMistralWorkspaceBudgetSharedWithinTTL(t *testing.T) {
	u := &wsUpstream{}
	srv := u.start(t)
	defer srv.Close()
	cfg := wsConfig(t, srv.URL, "k1", "k2")
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return srv.Client() })

	if err := p.RefreshNow(context.Background(), cfg.Accounts[0]); err != nil {
		t.Fatal(err)
	}
	firstWhoami, firstBudget := u.whoami.Load(), u.budget.Load()
	if err := p.RefreshNow(context.Background(), cfg.Accounts[1]); err != nil {
		t.Fatal(err)
	}
	if d := u.budget.Load() - firstBudget; d != 0 {
		t.Fatalf("sibling read the budget endpoint %d extra times, want 0", d)
	}
	if d := u.whoami.Load() - firstWhoami; d != 0 {
		t.Fatalf("sibling read whoami %d extra times, want 0", d)
	}
	b := p.Get("k2").Mistral
	if b == nil || b.Org != "11111111-2222-3333-4444-555555555555" || b.Budget == nil || b.Budget.Remaining != 22.5 {
		t.Fatalf("adopted state = %+v", b)
	}
	if src := p.Get("k2").Mistral.Source; src == "" {
		t.Fatal("adopted read lost its source")
	}
	// the adopting key writes its own snapshot; the owner's stays intact
	if p.Get("k1").Mistral == nil || p.Get("k1").Mistral.Budget.Total != 30 {
		t.Fatal("owner snapshot changed")
	}
}

// Without a sibling read, and after the TTL, a key reads for itself.
func TestMistralWorkspaceBudgetExpires(t *testing.T) {
	u := &wsUpstream{}
	srv := u.start(t)
	defer srv.Close()
	cfg := wsConfig(t, srv.URL, "k1", "k2")
	ttl := 1
	cfg.Mistral.WorkspaceQuotaTTLSec = &ttl
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return srv.Client() })
	if err := p.RefreshNow(context.Background(), cfg.Accounts[0]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	before := u.calls.Load()
	if err := p.RefreshNow(context.Background(), cfg.Accounts[1]); err != nil {
		t.Fatal(err)
	}
	if u.calls.Load() == before {
		t.Fatal("expired workspace read was still reused")
	}
}

// A failed read is never shared: the second key must really try.
func TestMistralWorkspaceBudgetFailureNotShared(t *testing.T) {
	u := &wsUpstream{}
	srv := u.start(t)
	defer srv.Close()
	cfg := wsConfig(t, srv.URL, "k1", "k2")
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return srv.Client() })
	u.mu.Lock()
	u.fail = true
	u.mu.Unlock()
	if err := p.RefreshNow(context.Background(), cfg.Accounts[0]); err == nil {
		t.Fatal("expected failure")
	}
	u.mu.Lock()
	u.fail = false
	u.mu.Unlock()
	if err := p.RefreshNow(context.Background(), cfg.Accounts[1]); err != nil {
		t.Fatal(err)
	}
	if p.Get("k2").Mistral == nil || p.Get("k2").Mistral.Budget == nil {
		t.Fatal("second key did not record its own read")
	}
}

// An older workspace record never overrides a read a key already holds.
func TestMistralWorkspaceBudgetNeverGoesBackwards(t *testing.T) {
	p := NewPoller(func() *config.Config { return config.Default() }, func(string) *http.Client { return http.DefaultClient })
	now := time.Now()
	stale := &MistralState{Org: "ws", KeyType: "API", Budget: &MistralBudget{Total: 1, Remaining: 1, Currency: "USD"}}
	newer := &MistralState{Org: "ws", KeyType: "API", Budget: &MistralBudget{Total: 30, Remaining: 22.5, Currency: "USD"}}
	p.snaps["k"] = &Snapshot{Mistral: &MistralState{Org: "ws", Source: "s", CheckedAt: now, Budget: newer.Budget}}
	p.wsQuota["ws"] = wsQuotaRead{state: stale, owner: "other", source: "s", at: now.Add(-time.Minute)}
	if got, ok := p.workspaceBudget("k", "ws", time.Hour, false); ok {
		t.Fatalf("reused an older read: %+v", got)
	}
}

// Concurrent due siblings must not each read the workspace.
func TestMistralWorkspaceBudgetCoalescesConcurrentReads(t *testing.T) {
	u := &wsUpstream{}
	srv := u.start(t)
	defer srv.Close()
	cfg := wsConfig(t, srv.URL, "k1", "k2", "k3", "k4")
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return srv.Client() })
	var wg sync.WaitGroup
	for _, a := range cfg.Accounts {
		wg.Add(1)
		go func(a config.Account) {
			defer wg.Done()
			_ = p.RefreshNow(context.Background(), a)
		}(a)
	}
	wg.Wait()
	// A genuine budget read is one whoami and one budget call; a second flight
	// is legitimate only after the first landed, and then adds nothing.
	if got := u.budget.Load(); got > 2 {
		t.Fatalf("budget endpoint read %d times for one workspace, want <= 2", got)
	}
	for _, id := range []string{"k1", "k2", "k3", "k4"} {
		if s := p.Get(id).Mistral; s == nil || s.Budget == nil {
			t.Fatalf("%s ended without a budget", id)
		}
	}
}
