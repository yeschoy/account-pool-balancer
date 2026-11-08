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

func TestMistralSubscriptionIndependentOfBudget(t *testing.T) {
	var status atomic.Int32
	status.Store(200)
	var redirected atomic.Bool
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Store(true) }))
	defer sink.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
		case "/api/vibe/whoami":
			fmt.Fprint(w, `{"plan_type":"API","plan_name":"FREE"}`)
		case "/v1/billing/budget":
			fmt.Fprintf(w, `{"api_budget":{"initial_budget":15,"usage_percentage":10,"currency":"USD","reset_at":%q}}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		case "/v1/billing/subscription":
			if status.Load() == 302 {
				http.Redirect(w, r, sink.URL, 302)
				return
			}
			if status.Load() != 200 {
				w.WriteHeader(int(status.Load()))
				fmt.Fprint(w, `{"error":"private-body"}`)
				return
			}
			fmt.Fprint(w, `{"plan_name":"EDU","payg_enabled":true,"vibe_payg_enabled":false}`)
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
	if err := p.RefreshNow(context.Background(), cfg.Accounts[0]); err != nil {
		t.Fatal(err)
	}
	good := p.Get("m").Mistral
	if good.Plan != "Free" || good.Subscription == nil || good.Subscription.Plan != "Student" || good.Subscription.PAYG == nil || !*good.Subscription.PAYG || good.Subscription.VibePAYG == nil || *good.Subscription.VibePAYG {
		t.Fatal("lost key access plan, organization plan, or explicit PAYG flags")
	}
	*good.Subscription.PAYG = false
	if !*p.Get("m").Mistral.Subscription.PAYG {
		t.Fatal("subscription clone aliases the cached snapshot")
	}
	for _, code := range []int32{403, 302} {
		status.Store(code)
		if err := p.RefreshNow(context.Background(), cfg.Accounts[0]); err != nil {
			t.Fatal("optional subscription failure invalidated the budget", err)
		}
		q := p.Get("m")
		if !q.FreshMistral(time.Now(), time.Hour) || q.Mistral.Subscription.Plan != "Student" || q.Mistral.Subscription.Error == "" || q.Mistral.Subscription.At.IsZero() || redirected.Load() {
			t.Fatal("failed subscription lost last good data, budget freshness, or redirect safety")
		}
		raw, _ := json.Marshal(q)
		var saved Snapshot
		if json.Unmarshal(raw, &saved) != nil || saved.Mistral.Subscription.Plan != "Student" || strings.Contains(string(raw), "private-body") {
			t.Fatal("subscription persistence/privacy contract failed")
		}
	}
}
