package stats

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func withoutCacheRate(b Bucket) Bucket {
	b.CachePrompt, b.CacheCached, b.CacheKnownRequests = 0, 0, 0
	return b
}

func TestResetCacheRatesIsolatedAndPersistent(t *testing.T) {
	s := New()
	now := time.Now()
	s.now = func() time.Time { return now }
	input, output, cached, write := int64(100), int64(20), int64(80), int64(10)
	record := func(provider, account string) {
		s.Record(Sample{Provider: provider, AccountID: account, Model: "shared-model", Proxy: "shared-proxy", Result: "success", Input: &input, Output: &output, Cached: &cached, CacheWrite: &write, Prompt: input, Currency: "USD", Cost: 0.5, BindingResult: "hit"})
	}
	record("mistral", "m1")
	now = now.Add(time.Minute)
	record("mistral", "m1")
	record("river", "r1")
	before, other := s.SummaryFor("mistral"), s.SummaryFor("river")
	allBefore := s.Summary()
	s.ResetCacheRates("mistral")
	cleared := s.SummaryFor("mistral")
	check := func(name string, b, original Bucket) {
		t.Helper()
		if b.CachePrompt != 0 || b.CacheCached != 0 || b.CacheKnownRequests != 0 || b != withoutCacheRate(original) {
			t.Fatalf("%s: reset altered non-rate fields or kept samples: %+v", name, b)
		}
	}
	check("total", cleared.Total, before.Total)
	check("last hour", cleared.LastHour, before.LastHour)
	check("account", cleared.Accounts["m1"], before.Accounts["m1"])
	check("model", cleared.Models["shared-model"], before.Models["shared-model"])
	check("proxy", cleared.Proxies["shared-proxy"], before.Proxies["shared-proxy"])
	for i := range cleared.Hourly {
		check("hourly", cleared.Hourly[i], before.Hourly[i])
	}
	if !reflect.DeepEqual(other, s.SummaryFor("river")) {
		t.Fatal("other provider changed")
	}
	all := s.Summary()
	for name, b := range map[string]Bucket{"total": all.Total, "model": all.Models["shared-model"], "proxy": all.Proxies["shared-proxy"]} {
		if b.CachePrompt != 100 || b.CacheCached != 80 || b.CacheKnownRequests != 1 {
			t.Fatalf("%s: overall rate did not subtract only this provider: %+v", name, b)
		}
	}
	if withoutCacheRate(all.Total) != withoutCacheRate(allBefore.Total) || all.Accounts["m1"].CachePrompt != 0 {
		t.Fatal("overall raw counters changed or account rate survived")
	}
	// Invalid/empty scopes and repeated resets must not affect another pool.
	for _, provider := range []string{"", "unknown", "mistral"} {
		s.ResetCacheRates(provider)
	}
	if !reflect.DeepEqual(all, s.Summary()) {
		t.Fatal("non-scoped/repeated reset changed statistics")
	}
	// JSON disk round-trip must not backfill the cleared rate from raw tokens.
	data, err := json.Marshal(s.Export())
	if err != nil {
		t.Fatal(err)
	}
	var saved Export
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	restored := New()
	restored.now = s.now
	restored.Import(saved)
	if !reflect.DeepEqual(cleared, restored.SummaryFor("mistral")) {
		t.Fatal("reset lost across restart")
	}
	// New requests in exactly the same minute count immediately, not next minute.
	s = restored
	cached = 20
	record("mistral", "m1")
	b := s.SummaryFor("mistral").Total
	if b.Requests != 3 || b.Input != 300 || b.Cached != 180 || b.CachePrompt != 100 || b.CacheCached != 20 || b.CacheKnownRequests != 1 {
		t.Fatalf("new sample or original usage lost: %+v", b)
	}
	now = now.Add(24 * time.Hour)
	if b := s.SummaryFor("mistral").Total; b.Requests != 0 || b.CachePrompt != 0 {
		t.Fatalf("reset samples outlived 24h: %+v", b)
	}
}
