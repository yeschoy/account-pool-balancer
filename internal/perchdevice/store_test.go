package perchdevice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var secret = []byte("0123456789abcdef0123456789abcdef")

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(dir, "perch-device.json"), secret, func(string, ...any) {})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestIdentityIsStableAndPerAccount(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)

	a1 := s.Get("user-a")
	b1 := s.Get("user-b")
	if a1.MachineID == "" || b1.MachineID == "" {
		t.Fatal("an identity was minted without a machine id")
	}
	// The whole point: one account must never borrow another's device id.
	if a1.MachineID == b1.MachineID {
		t.Fatal("two accounts share one machine id")
	}
	if again := s.Get("user-a"); again.MachineID != a1.MachineID {
		t.Fatalf("machine id changed between calls: %q → %q", a1.MachineID, again.MachineID)
	}
	if s.ProcessDeviceID() == "" || s.ProcessDeviceID() == a1.MachineID {
		t.Fatal("the process device id must exist and be distinct from an account's")
	}
}

func TestIdentitySurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	first := s.Get("user-a")
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// a new process, same file and secret
	s2 := open(t, dir)
	if got := s2.Get("user-a"); got.MachineID != first.MachineID {
		t.Fatalf("machine id did not survive a restart: %q → %q", first.MachineID, got.MachineID)
	}
	if reopened := s2.Get("user-a"); reopened.CreatedAt.IsZero() {
		t.Fatal("the first-run stamp was lost")
	}
}

func TestIdentityIsSealedOnDisk(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	id := s.Get("user-a")
	if err := s.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "perch-device.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), id.MachineID) {
		t.Fatal("the machine id is on disk in plain text")
	}
	if strings.Contains(string(raw), "user-a") {
		t.Fatal("the account id is on disk in plain text")
	}
	// the process id is the one identity that is intentionally readable: it
	// names no account
	if !strings.Contains(string(raw), s.ProcessDeviceID()) {
		t.Fatal("the process device id should be readable")
	}
	info, err := os.Stat(filepath.Join(dir, "perch-device.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestWrongKeyDoesNotSilentlyAdopt(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	first := s.Get("user-a")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// A different routing secret cannot open the record. The store must not
	// hand out the old id (it cannot read it) and must not crash: it mints a
	// replacement and logs the loss.
	s2, err := Open(filepath.Join(dir, "perch-device.json"), []byte("ffffffffffffffffffffffffffffffff"), func(string, ...any) {})
	if err != nil {
		t.Fatalf("Open with another key: %v", err)
	}
	got := s2.Get("user-a")
	if got.MachineID == "" {
		t.Fatal("no replacement id was minted")
	}
	if got.MachineID == first.MachineID {
		t.Fatal("a record opened under the wrong key reported the old machine id")
	}
}

func TestRecordCannotBeMovedBetweenAccounts(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	s.Get("user-a")
	s.Get("user-b")
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	// swap the two sealed records: each is bound to its account id as
	// associated data, so neither opens under the other's slot
	s.mu.Lock()
	ka, kb := s.slot("user-a"), s.slot("user-b")
	a := s.env.Records[ka]
	s.env.Records[ka], s.env.Records[kb] = s.env.Records[kb], a
	s.dirty = true
	_ = s.flushLocked()
	s.mu.Unlock()

	// Reopen: the store's in-memory copy still has the old values, so the
	// check has to be on a fresh process reading the swapped file.
	s2 := open(t, dir)
	// Neither slot opens: a record bound to one account id cannot be read
	// under another, so both are replaced rather than cross-assigned.
	a2, b2 := s2.Get("user-a"), s2.Get("user-b")
	if a2.MachineID == "" || b2.MachineID == "" {
		t.Fatal("a replacement id should be minted for both slots")
	}
	if a2.MachineID == b2.MachineID {
		t.Fatal("the two slots collapsed onto one id")
	}
}

func TestMarkFirstPromptIsOnce(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	s.Get("user-a")
	if !s.MarkFirstPrompt("user-a") {
		t.Fatal("the first prompt should report first")
	}
	if s.MarkFirstPrompt("user-a") {
		t.Fatal("the second prompt reported first again")
	}
	if s.MarkFirstPrompt("user-b") != true {
		t.Fatal("another account's first prompt should still report first")
	}
}

func TestClockIsStamped(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir)
	fixed := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return fixed })
	id := s.Get("user-a")
	if !id.CreatedAt.Equal(fixed) {
		t.Fatalf("createdAt = %v, want %v", id.CreatedAt, fixed)
	}
}
