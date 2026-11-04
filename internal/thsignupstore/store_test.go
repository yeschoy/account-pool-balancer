package thsignupstore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "th-signup.json")
	s, err := Open(path, secret)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s, path
}

func TestAddDeduplicatesMailboxes(t *testing.T) {
	s, _ := openTemp(t)
	got, err := s.Add("att_1", []Row{{Email: "A@icloud.com", Password: "pw", MailboxURL: "https://m/messages/a"}})
	if err != nil || len(got) != 1 {
		t.Fatalf("add: %v %v", got, err)
	}
	again, err := s.Add("att_2", []Row{{Email: "a@icloud.com", Password: "other"}})
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if again[0].Attempt != "att_1" || again[0].ID != got[0].ID {
		t.Fatalf("re-add must return the existing row, got %+v", again[0])
	}
	if !strings.EqualFold(again[0].Email, "a@icloud.com") {
		t.Fatalf("email must be normalised: %q", again[0].Email)
	}
	if n := len(s.List()); n != 1 {
		t.Fatalf("want 1 row, got %d", n)
	}
}

func TestClaimIsExclusiveAndSurvivesReopen(t *testing.T) {
	s, path := openTemp(t)
	added, _ := s.Add("att_1", []Row{{Email: "b@icloud.com", Password: "pw"}})
	id := added[0].ID
	if _, err := s.Claim(id); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.Claim(id); !errors.Is(err, ErrInvalid) {
		t.Fatalf("second claim must fail, got %v", err)
	}
	// A process that dies mid-run leaves running rows: reopening must mark
	// them uncertain, never silently requeue them.
	s2, err := Open(path, secret)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	row, err := s2.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != Uncertain {
		t.Fatalf("want uncertain after reopen, got %s", row.Status)
	}
	if _, err := s2.Retry(id); err != nil {
		t.Fatalf("retry after uncertain: %v", err)
	}
}

func TestFileIsEncryptedAnd0600(t *testing.T) {
	s, path := openTemp(t)
	if _, err := s.Add("att_1", []Row{{Email: "c@icloud.com", Password: "hunter2-secret"}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2-secret") || strings.Contains(string(raw), "c@icloud.com") {
		t.Fatal("store file must not contain plaintext credentials")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("want 0600, got %o", perm)
	}
	if _, err := Open(path, []byte("another-secret-0123456789abcdef")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("a different routing secret must not open the store, got %v", err)
	}
}

func TestSummaryHidesCredentials(t *testing.T) {
	row := Row{ID: "thr_1", Email: "d@icloud.com", Password: "pw", APIKey: "thk_live_abcdefghijklmnop", ProxyURL: "socks5://u:p@1.2.3.4:1080"}
	sum := SummaryOf(row)
	if strings.Contains(sum.KeyMasked, "abcdefghijklmnop") {
		t.Fatalf("key must be masked: %q", sum.KeyMasked)
	}
	if sum.Proxy != "socks5://1.2.3.4:1080" {
		t.Fatalf("proxy label leaked or wrong: %q", sum.Proxy)
	}
	if b, _ := json.Marshal(row); strings.Contains(string(b), "pw") {
		t.Fatalf("Row must marshal redacted, got %s", b)
	}
}
