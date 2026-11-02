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

func TestProxyInventoryPersistence(t *testing.T) {
	s, p := openTest(t)
	a, err := s.Import([]Candidate{candidate("existing")})
	if err != nil || len(a) != 1 {
		t.Fatal(err)
	}
	rows, err := s.ImportProxies([]string{" http://alice:password-secret@proxy.test:8080 ", "socks5://bob:other-secret@proxy2.test:1080", "http://alice:password-secret@proxy.test:8080"})
	if err != nil || len(rows) != 2 {
		t.Fatalf("import %d: %v", len(rows), err)
	}
	if rows[0].Proxy != "http://proxy.test:8080" || rows[1].Proxy != "socks5://proxy2.test:1080" {
		t.Fatal(rows)
	}
	cred, err := s.GetProxy(rows[0].ID)
	if err != nil || cred.URL != "http://alice:password-secret@proxy.test:8080" {
		t.Fatal("missing proxy")
	}
	for _, v := range []any{cred, &cred, rows, s.ListProxies(), s} {
		j, _ := json.Marshal(v)
		text := fmt.Sprintf("%v %+v %#v %s", v, v, v, j)
		if strings.Contains(text, "password-secret") || strings.Contains(text, "alice") || strings.Contains(text, "other-secret") {
			t.Fatal("credential leak")
		}
	}
	data, err := os.ReadFile(p)
	if err != nil || bytes.Contains(data, []byte("password-secret")) || bytes.Contains(data, []byte("proxy.test")) {
		t.Fatal("unencrypted file")
	}
	reopened, err := Open(p, testKey)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.ListProxies()
	if len(got) != 2 || got[0] != rows[0] || got[1] != rows[1] || len(reopened.List()) != 1 {
		t.Fatal("restart mismatch")
	}
	if err = reopened.DeleteProxy(rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if err = reopened.DeleteProxy(rows[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	reopened, err = Open(p, testKey)
	if err != nil || len(reopened.ListProxies()) != 1 || reopened.ListProxies()[0] != rows[1] {
		t.Fatal("delete not durable")
	}
}

func TestOldFileAndProxyAssignment(t *testing.T) {
	s, p := openTest(t)
	_, err := s.Import([]Candidate{candidate("old")})
	if err != nil {
		t.Fatal(err)
	}
	// Candidate-only version 1 files have no Proxies field.
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	if json.Unmarshal(data, &env) != nil {
		t.Fatal("invalid envelope")
	}
	plain, err := s.aead.Open(nil, env.Nonce, env.Data, []byte("perchsignupstore/v1"))
	if err != nil || bytes.Contains(plain, []byte("Proxies")) {
		t.Fatal("expected legacy payload")
	}
	s, err = Open(p, testKey)
	if err != nil || len(s.ListProxies()) != 0 {
		t.Fatal("legacy open")
	}
	ps, err := s.ImportProxies([]string{"http://u:p@one.test:80", "http://v:q@two.test:80"})
	if err != nil {
		t.Fatal(err)
	}
	c1, c2, c3 := candidate("new1"), candidate("new2"), candidate("new3")
	c1.ProxyURL = ""
	c2.ProxyURL = ""
	c3.ProxyURL = "http://custom.test:80"
	rows, err := s.ImportWithProxies([]Candidate{candidate("old"), c1, c2, c3})
	if err != nil || len(rows) != 3 {
		t.Fatal("assignment failed", err)
	}
	for i, want := range []string{"http://v:q@two.test:80", "http://u:p@one.test:80", "http://custom.test:80"} {
		r, e := s.Get(rows[i].ID)
		if e != nil || r.Candidate.ProxyURL != want {
			t.Fatalf("assignment %d", i)
		}
	}
	if err = s.DeleteProxy(ps[1].ID); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Get(rows[0].ID)
	if r.Candidate.ProxyURL != "http://v:q@two.test:80" {
		t.Fatal("assigned proxy removed")
	}
	s, err = Open(p, testKey)
	if err != nil || len(s.List()) != 4 || len(s.ListProxies()) != 1 {
		t.Fatal("reopen mismatch")
	}
}

func TestProxyInvalidAndRollback(t *testing.T) {
	s, p := openTest(t)
	for _, raw := range []string{"", "http://user:secret@host", "file://host:12", "http://u:p@host:80/?token=secret", "http://host:99999"} {
		if _, err := s.ImportProxies([]string{raw}); !errors.Is(err, ErrInvalid) || strings.Contains(fmt.Sprint(err), "secret") {
			t.Fatal("invalid proxy accepted")
		}
	}
	if _, err := s.ImportProxies([]string{"http://good.test:80", "bad"}); !errors.Is(err, ErrInvalid) || len(s.ListProxies()) != 0 {
		t.Fatal("partial import")
	}
	rows, err := s.ImportProxies([]string{"http://good.test:80"})
	if err != nil {
		t.Fatal(err)
	}
	original := s.path
	s.path = filepath.Join(p, "missing", "store")
	if _, err = s.ImportProxies([]string{"http://other.test:80"}); err == nil || len(s.ListProxies()) != 1 {
		t.Fatal("failed import changed memory")
	}
	if err = s.DeleteProxy(rows[0].ID); err == nil || len(s.ListProxies()) != 1 {
		t.Fatal("failed delete changed memory")
	}
	s.path = original
	s, err = Open(p, testKey)
	if err != nil || len(s.ListProxies()) != 1 {
		t.Fatal("disk changed")
	}
}

func TestClaimAssignsLateProxyAndResume(t *testing.T) {
	s, p := openTest(t)
	c := candidate("late")
	c.ProxyURL = ""
	rows, err := s.Import([]Candidate{c})
	if err != nil {
		t.Fatal(err)
	}
	// An empty inventory is the direct path, so the claim must not be blocked.
	r, err := s.Claim()
	if err != nil || r.Candidate.ProxyURL != "" {
		t.Fatalf("direct claim failed: %+v %v", r, err)
	}
	if err = s.Update(r.ID, Uncertain, []byte("session"), ""); err != nil {
		t.Fatal(err)
	}
	r, err = s.ResumeUncertain(rows[0].ID)
	if err != nil || r.Status != Running || r.Candidate.ProxyURL != "" {
		t.Fatalf("direct resume failed: %+v %v", r, err)
	}
	if err = s.Update(r.ID, Failed, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.Update(r.ID, Queued, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportProxies([]string{"http://user:secret@proxy.test:80"}); err != nil {
		t.Fatal(err)
	}
	r, err = s.Claim()
	if err != nil || r.Candidate.ProxyURL != "http://user:secret@proxy.test:80" {
		t.Fatal("late binding failed")
	}
	if err = s.Update(r.ID, Running, []byte("session"), ""); err != nil {
		t.Fatal(err)
	}
	s, err = Open(p, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Claim(); !errors.Is(err, ErrNoQueued) {
		t.Fatal(err)
	}
	r, err = s.ResumeUncertain(rows[0].ID)
	if err != nil || r.Status != Running || string(r.Session) != "session" {
		t.Fatal("resume failed")
	}
	if err = s.Update(r.ID, Ready, nil, ""); err != nil {
		t.Fatal(err)
	}
	if !s.List()[0].HasSession {
		t.Fatal("ready session presence missing")
	}
	if err = s.Update(r.ID, Completed, nil, ""); err != nil {
		t.Fatal(err)
	}
	if s.List()[0].HasSession {
		t.Fatal("completed session presence not cleared")
	}
	if _, err = s.ResumeUncertain(r.ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("ready resumed")
	}
}

func TestUncertainResumeWithoutProxyIsDirect(t *testing.T) {
	s, p := openTest(t)
	// Simulate an authenticated file written on the direct path.
	s.records = []diskRecord{{ID: "old", Candidate: diskCandidate{Login: "legacy"}, Status: Uncertain, Session: []byte("secret-session")}}
	if err := s.save(s.records, nil); err != nil {
		t.Fatal(err)
	}
	s, err := Open(p, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if !s.List()[0].HasSession {
		t.Fatal("session presence missing")
	}
	encoded, err := json.Marshal(s.List())
	if err != nil || strings.Contains(string(encoded), "secret-session") {
		t.Fatal("session leaked")
	}
	// Inventory must not be borrowed retroactively: the interrupted attempt was direct.
	if _, err = s.ImportProxies([]string{"http://proxy.test:80"}); err != nil {
		t.Fatal(err)
	}
	r, err := s.ResumeUncertain("old")
	if err != nil || r.Status != Running || r.Candidate.ProxyURL != "" || string(r.Session) != "secret-session" {
		t.Fatalf("direct resume: %+v %v", r, err)
	}
	// Reopening marks interrupted work uncertain again; the direct proxy stays empty.
	s, err = Open(p, testKey)
	if err != nil || s.List()[0].Status != Uncertain || s.List()[0].HasSession == false {
		t.Fatalf("resume not durable: %v", err)
	}
	if len(s.ListProxies()) != 1 {
		t.Fatal("inventory changed")
	}
}

func TestConcurrentProxyAndCandidateImports(t *testing.T) {
	s, _ := openTest(t)
	if _, err := s.ImportProxies([]string{"http://one.test:80"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := candidate(fmt.Sprintf("u-%d", i))
			c.ProxyURL = ""
			if _, err := s.ImportWithProxies([]Candidate{c}); err != nil {
				t.Error(err)
			}
			_, err := s.ImportProxies([]string{fmt.Sprintf("http://proxy-%d.test:80", i)})
			if err != nil {
				t.Error(err)
			}
			_ = s.ListProxies()
		}(i)
	}
	wg.Wait()
	if len(s.List()) != 20 || len(s.ListProxies()) != 21 {
		t.Fatal("lost update")
	}
}

// ClaimReady hands out only registered candidates, and ClaimByID only the chosen one.
func TestClaimReadyAndClaimByID(t *testing.T) {
	s, p := openTest(t)
	rows, err := s.Import([]Candidate{
		{Login: "one", Password: "p", TOTPSecret: "t", MailboxURL: "m", ProxyURL: "http://proxy.test:80"},
		{Login: "two", Password: "p", TOTPSecret: "t", MailboxURL: "m", ProxyURL: "http://proxy.test:80"},
	})
	if err != nil || len(rows) != 2 {
		t.Fatal("import", err)
	}
	if _, err = s.ClaimReady(); !errors.Is(err, ErrNoReady) {
		t.Fatal("queued candidate must not be claimable as ready")
	}
	// Register the second one ahead of time.
	r, err := s.ClaimByID(rows[1].ID)
	if err != nil || r.Candidate.Login != "two" || r.Status != Running {
		t.Fatalf("claim by id: %+v %v", r, err)
	}
	if got := s.List()[0].Status; got != Queued {
		t.Fatalf("unchosen candidate changed to %s", got)
	}
	if _, err = s.ClaimByID(rows[0].ID + "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown id claimed")
	}
	if _, err = s.ClaimByID(rows[1].ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("running candidate claimed again")
	}
	if err = s.Update(r.ID, Ready, []byte("session"), ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimReady()
	if err != nil || got.Candidate.Login != "two" || got.Status != Running || string(got.Session) != "session" {
		t.Fatalf("claim ready: %+v %v", got, err)
	}
	if _, err = s.ClaimReady(); !errors.Is(err, ErrNoReady) {
		t.Fatal("empty ready set claimed twice")
	}
	// The import completes from running.
	if err = s.Update(got.ID, Completed, nil, "acct"); err != nil {
		t.Fatal(err)
	}
	s, err = Open(p, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if list := s.List(); list[0].Status != Queued || list[1].Status != Completed {
		t.Fatalf("durable state %+v", list)
	}
}
