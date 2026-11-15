package altapisignupstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Two rows for one mailbox would both consume the same 30s send window and
// both fail, so a duplicate email must be refused rather than replaced.
func TestAddRefusesDuplicateEmail(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(Candidate{Email: "a@b.c", MailboxURL: "u", Status: Queued}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Add(Candidate{Email: "A@B.C", MailboxURL: "u2", Status: Queued}); err == nil {
		t.Fatal("duplicate email (case-insensitive) was accepted")
	}
}

func TestAddRequiresEmail(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "s.json"))
	if _, err := st.Add(Candidate{MailboxURL: "u"}); err == nil {
		t.Fatal("candidate without email was accepted")
	}
}

// The file holds plaintext credentials: it must be 0600 and survive a reload.
func TestStorePersistsWithPrivatePerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	st, _ := Open(path)
	c, err := st.Add(Candidate{Email: "a@b.c", MailboxURL: "u", Status: Queued})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Update(c.ID, func(cc *Candidate) error {
		cc.APIKey = "1AgxNApHSVWO2ofXIladoNLnCIEMOCT9gF5CZibnQAeeHAHF"
		cc.SessionCookie = "abc"
		cc.BalanceUSD = 10
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm %o, want 600 (the file holds a session and a key)", perm)
	}

	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.APIKey == "" || got.SessionCookie != "abc" || got.BalanceUSD != 10 {
		t.Fatalf("lost fields after reload: %+v", got)
	}
}

func TestSummarizeCountsBalanceHonestly(t *testing.T) {
	st, _ := Open(filepath.Join(t.TempDir(), "s.json"))
	a, _ := st.Add(Candidate{Email: "a@b.c", Status: KeyCreated})
	b, _ := st.Add(Candidate{Email: "b@b.c", Status: KeyCreated})
	// a has a real balance read; b only has a key, so its balance is unknown.
	// A real balance read: the quota handler writes BalanceUSD together with
	// QuotaCheckedAt, which is what "known" means.
	_, _ = st.Update(a.ID, func(cc *Candidate) error {
		cc.SessionCookie = "s"
		cc.BalanceUSD = 10
		cc.QuotaCheckedAt = time.Now()
		return nil
	})
	_, _ = st.Update(b.ID, func(cc *Candidate) error {
		cc.APIKey = "k"
		return nil
	})
	sum := st.Summarize()
	if sum.Total != 2 || sum.WithKey != 1 || sum.WithSession != 1 {
		t.Fatalf("counts wrong: %+v", sum)
	}
	if sum.BalanceUSD != 10 {
		t.Fatalf("balance sum %v, want 10", sum.BalanceUSD)
	}
	if sum.BalanceCount != 1 {
		t.Fatalf("balance count %v, want 1", sum.BalanceCount)
	}
}

// An older file without the newer settings must inherit defaults rather than
// ending up with a 0s send interval (which would hammer the site).
func TestOpenFillsMissingSettings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"candidates":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s := st.Settings()
	if s.MinIntervalSec <= 0 {
		t.Fatalf("min interval %v, want a positive default", s.MinIntervalSec)
	}
	if s.TokenGroup == "" || s.BaseURL == "" {
		t.Fatalf("defaults missing: %+v", s)
	}
}
