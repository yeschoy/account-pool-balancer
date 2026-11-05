package thregister

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func pricingFixture(tier string) string {
	plans := []map[string]any{}
	for _, p := range []struct{ tier, slug, name string }{
		{"free", "free", "Free"}, {"plus", "starter_pass_monthly", "Agent Pass"},
		{"pro", "pro_pass_monthly", "Office Pass"}, {"frontier", "frontier_pass_monthly", "Frontier Pass"},
	} {
		state := "available"
		if tier == p.tier {
			state = "current"
		}
		plans = append(plans, map[string]any{"plan": map[string]string{"tier": p.tier, "slug": p.slug, "name": p.name}, "state": state})
	}
	var cycle any
	if tier != "free" {
		cycle = "monthly"
	}
	props := map[string]any{"free": plans[0], "plans": plans[1:], "signedIn": true, "currentCycle": cycle}
	raw, _ := json.Marshal([]any{"$", "$L3a", nil, props})
	chunk, _ := json.Marshal([]any{1, "a:" + string(raw) + "\n"})
	return "<html><script>self.__next_f.push(" + string(chunk) + ")</script></html>"
}

func TestSubscriptionReadsEffectiveAuthenticatedPlan(t *testing.T) {
	for _, tier := range []string{"free", "plus", "pro", "frontier"} {
		t.Run(tier, func(t *testing.T) {
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/pricing" || r.Header.Get("Cookie") != "session=saved" || r.Header.Get("Cache-Control") == "" {
					t.Error("must only read uncached pricing with saved session")
				}
				w.Write([]byte(pricingFixture(tier)))
			}))
			defer up.Close()
			f, _ := New(Config{BaseURL: up.URL})
			defer f.Close()
			f.SeedCookies([]NamedCookie{{Name: "session", Value: "saved"}})
			p, err := f.CheckSubscription(context.Background())
			if err != nil || p.Tier != tier || p.Active() != (tier != "free") || p.CheckedAt.IsZero() || calls != 1 {
				t.Fatalf("bad subscription: %+v %v calls=%d", p, err, calls)
			}
		})
	}
}

func TestSubscriptionFailsClosedOnSchemaAndSessionDrift(t *testing.T) {
	for name, body := range map[string]string{
		"no flight":     `<html>Paid subscription active checkout_url thk_secret</html>`,
		"signed out":    strings.Replace(pricingFixture("plus"), `\"signedIn\":true`, `\"signedIn\":false`, 1),
		"missing cycle": strings.Replace(pricingFixture("plus"), `\"monthly\"`, `null`, 1),
		"two current":   strings.Replace(pricingFixture("plus"), `\"state\":\"available\"`, `\"state\":\"current\"`, 1),
		"no current":    pricingFixture("unknown"),
		"unknown tier":  strings.ReplaceAll(pricingFixture("plus"), `starter_pass_monthly`, `unknown_pass`),
		"ambiguous":     pricingFixture("free") + pricingFixture("plus"),
	} {
		t.Run(name, func(t *testing.T) {
			if p, err := parseSubscription([]byte(body)); err == nil || p.Active() {
				t.Fatalf("unsafe schema accepted: %+v", p)
			}
		})
	}
}

func TestSubscriptionRefusesRedirectErrorAndOversizeWithoutRetry(t *testing.T) {
	for _, status := range []int{302, 401, 403, 429, 500, 200} {
		calls := 0
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Location", "/must-not-follow")
			w.WriteHeader(status)
			if status == 200 {
				w.Write([]byte(strings.Repeat("x", maxBody+1)))
			} else {
				w.Write([]byte("secret_cookie"))
			}
		}))
		f, _ := New(Config{BaseURL: up.URL})
		f.SeedCookies([]NamedCookie{{Name: "session", Value: "saved"}})
		p, err := f.CheckSubscription(context.Background())
		f.Close()
		up.Close()
		if err == nil || p.Status != "unknown" || p.Active() || strings.Contains(err.Error(), "secret_cookie") || calls != 1 {
			t.Fatalf("unsafe HTTP handling status=%d p=%+v err=%v calls=%d", status, p, err, calls)
		}
	}
}

func TestSubscriptionMissingSessionDoesNotCallHTTP(t *testing.T) {
	f, _ := New(Config{BaseURL: "http://127.0.0.1:1"})
	defer f.Close()
	p, err := f.CheckSubscription(context.Background())
	if err == nil || p.Status != "unknown" || !strings.Contains(err.Error(), "会话") {
		t.Fatal("missing session must fail locally")
	}
}
