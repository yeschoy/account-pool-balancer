package telnyxsignupstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddManySkipsDuplicateEmailIgnoringCase(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := NewRow("Lilah@outlook.com")
	b := NewRow("lilah@outlook.com")
	c := NewRow("other@outlook.com")
	added, errs, err := st.AddMany([]Row{a, b, c})
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 2 {
		t.Fatalf("added = %d, want 2", len(added))
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %v, want 1", errs)
	}
}

func TestSettingsClampConcurrency(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.SaveSettings(Settings{Concurrency: 99})
	if err != nil {
		t.Fatal(err)
	}
	if got.Concurrency != MaxConcurrency {
		t.Errorf("concurrency = %d, want %d", got.Concurrency, MaxConcurrency)
	}
	got, _ = st.SaveSettings(Settings{Concurrency: -5})
	if got.Concurrency != MinConcurrency {
		t.Errorf("concurrency = %d, want %d", got.Concurrency, MinConcurrency)
	}
}

// A store written by an older build (missing the newer settings fields) must
// still load, with the gaps filled from defaults rather than zeroed.
func TestMergeSettingsFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	raw := `{"version":1,"rows":[],"settings":{"concurrency":3}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	set := st.Settings()
	if set.Concurrency != 3 {
		t.Errorf("concurrency lost: %d", set.Concurrency)
	}
	if set.SiteKey == "" || set.TurnstileAction == "" {
		t.Error("turnstile defaults not applied")
	}
	if set.MailPollSec == 0 || set.RequestTimeoutSec == 0 {
		t.Errorf("timings not defaulted: %+v", set)
	}
}

func TestPersistAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	row := NewRow("a@b.com")
	row.Status = Keyed
	row.APIKey = "KEY01_x"
	row.RefreshToken = "M.C533!x$$"
	row.ShareToken = "share-tok"
	if _, err := st.Add(row); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveSettings(Settings{Concurrency: 4, CaptchaToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Flush(); err != nil {
		t.Fatal(err)
	}

	re, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows := re.List()
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	// The refresh token contains '!' and '$' and must survive a round trip.
	if rows[0].RefreshToken != "M.C533!x$$" {
		t.Errorf("refresh token corrupted: %q", rows[0].RefreshToken)
	}
	if rows[0].APIKey != "KEY01_x" {
		t.Errorf("api key lost: %q", rows[0].APIKey)
	}
	if got := re.Settings().CaptchaToken; got != "tok" {
		t.Errorf("captcha token lost: %q", got)
	}
}

func TestClearFinishedKeepsActiveRows(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "s.json"))
	for _, s := range []Status{Queued, Running, Waiting, Linked, Keyed, Failed, Cancelled} {
		r := NewRow(string(s) + "@x.com")
		r.Status = s
		if _, err := st.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	n, err := st.ClearFinished()
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("removed = %d, want 3 (keyed, failed, cancelled)", n)
	}
	if got := len(st.List()); got != 4 {
		t.Errorf("kept = %d, want 4", got)
	}
}

func TestStatusSemantics(t *testing.T) {
	if !Keyed.Done() {
		t.Error("keyed must be done")
	}
	for _, s := range []Status{Queued, Failed, Cancelled} {
		if !s.Retryable() {
			t.Errorf("%s must be retryable", s)
		}
	}
	// A missing portal link cannot be resumed: waiting must not be retryable
	// from the console without a fresh signup.
	for _, s := range []Status{Running, Waiting, Linked, Keyed} {
		if s.Retryable() {
			t.Errorf("%s must not be retryable", s)
		}
	}
}
