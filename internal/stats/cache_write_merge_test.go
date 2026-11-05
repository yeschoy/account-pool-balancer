package stats

import "testing"

func TestCacheWriteMergeAliasesShareOneMeasurement(t *testing.T) {
	s := New()
	canonical, legacy := int64(5), int64(3)
	s.Record(Sample{Provider: "opencode-go", CacheWrite: &canonical, CacheCreation: &legacy})
	s.Record(Sample{Provider: "tokenharbor", CacheCreation: &legacy})
	got := s.Summary().Total
	if got.CacheWrite != 8 || got.CacheCreation != 8 || got.CacheWriteKnownRequests != 2 {
		t.Fatalf("aliases double-counted or lost measurement: %+v", got)
	}
	restored := New()
	restored.Import(s.Export())
	if again := restored.Summary().Total; again.CacheWrite != 8 || again.CacheCreation != 8 || again.CacheWriteKnownRequests != 2 {
		t.Fatal("write counters lost on persistence")
	}
}
