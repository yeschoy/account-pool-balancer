package stats

import "testing"

func TestGoGatewayCacheCoverageAndAffinitySurviveExport(t *testing.T) {
	s := New()
	in, cached, writes, ttft := int64(10), int64(5), int64(5), int64(20)
	s.Record(Sample{Provider: "opencode-go", AccountID: "go1", Input: &in, Cached: &cached, CacheWrite: &writes, Prompt: 20, Result: "success", BindingResult: "hit", CacheAffinityResult: "session-preferred", DurationMs: 100, TTFTMs: &ttft})
	s.Record(Sample{Provider: "opencode-go", AccountID: "go1", Input: &in, Prompt: 10, Result: "success", BindingResult: "miss", CacheAffinityResult: "hit", DurationMs: 50})
	s.Record(Sample{Provider: "opencode-zen", Input: &in, Cached: &cached, Prompt: 10, Result: "success"})
	restored := New()
	restored.Import(s.Export())
	got := restored.SummaryFor("opencode-go").Total
	if got.Requests != 2 || got.Prompt != 30 || got.CachePrompt != 20 || got.CacheCached != 5 || got.CacheKnownRequests != 1 || got.CacheWrite != 5 {
		t.Fatalf("wrong cache coverage: %+v", got)
	}
	if got.BindingHits != 1 || got.AffinityRequests != 2 || got.CacheAffinityHits != 1 || got.DurationMs != 150 || got.TTFTCount != 1 || got.TTFTMs != 20 {
		t.Fatalf("wrong affinity/timing: %+v", got)
	}
}
