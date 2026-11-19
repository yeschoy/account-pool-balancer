package quota

import (
	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factory"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFactoryLimitsStrictParsing(t *testing.T) {
	for _, b := range []string{`{}`, `{"limits":{"standard":{}}}`, `{"limits":{"standard":{"fiveHour":{}}}}`, `{"limits":{"standard":{"fiveHour":{"usedPercent":"0"}}}}`, `{"limits":{"standard":{"fiveHour":{"usedPercent":1,"windowEnd":"bad"}}}}`} {
		if _, e := parseFactoryLimits([]byte(b)); e == nil {
			t.Fatal("invalid accepted", b)
		}
	}
	q, e := parseFactoryLimits([]byte(`{"limits":{"standard":{"fiveHour":{"usedPercent":0,"windowEnd":"2026-12-01T00:00:00Z"},"weekly":{"usedPercent":130,"windowEnd":1798761600000}},"core":{"monthly":{"usedPercent":5}}},"extraUsageBalanceCents":0,"extraUsageAllowed":false}`))
	if e != nil || len(q.Windows) != 3 || q.Windows[0].UsedPercent != 0 || q.ExtraBalanceCents == nil || *q.ExtraBalanceCents != 0 {
		t.Fatal("valid or unknown/zero lost", e)
	}
}
func TestFactoryRefreshPermissionHistoryAndPersistence(t *testing.T) {
	var status, reads atomic.Int32
	status.Store(200)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fk-mock_test_key" {
			t.Error("wrong credential")
		}
		// The capture separates the plans: whoami is the Bun runtime with the
		// extended flag only, while billing carries the business headers.
		if r.Header.Get("User-Agent") != "Bun/"+factory.BunVersion {
			t.Error("management read without the Bun runtime identity")
		}
		if r.URL.Path == "/api/cli/whoami" {
			if r.Header.Get("X-Factory-Whoami-Extended") != "true" || r.Header.Get("X-Factory-Client") != "" || r.Header.Get("X-Client-Version") != "" || r.Header.Get("X-Factory-Request-Surface") != "" {
				t.Error("whoami headers do not match droid")
			}
			fmt.Fprint(w, `{"userId":"mock-user","orgId":"mock-org"}`)
			return
		}
		if r.Header.Get("X-Factory-Client") != "cli" || r.Header.Get("X-Client-Version") != factory.DroidVersion || r.Header.Get("X-Factory-Request-Surface") != config.FactorySurfaceTUI || r.Header.Get("X-Factory-Org-Id") != "mock-org" || !strings.HasPrefix(r.Header.Get("Traceparent"), "00-") {
			t.Error("billing read without droid's business identity")
		}
		reads.Add(1)
		w.WriteHeader(int(status.Load()))
		if status.Load() == 200 {
			fmt.Fprint(w, `{"limits":{"standard":{"fiveHour":{"usedPercent":42}}}}`)
		} else {
			fmt.Fprint(w, `{"secret":"must-not-store"}`)
		}
	}))
	defer up.Close()
	c := config.Default()
	c.Factory.BaseURL = up.URL
	c.Accounts = []config.Account{{ID: "f", Provider: config.ProviderFactory, RuntimeKey: "fk-mock_test_key"}}
	p := NewPoller(func() *config.Config { return c }, func(string) *http.Client { return up.Client() })
	a := c.Accounts[0]
	if e := p.RefreshNow(context.Background(), a); e != nil {
		t.Fatal(e)
	}
	first := p.Get(a.ID)
	if !first.Factory.Fresh(time.Now(), time.Minute) || first.Catalog != nil || first.WalletUSD != "" {
		t.Fatal("wrong snapshot")
	}
	status.Store(403)
	if e := p.RefreshNow(context.Background(), a); e != nil {
		t.Fatal("quota permission invalidated identity", e)
	}
	q := p.Get(a.ID)
	if q.AuthFailed || q.Factory.QuotaState != "unsupported-credential" || len(q.Factory.Windows) != 1 || q.Factory.Fresh(time.Now(), time.Minute) {
		t.Fatal("permission vs auth/history")
	}
	before := reads.Load()
	if e := p.fetchFactory(context.Background(), c, a, false); e != nil || reads.Load() != before {
		t.Fatal("denied billing loop")
	}
	raw, _ := json.Marshal(q)
	var restored Snapshot
	if json.Unmarshal(raw, &restored) != nil || len(restored.Factory.Windows) != 1 {
		t.Fatal("restart state")
	}
	p.Get(a.ID).Factory.Windows[0].UsedPercent = 99
	if p.Get(a.ID).Factory.Windows[0].UsedPercent == 99 {
		t.Fatal("snapshot alias")
	}
	status.Store(200)
	if e := p.RefreshNow(context.Background(), a); e != nil || reads.Load() != before+1 {
		t.Fatal("manual retry missing")
	}
}
