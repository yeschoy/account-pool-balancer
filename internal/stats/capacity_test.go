package stats

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestSparseSharedAccountStorageAndMixedProviderReset(t *testing.T) {
	s := New()
	now := time.Now()
	s.now = func() time.Time { return now }
	in, cache := int64(100), int64(80)
	record := func(p, id string) {
		s.Record(Sample{Provider: p, AccountID: id, Model: "model", Proxy: "direct", Result: "success", Input: &in, Cached: &cache, Prompt: in})
	}
	for i := 0; i < 4000; i++ {
		record("mistral", fmt.Sprint(i))
	}
	for id, r := range s.all.account {
		if len(r.slots) != 1 || r != s.prov["mistral"].account[id] {
			t.Fatal("cold account preallocated or duplicated")
		}
	}
	record("river", "0")
	if s.all.account["0"] == s.prov["mistral"].account["0"] || s.SummaryFor("mistral").Accounts["0"].Requests != 1 || s.Summary().Accounts["0"].Requests != 2 {
		t.Fatal("mixed provider alias")
	}
	s.ResetCacheRates("mistral")
	if s.Summary().Accounts["0"].CacheCached != 80 || s.SummaryFor("river").Accounts["0"].CacheCached != 80 || s.SummaryFor("mistral").Accounts["0"].CacheKnownRequests != 0 {
		t.Fatal("mixed reset isolation")
	}
	before := s.Summary()
	copy := New()
	copy.now = s.now
	copy.Import(s.Export())
	if !reflect.DeepEqual(before, copy.Summary()) {
		t.Fatal("sparse format changed round trip")
	}
	if copy.all.account["1"] != copy.prov["mistral"].account["1"] {
		t.Fatal("restart lost deduplication")
	}
	now = now.Add(24 * time.Hour)
	if s.Summary().Total.Requests != 0 || len(s.Summary().Accounts) != 0 {
		t.Fatal("old minutes survived")
	}
	record("mistral", "0")
	if s.Summary().Accounts["0"].Requests != 1 {
		t.Fatal("idle account could not resume")
	}
}
func TestSparseTotalsMatchDenseWindowAndRateReset(t *testing.T) {
	s := New()
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	for i := 0; i < 1600; i++ {
		s.Record(Sample{Provider: "mistral", AccountID: "a", Result: "success"})
		now = now.Add(time.Minute)
	}
	// The first sample is now exactly 1600 minutes old; 1439 remain because
	// the rolling cut excludes the minute equal to now-1440.
	if n := s.Summary().Accounts["a"].Requests; n != 1439 {
		t.Fatal("wrong rolling boundary", n)
	}
	if len(s.all.account["a"].slots) > 1440 {
		t.Fatal("unbounded slots")
	}
	copy := New()
	copy.now = s.now
	copy.Import(s.Export())
	if !reflect.DeepEqual(s.Summary(), copy.Summary()) {
		t.Fatal("dense minute snapshot changed")
	}
}
func TestSparseProjectionsReclaimIdleAndBoundedKeys(t *testing.T) {
	s := New()
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	for i := 0; i < maxProxies; i++ {
		s.Record(Sample{AccountID: "one", Provider: "mistral", Model: fmt.Sprint(i), Proxy: fmt.Sprint(i), Result: "success"})
	}
	now = now.Add(25 * time.Hour)
	s.SummaryFor("mistral")
	s.Record(Sample{AccountID: "one", Provider: "mistral", Model: "new", Proxy: "new", Result: "success"})
	if s.all.account["one"] != s.prov["mistral"].account["one"] {
		t.Fatal("idle provider projection no longer shares account")
	}
	v := s.SummaryFor("mistral")
	if len(v.Models) != 1 || v.Models["new"].Requests != 1 || len(v.Proxies) != 1 || v.Proxies["new"].Requests != 1 {
		t.Fatal("expired bounded model/proxy keys not reclaimed")
	}
}

func BenchmarkSparseAccounts(b *testing.B) {
	ids := make([]string, 4000)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	b.ReportAllocs()
	for b.Loop() {
		s := New()
		for _, id := range ids {
			s.Record(Sample{Provider: "mistral", AccountID: id, Result: "success"})
		}
	}
}
