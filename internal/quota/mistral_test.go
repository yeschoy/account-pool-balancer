package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestMistralCatalogAndBudgetRefreshAndSource(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/vibe/whoami" {
			fmt.Fprint(w, `{"plan_type":"API","plan_name":"FREE"}`)
			return
		}
		if r.URL.Path == "/v1/billing/budget" {
			fmt.Fprintf(w, `{"api_budget":{"initial_budget":30,"usage_percentage":25,"currency":"USD","reset_at":%q,"payg_enabled":false}}`, time.Now().Add(24*time.Hour).Format(time.RFC3339))
			return
		}
		if r.URL.Path == "/v1/billing/subscription" {
			fmt.Fprint(w, `{"plan_name":"INDIVIDUAL","payg_enabled":false}`)
			return
		}
		calls.Add(1)
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer mstrl_mock_secret" {
			t.Error("wrong metadata target/auth")
		}
		w.WriteHeader(int(code.Load()))
		w.Write([]byte(`{"data":[{"id":"ft:custom","base_model_id":"mistral-base"},{"id":"disabled","enabled":false}]}`))
	}))
	defer up.Close()
	cfg := config.Default()
	cfg.Mistral.BaseURL = up.URL + "/v1"
	cfg.Accounts = []config.Account{{ID: "m", RuntimeKey: "mstrl_mock_secret", Enabled: true}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return up.Client() })
	a := cfg.Accounts[0]
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	good := p.Get(a.ID)
	if !good.Catalog["ft:custom"] || good.Catalog["disabled"] || len(good.Aliases) != 0 || len(good.Bases) != 0 || good.CatalogSource == "" || !good.PlanAt.IsZero() || good.WalletUSD != "" {
		t.Fatal("native IDs or unknown funding lost")
	}
	if good.Mistral == nil || good.Mistral.Budget.Remaining != 22.5 || !good.FreshMistral(time.Now(), time.Hour) {
		t.Fatal("budget not refreshed")
	}
	if err := p.fetch(context.Background(), cfg, a); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("alias-free catalog fetched before TTL")
	}
	for _, st := range []int32{503, 401} {
		code.Store(st)
		if p.RefreshNow(context.Background(), a) == nil {
			t.Fatal("failed refresh reported success")
		}
		q := p.Get(a.ID)
		if !q.CatalogAt.Equal(good.CatalogAt) || !q.Catalog["ft:custom"] || q.LastError == "" || q.AuthFailed != (st == 401) {
			t.Fatal("last good data destroyed or failure unmarked")
		}
	}
	code.Store(200)
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if p.Get(a.ID).AuthFailed {
		t.Fatal("successful refresh did not recover auth")
	}
	old := p.Get(a.ID)
	cfg.Accounts[0].RuntimeKey = "mstrl_replaced_secret"
	p.Forget(a.ID)
	p.store(a.ID, old)
	if p.Get(a.ID) != nil {
		t.Fatal("late source write resurrected catalog")
	}
	if catalogDue(good, time.Now(), true, false) {
		t.Fatal("native IDs incorrectly require alias migration")
	}
}

