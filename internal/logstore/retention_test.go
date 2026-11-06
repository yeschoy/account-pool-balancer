package logstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func retainedLimits(days int) Limits {
	counts := map[string]int{}
	for name := range Filters {
		counts[name] = 100
	}
	return Limits{RetentionDays: days, MaxTotalBytes: 8 << 20, MaxRecords: counts}
}
func pruneNow(t *testing.T, s *Store) {
	t.Helper()
	done := make(chan struct{})
	s.ch <- job{prune: true, done: done}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retention maintenance did not finish")
	}
}
func TestRetentionPrunesEveryKindByRecordTimeOnStartup(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	old := now.Add(-8 * 24 * time.Hour)
	fresh := now.Add(-time.Hour)
	for name := range Filters {
		var text strings.Builder
		for _, r := range []keyedRec{{TS: old, RequestID: "expired-" + name}, {TS: fresh, RequestID: "fresh-" + name}} {
			b, _ := json.Marshal(r)
			text.Write(b)
			text.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-20261001-000000-1.jsonl", name)), []byte(text.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A fresh record in an old-mtime file must not be deleted by a coarse file-age pass.
	b, _ := json.Marshal(keyedRec{TS: fresh, RequestID: "fresh-old-file"})
	oldPath := filepath.Join(dir, "requests-20261001-000001-2.jsonl")
	if err := os.WriteFile(oldPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldPath, old, old); err != nil {
		t.Fatal(err)
	}
	untouched := filepath.Join(dir, "operator-audit.jsonl")
	if err := os.WriteFile(untouched, []byte("not a managed business log"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, func() Limits { return retainedLimits(7) })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitFor(t, s.Ready)
	for name := range Filters {
		page, err := s.Query(name, nil, time.Time{}, time.Time{}, 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		expected := 1
		if name == "requests" {
			expected = 2
		}
		if len(page.Items) != expected {
			t.Fatalf("%s retained %d rows, want %d", name, len(page.Items), expected)
		}
		files, _ := filepath.Glob(filepath.Join(dir, name+"-*.jsonl"))
		for _, path := range files {
			raw, _ := os.ReadFile(path)
			if strings.Contains(string(raw), "expired-") {
				t.Fatalf("%s expired record remained on disk", name)
			}
		}
	}
	if raw, err := os.ReadFile(untouched); err != nil || string(raw) != "not a managed business log" {
		t.Fatal("retention touched an unrelated file", err)
	}
}
func TestRetentionChangePrunesLiveSegmentRingAndReopen(t *testing.T) {
	dir := t.TempDir()
	var days atomic.Int64
	days.Store(7)
	s, err := Open(dir, func() Limits { return retainedLimits(int(days.Load())) })
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, s.Ready)
	now := time.Now().UTC()
	old := now.Add(-2 * 24 * time.Hour)
	s.Append("requests", old, keyedRec{TS: old, RequestID: "old"})
	s.Append("requests", now, keyedRec{TS: now, RequestID: "fresh"})
	waitFor(t, func() bool {
		files, _ := filepath.Glob(filepath.Join(dir, "requests-*.jsonl"))
		for _, p := range files {
			raw, _ := os.ReadFile(p)
			if strings.Count(string(raw), "\n") == 2 {
				return true
			}
		}
		return false
	})
	days.Store(1)
	page, _ := s.Query("requests", nil, time.Time{}, time.Time{}, 100, 0)
	if len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "fresh") {
		t.Fatal("queries exposed expired records after a settings change")
	}
	pruneNow(t, s)
	if len(s.kinds["requests"].ordered()) != 1 {
		t.Fatal("maintenance kept expired rows in memory")
	}
	s.Append("requests", now, keyedRec{TS: now, RequestID: "after-prune"})
	waitFor(t, func() bool {
		p, _ := s.Query("requests", nil, time.Time{}, time.Time{}, 100, 0)
		return len(p.Items) == 2
	})
	if removed, err := s.Remove("requests", "fresh"); err != nil || removed.Disk != 1 {
		t.Fatalf("removal after retention rewrite failed: %+v %v", removed, err)
	}
	s.Close()
	reopened, err := Open(dir, func() Limits { return retainedLimits(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	waitFor(t, reopened.Ready)
	page, _ = reopened.Query("requests", nil, time.Time{}, time.Time{}, 100, 0)
	if len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), "after-prune") {
		t.Fatal("expired or deleted record returned after reopening")
	}
}
func TestRetentionDeletesAllExpiredLiveSegmentAndRejectsExpiredAppend(t *testing.T) {
	dir := t.TempDir()
	var days atomic.Int64
	days.Store(7)
	s, err := Open(dir, func() Limits { return retainedLimits(int(days.Load())) })
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	waitFor(t, s.Ready)
	now := time.Now().UTC()
	old := now.Add(-2 * 24 * time.Hour)
	s.Append("errors", old, map[string]any{"ts": old, "requestId": "old"})
	waitFor(t, func() bool { files, _ := filepath.Glob(filepath.Join(dir, "errors-*.jsonl")); return len(files) == 1 })
	days.Store(1)
	pruneNow(t, s)
	files, _ := filepath.Glob(filepath.Join(dir, "errors-*.jsonl"))
	if len(files) != 0 {
		t.Fatal("expired current segment was exempted from cleanup")
	}
	s.Append("errors", old, map[string]any{"ts": old, "requestId": "already-expired"})
	s.Append("errors", now, map[string]any{"ts": now, "requestId": "fresh"})
	pruneNow(t, s)
	page, _ := s.Query("errors", nil, time.Time{}, time.Time{}, 100, 0)
	if len(page.Items) != 1 {
		t.Fatal("expired append was retained or fresh append failed")
	}
}
