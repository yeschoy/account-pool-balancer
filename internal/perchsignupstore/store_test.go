package perchsignupstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

func candidate(login string) Candidate {
	return Candidate{Login: login, Password: "password-secret", TOTPSecret: "totp-secret", MailboxURL: "https://mail.test/secret", ProxyURL: "http://user:proxy-secret@proxy.test:8080/path?token=secret"}
}
func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "store.json")
	s, e := Open(p, testKey)
	if e != nil {
		t.Fatal(e)
	}
	return s, p
}

func TestPersistenceRestartAndReady(t *testing.T) {
	s, p := openTest(t)
	added, e := s.Import([]Candidate{candidate("a@example.test"), candidate("b@example.test"), candidate(" A@example.test ")})
	if e != nil || len(added) != 2 {
		t.Fatalf("import: %v %v", added, e)
	}
	r, e := s.Claim()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Update(r.ID, Running, []byte("session-secret"), "account"); e != nil {
		t.Fatal(e)
	}
	r2, e := s.Claim()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Update(r2.ID, Ready, []byte("ready-secret"), "account2"); e != nil {
		t.Fatal(e)
	}
	s, e = Open(p, testKey)
	if e != nil {
		t.Fatal(e)
	}
	a, e := s.Get(r.ID)
	if e != nil || a.Status != Uncertain || string(a.Session) != "session-secret" {
		t.Fatalf("restart running: %v %v", a, e)
	}
	b, e := s.Get(r2.ID)
	if e != nil || b.Status != Ready || string(b.Session) != "ready-secret" {
		t.Fatalf("restart ready: %v %v", b, e)
	}
	if _, e = s.Claim(); !errors.Is(e, ErrNoQueued) {
		t.Fatal(e)
	}
	if e = s.Update(r2.ID, Completed, nil, ""); e != nil {
		t.Fatal(e)
	}
	b, _ = s.Get(r2.ID)
	if len(b.Session) != 0 {
		t.Fatal("session retained")
	}
	s, e = Open(p, testKey)
	if e != nil {
		t.Fatal(e)
	}
	b, _ = s.Get(r2.ID)
	if b.Status != Completed || len(b.Session) != 0 || b.Candidate.Password != "" || b.Candidate.TOTPSecret != "" || b.Candidate.MailboxURL != "" {
		t.Fatal("completion cleanup not durable")
	}
	if b.Candidate.Login != "b@example.test" || b.AccountID != "account2" || b.Candidate.ProxyURL == "" {
		t.Fatal("completion removed required metadata")
	}
	data, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	var env envelope
	if e = json.Unmarshal(data, &env); e != nil {
		t.Fatal(e)
	}
	plain, e := s.aead.Open(nil, env.Nonce, env.Data, []byte("perchsignupstore/v1"))
	if e != nil {
		t.Fatal(e)
	}
	var stored diskData
	if e = json.Unmarshal(plain, &stored); e != nil {
		t.Fatal(e)
	}
	for _, row := range stored.Records {
		if row.ID == r2.ID && (row.Candidate.Password != "" || row.Candidate.TOTPSecret != "" || row.Candidate.MailboxURL != "" || len(row.Session) != 0) {
			t.Fatal("credentials retained in encrypted payload")
		}
	}
}

func TestEncryptionRedactionAndTamper(t *testing.T) {
	s, p := openTest(t)
	_, e := s.Import([]Candidate{candidate("a@example.test")})
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.Claim()
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Update(r.ID, Running, []byte("session-secret"), "account-secret"); e != nil {
		t.Fatal(e)
	}
	r, _ = s.Get(r.ID)
	data, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(p)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("mode %v %v", st, e)
	}
	for _, v := range []any{r, r.Candidate, s, s.List()} {
		j, _ := json.Marshal(v)
		text := fmt.Sprintf("%v %#v %+v %s", v, v, v, j)
		for _, secret := range []string{"password-secret", "totp-secret", "proxy-secret", "mail.test/secret", "session-secret", "account-secret", "a@example.test"} {
			if strings.Contains(text, secret) || bytes.Contains(data, []byte(secret)) {
				t.Fatalf("secret leaked: %s", secret)
			}
		}
	}
	list := s.List()
	if list[0].Proxy != "http://proxy.test:8080" {
		t.Fatal(list[0].Proxy)
	}
	if _, e = Open(p, []byte("wrong-key-0123456789012345678901")); !errors.Is(e, ErrCorrupt) {
		t.Fatalf("wrong key: %v", e)
	}
	var env envelope
	if e = json.Unmarshal(data, &env); e != nil {
		t.Fatal(e)
	}
	env.Data[0] ^= 1
	data, _ = json.Marshal(env)
	if e = os.WriteFile(p, data, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(p, testKey); !errors.Is(e, ErrCorrupt) {
		t.Fatalf("tamper: %v", e)
	}
}

