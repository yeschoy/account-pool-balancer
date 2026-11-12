package quota

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestDeployPollingHasIndependentConcurrency(t *testing.T) {
	started := make(chan string, 8)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.Header.Get("Authorization")
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"native"}]}`)
	}))
	defer up.Close()
	c := config.Default()
	c.Deploy.BaseURL = up.URL + "/api/v1"
	c.Upstream.RiverBase = up.URL + "/v1"
	c.Quota.Concurrency = 1
	q := c.Deploy.EffectiveQuota()
	q.Concurrency = 2
	c.Deploy.Quota = &q
	for i := 0; i < 3; i++ {
		c.Accounts = append(c.Accounts, config.Account{ID: fmt.Sprintf("d%d", i), Provider: config.ProviderDeploy, RuntimeKey: fmt.Sprintf("deploy_mock_%d", i)})
	}
	c.Accounts = append(c.Accounts, config.Account{ID: "river", Provider: config.ProviderRiver, RuntimeKey: "rv_mock"})
	p := NewPoller(func() *config.Config { return c }, func(string) *http.Client { return up.Client() })
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); close(release); p.workers.Wait() }()
	p.dispatch(ctx)
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("independent workers did not start")
		}
	}
	p.mu.Lock()
	total := len(p.running)
	d := 0
	for _, a := range c.Accounts {
		if a.Provider == config.ProviderDeploy && p.running[a.ID] != nil {
			d++
		}
	}
	p.mu.Unlock()
	if total != 3 || d != 2 {
		t.Fatalf("budgets not independent: total=%d deploy=%d", total, d)
	}
	select {
	case <-started:
		t.Fatal("extra worker exceeded budget")
	default:
	}
}