func TestMistralBudgetTypesAndValidation(t *testing.T) {
	future := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	valid := fmt.Sprintf(`{"api_budget":{"initial_budget":30,"usage_percentage":25,"currency":"USD","reset_at":%q,"payg_enabled":false},"vibe_budget":{"initial_budget":300,"usage_percentage":10,"currency":"EUR","reset_at":%q,"payg_enabled":true}}`, future, future)
	for _, tc := range []struct {
		name, kind, body string
		status           int
		want             float64
		fail             bool
	}{
		{name: "studio", kind: "API", body: valid, want: 22.5},
		{name: "vibe", kind: "CHAT", body: valid, want: 270},
		{name: "zero measured", kind: "API", body: strings.Replace(valid, `"initial_budget":30,`, `"initial_budget":0,`, 1)},
		{name: "overage", kind: "API", body: strings.Replace(valid, `"usage_percentage":25`, `"usage_percentage":110`, 1)},
		{name: "missing percentage", kind: "API", body: strings.Replace(valid, `"usage_percentage":25,`, "", 1), fail: true},
		{name: "missing total", kind: "API", body: strings.Replace(valid, `"initial_budget":30,`, "", 1), fail: true},
		{name: "wrong budget", kind: "CHAT", body: `{"api_budget":{}}`, fail: true},
		{name: "negative", kind: "API", body: strings.Replace(valid, `"usage_percentage":25`, `"usage_percentage":-1`, 1), fail: true},
		{name: "missing currency", kind: "API", body: strings.Replace(valid, `"currency":"USD",`, "", 1), fail: true},
		{name: "expired", kind: "API", body: strings.ReplaceAll(valid, future, "2000-01-01T00:00:00Z"), fail: true},
		{name: "missing type", body: valid, fail: true},
		{name: "unknown type", kind: "MISTRAL_CODE", body: valid, fail: true},
		{name: "permission", kind: "API", body: `{"error":"private-secret-body"}`, status: 403, fail: true},
		{name: "invalid JSON", kind: "API", body: `{"secret":`, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer mock-key" {
					t.Error("wrong budget auth")
				}
				switch r.URL.Path {
				case "/api/vibe/whoami":
					fmt.Fprintf(w, `{"plan_type":%q,"plan_name":"INDIVIDUAL","email":"private@example.invalid","domains":{"api":"https://untrusted.invalid"}}`, tc.kind)
				case "/v1/billing/subscription":
					fmt.Fprint(w, `{"plan_name":"INDIVIDUAL","payg_enabled":false}`)
				case "/v1/billing/budget":
					if tc.status != 0 {
						w.WriteHeader(tc.status)
					}
					fmt.Fprint(w, tc.body)
				default:
					t.Errorf("unexpected metadata path %s", r.URL.Path)
				}
			}))
			defer up.Close()
			m, err := FetchMistralBudget(context.Background(), up.Client(), up.URL+"/v1", "mock-key", "m")
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected budget outcome: %v", err)
			}
			if err != nil {
				if strings.Contains(err.Error(), "private-secret-body") {
					t.Fatal("upstream body exposed")
				}
				return
			}
			if m.KeyType != tc.kind || m.Budget.Remaining != tc.want {
				t.Fatalf("wrong selected budget: %+v", m)
			}
			b, _ := json.Marshal(m)
			if strings.Contains(string(b), "private@example") || strings.Contains(string(b), "untrusted.invalid") {
				t.Fatal("identity persisted")
			}
		})
	}
}

func TestMistralBudgetFailurePreservesValueAndMetadata(t *testing.T) {
	var denied atomic.Bool
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
		case "/api/vibe/whoami":
			fmt.Fprint(w, `{"plan_type":"CHAT","plan_name":"INDIVIDUAL"}`)
		case "/v1/billing/budget":
			if denied.Load() {
				w.WriteHeader(403)
				fmt.Fprint(w, `{"error":"private-secret-body"}`)
				return
			}
			fmt.Fprintf(w, `{"vibe_budget":{"initial_budget":300,"usage_percentage":10,"currency":"USD","reset_at":%q}}`, time.Now().Add(24*time.Hour).Format(time.RFC3339))
		}
	}))
	defer up.Close()
	cfg := config.Default()
	cfg.Mistral.BaseURL = up.URL + "/v1"
	cfg.Accounts = []config.Account{{ID: "m", RuntimeKey: "mstrl_mock", Enabled: true}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return up.Client() })
	a := cfg.Accounts[0]
	if err := p.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	good := p.Get(a.ID)
	denied.Store(true)
	if err := p.RefreshNow(context.Background(), a); err == nil {
		t.Fatal("denied budget reported success")
	}
	failed := p.Get(a.ID)
	if failed.AuthFailed || failed.Mistral.Error == "" || failed.Mistral.Budget.Remaining != 270 || !failed.Mistral.At.Equal(good.Mistral.At) || failed.FreshMistral(time.Now(), time.Hour) {
		t.Fatal("permission failure destroyed data or invalidated key")
	}
	failed.Mistral.Budget.Remaining = 1
	if p.Get(a.ID).Mistral.Budget.Remaining != 270 {
		t.Fatal("snapshot clone aliases budget")
	}
	raw, err := json.Marshal(p.All())
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]*Snapshot
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	p2 := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return up.Client() })
	p2.Load(saved)
	if p2.Get(a.ID).Mistral.Budget.Remaining != 270 || p2.Get(a.ID).Mistral.Error == "" {
		t.Fatal("metadata round trip lost budget/error")
	}
	denied.Store(false)
	if err := p.RefreshNow(context.Background(), a); err != nil || !p.Get(a.ID).FreshMistral(time.Now(), time.Hour) {
		t.Fatal("budget did not recover", err)
	}
}