func TestWriteFailuresRollback(t *testing.T) {
	s, p := openTest(t)
	_, e := s.Import([]Candidate{candidate("a")})
	if e != nil {
		t.Fatal(e)
	}
	// A nonexistent parent deterministically fails even when tests run as root.
	original := s.path
	s.path = filepath.Join(p, "missing", "store")
	if _, e = s.Claim(); e == nil {
		t.Fatal("claim succeeded")
	}
	if s.List()[0].Status != Queued {
		t.Fatal("claim changed memory")
	}
	if _, e = s.Import([]Candidate{candidate("b")}); e == nil || len(s.List()) != 1 {
		t.Fatal("import changed memory")
	}
	s.path = original
	r, e := s.Claim()
	if e != nil {
		t.Fatal(e)
	}
	s.path = filepath.Join(p, "missing", "store")
	if e = s.Update(r.ID, Ready, []byte("x"), ""); e == nil {
		t.Fatal("update succeeded")
	}
	got, _ := s.Get(r.ID)
	if got.Status != Running || len(got.Session) != 0 {
		t.Fatal("update changed memory")
	}
	s.path = original
	reopened, e := Open(p, testKey)
	if e != nil || reopened.List()[0].Status != Uncertain {
		t.Fatalf("disk mismatch: %v", e)
	}
}

func TestNonceTransitionsAndCopies(t *testing.T) {
	s, p := openTest(t)
	a, err := s.Import([]Candidate{candidate("one")})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Update(a[0].ID, Completed, nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("queued completion accepted")
	}
	r, err := s.Claim()
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var x, y envelope
	if json.Unmarshal(before, &x) != nil || json.Unmarshal(after, &y) != nil || bytes.Equal(x.Nonce, y.Nonce) {
		t.Fatal("nonce did not change")
	}
	if err := s.Update(r.ID, Ready, nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("ready without session accepted")
	}
	session := []byte("session-value")
	if err := s.Update(r.ID, Running, session, ""); err != nil {
		t.Fatal(err)
	}
	session[0] = 'X'
	r, _ = s.Get(r.ID)
	if string(r.Session) != "session-value" {
		t.Fatal("input alias")
	}
	r.Session[0] = 'Y'
	r, _ = s.Get(r.ID)
	if string(r.Session) != "session-value" {
		t.Fatal("output alias")
	}
}

func TestDeleteAndLoginSummary(t *testing.T) {
	s, p := openTest(t)
	rows, err := s.Import([]Candidate{candidate("github-user"), candidate("other@example.test")})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Login != "github-user" || rows[1].Login != "[REDACTED]" {
		t.Fatal(rows)
	}
	r, err := s.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(r.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("deleted running candidate")
	}
	if err := s.Delete(rows[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(rows[1].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	reopened, err := Open(p, testKey)
	if err != nil || len(reopened.List()) != 1 {
		t.Fatalf("delete not persisted: %v", err)
	}
}

func TestConcurrentClaim(t *testing.T) {
	s, _ := openTest(t)
	cs := make([]Candidate, 30)
	for i := range cs {
		cs[i] = candidate(fmt.Sprintf("user-%d", i))
	}
	if _, e := s.Import(cs); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	ids := make(chan string, len(cs))
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := s.Claim()
			if e == nil {
				ids <- r.ID
				if err := s.Update(r.ID, Failed, nil, ""); err != nil {
					t.Error(err)
				}
			} else if !errors.Is(e, ErrNoQueued) {
				t.Error(e)
			}
			_ = s.List()
		}()
	}
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("duplicate claim")
		}
		seen[id] = true
	}
	if len(seen) != len(cs) {
		t.Fatalf("claimed %d", len(seen))
	}
}