func TestMistralBudgetLateSourceWriteDiscarded(t *testing.T) {
	for _, change := range []string{"key", "base", "proxy"} {
		t.Run(change, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models":
					fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
				case "/api/vibe/whoami":
					fmt.Fprint(w, `{"plan_type":"API"}`)
				case "/v1/billing/budget":
					close(started)
					<-release
					fmt.Fprintf(w, `{"api_budget":{"initial_budget":30,"usage_percentage":25,"currency":"USD","reset_at":%q}}`, time.Now().Add(time.Hour).Format(time.RFC3339))
				}
			}))
			defer up.Close()
			cfg := config.Default()
			cfg.Mistral.BaseURL = up.URL + "/v1"
			cfg.Accounts = []config.Account{{ID: "m", RuntimeKey: "mstrl_mock", Enabled: true}}
			if err := cfg.Normalize(); err != nil {
				t.Fatal(err)
			}
			var current atomic.Pointer[config.Config]
			current.Store(cfg)
			p := NewPoller(current.Load, func(string) *http.Client { return up.Client() })
			done := make(chan error, 1)
			go func() { done <- p.RefreshNow(context.Background(), cfg.Accounts[0]) }()
			<-started
			next := cfg.Clone()
			switch change {
			case "key":
				next.Accounts[0].RuntimeKey = "mstrl_other_mock"
			case "base":
				next.Mistral.BaseURL = up.URL + "/new/v1"
			case "proxy":
				next.Accounts[0].ProxyURL = "http://127.0.0.1:1"
			}
			current.Store(next)
			p.Forget("m")
			close(release)
			<-done
			if p.Get("m") != nil {
				t.Fatal("late budget resurrected after source changed")
			}
		})
	}
}

func TestMistralIndependentPollingAndConcurrency(t *testing.T) {
	started, release := make(chan struct{}, 1), make(chan struct{})
	var models atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			models.Add(1)
			fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
		case "/api/vibe/whoami":
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			fmt.Fprint(w, `{"plan_type":"API"}`)
		case "/v1/billing/budget":
			fmt.Fprintf(w, `{"api_budget":{"initial_budget":30,"usage_percentage":25,"currency":"USD","reset_at":%q}}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		}
	}))
	defer up.Close()
	cfg := config.Default()
	cfg.Quota.Enabled = false
	cfg.Mistral.BaseURL = up.URL + "/v1"
	q := config.Quota{Enabled: true, IntervalSec: 120, Concurrency: 1, StaleSec: 300}
	cfg.Mistral.Quota = &q
	zero := 0
	cfg.Mistral.ModelSyncSec = &zero
	cfg.Accounts = []config.Account{{ID: "m1", RuntimeKey: "mstrl_mock1", Enabled: true}, {ID: "m2", RuntimeKey: "mstrl_mock2", Enabled: true}}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	p := NewPoller(func() *config.Config { return cfg }, func(string) *http.Client { return up.Client() })
	p.dispatch(context.Background())
	<-started
	p.mu.Lock()
	running := len(p.running)
	p.mu.Unlock()
	if running != 1 {
		close(release)
		p.workers.Wait()
		t.Fatalf("independent concurrency cap ignored: %d", running)
	}
	close(release)
	p.workers.Wait()
	cfg.Mistral.Quota.Enabled = false
	p.dispatch(context.Background())
	p.workers.Wait()
	if models.Load() != 1 {
		t.Fatal("disabled pool polled")
	}
	if err := p.fetchMistral(context.Background(), cfg, cfg.Accounts[0], false); err != nil {
		t.Fatal(err)
	}
	if models.Load() != 1 {
		t.Fatal("disabled catalog interval refetched existing catalog")
	}
	if err := p.RefreshNow(context.Background(), cfg.Accounts[0]); err != nil {
		t.Fatal(err)
	}
	if models.Load() != 2 {
		t.Fatal("manual refresh did not bypass polling switches")
	}
	minute := 60
	cfg.Mistral.ModelSyncSec = &minute
	cfg.Mistral.Quota.IntervalSec = 3600
	if err := p.fetchMistral(context.Background(), cfg, cfg.Accounts[0], false); err != nil {
		t.Fatal(err)
	}
	next := p.Get("m1")
	if !next.NextAt.Equal(next.CatalogAt.Add(time.Minute)) {
		t.Fatal("long budget interval suppressed shorter model refresh")
	}
}

func TestMistralBudgetNoCredentialRedirect(t *testing.T) {
	var seen atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen.Store(true) }))
	defer sink.Close()
	for _, path := range []string{"/api/vibe/whoami", "/v1/billing/budget"} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == path {
				http.Redirect(w, r, sink.URL, 302)
				return
			}
			fmt.Fprint(w, `{"plan_type":"API"}`)
		}))
		_, err := FetchMistralBudget(context.Background(), up.Client(), up.URL+"/v1", "mock-key", "m")
		up.Close()
		if err == nil || seen.Load() {
			t.Fatal("credential redirect followed")
		}
	}
}

func TestMistralMetadataNoCredentialRedirect(t *testing.T) {
	var seen atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen.Store(true) }))
	defer sink.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL+"/models", 302) }))
	defer up.Close()
	if _, err := FetchMistralCatalog(context.Background(), up.Client(), up.URL, "mstrl_mock_secret", "test"); err == nil || seen.Load() {
		t.Fatal("redirect followed")
	}
}
