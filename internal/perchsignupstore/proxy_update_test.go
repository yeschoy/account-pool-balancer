package perchsignupstore

import (
	"errors"
	"strings"
	"testing"
)

// TestUpdateProxyKeepsCandidatesAndID covers the contract the console relies
// on: editing inventory rewrites the entry in place, keeps its ID, and leaves
// already-bound candidates pointing where they were registered.
func TestUpdateProxyKeepsCandidatesAndID(t *testing.T) {
	s, p := openTest(t)
	ps, err := s.ImportProxies([]string{"http://old.example:8080"})
	if err != nil || len(ps) != 1 {
		t.Fatal(err)
	}
	id := ps[0].ID
	rows, err := s.ImportWithProxies([]Candidate{{Login: "bound", Password: "pw", TOTPSecret: "totp", MailboxURL: "mail"}})
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	got, err := s.UpdateProxy(id, " socks5://user:newsecret@new.example:1080 ")
	if err != nil || got.ID != id || got.Proxy != "socks5://new.example:1080" {
		t.Fatalf("update: %+v %v", got, err)
	}
	// credentials must never surface through the redacted summary
	if strings.Contains(got.Proxy, "newsecret") || strings.Contains(got.Proxy, "user") {
		t.Fatal("update leaked credentials")
	}
	rec, err := s.Get(rows[0].ID)
	if err != nil || rec.Candidate.ProxyURL != "http://old.example:8080" {
		t.Fatalf("bound candidate changed: %+v %v", rec, err)
	}
	reopened, err := Open(p, testKey)
	if err != nil {
		t.Fatal(err)
	}
	list := reopened.ListProxies()
	if len(list) != 1 || list[0].ID != id || list[0].Proxy != "socks5://new.example:1080" {
		t.Fatalf("restart: %+v", list)
	}
	if rec, _ := reopened.Get(rows[0].ID); rec.Candidate.ProxyURL != "http://old.example:8080" {
		t.Fatal("bound candidate changed after restart")
	}
}

func TestUpdateProxyRejectsInvalidDuplicateAndUnknown(t *testing.T) {
	s, _ := openTest(t)
	ps, err := s.ImportProxies([]string{"http://one.example:8080", "http://two.example:8080"})
	if err != nil || len(ps) != 2 {
		t.Fatal(err)
	}
	for _, raw := range []string{"", "http://user:secret@nodots", "file://host:12", "http://u:p@host:80/?token=secret", "http://host:99999"} {
		_, err := s.UpdateProxy(ps[0].ID, raw)
		if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid %q: %v", raw, err)
		}
	}
	// the same endpoint is the duplicate even behind other credentials
	if _, err := s.UpdateProxy(ps[0].ID, "http://other:creds@two.example:8080"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate endpoint accepted: %v", err)
	}
	if _, err := s.UpdateProxy("missing", "http://three.example:8080"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	// a rejected edit leaves both entries exactly as they were
	if list := s.ListProxies(); len(list) != 2 || list[0] != ps[0] || list[1] != ps[1] {
		t.Fatalf("inventory changed after refusal: %+v", list)
	}
	// re-saving an entry with its own address is not a self-duplicate
	if _, err := s.UpdateProxy(ps[0].ID, "http://one.example:8080"); err != nil {
		t.Fatalf("self duplicate: %v", err)
	}
}

func TestImportProxiesDeduplicatesByEndpoint(t *testing.T) {
	s, _ := openTest(t)
	added, err := s.ImportProxies([]string{"http://alice:one@same.example:8080", "http://bob:two@same.example:8080"})
	if err != nil || len(added) != 1 {
		t.Fatalf("endpoint dedupe: %+v %v", added, err)
	}
}

func TestUpdateCandidateProxy(t *testing.T) {
	s, p := openTest(t)
	rows, err := s.Import([]Candidate{{Login: "a", Password: "pw", TOTPSecret: "totp", MailboxURL: "mail"}})
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	id := rows[0].ID
	// proxyAddress is the inventory contract: it accepts socks5h and keeps the
	// scheme, dropping only userinfo, path and query.
	got, err := s.UpdateCandidateProxy(id, " socks5h://user:candsecret@cand.example:1080/ ")
	if err != nil || got.Proxy != "socks5h://cand.example:1080" {
		t.Fatalf("set: %+v %v", got, err)
	}
	if strings.Contains(got.Proxy, "candsecret") || strings.Contains(got.Proxy, "user:") {
		t.Fatalf("summary leaked: %+v", got)
	}
	reopened, err := Open(p, testKey)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := reopened.Get(id)
	if err != nil || rec.Candidate.ProxyURL != "socks5h://cand.example:1080" {
		t.Fatalf("restart: %+v %v", rec, err)
	}
	// an empty value is the direct path, not a missing one
	if got, err = reopened.UpdateCandidateProxy(id, ""); err != nil || got.Proxy != "" {
		t.Fatalf("clear: %+v %v", got, err)
	}
	rec, _ = reopened.Get(id)
	if rec.Candidate.ProxyURL != "" {
		t.Fatal("clear not durable")
	}
	if _, err := reopened.UpdateCandidateProxy(id, "http://host:notaport"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid proxy: %v", err)
	}
	if _, err := reopened.UpdateCandidateProxy("missing", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

// TestUpdateCandidateProxyRefusesRunningAndCompleted pins the states where the
// proxy can no longer be meaningfully changed.
func TestUpdateCandidateProxyRefusesRunningAndCompleted(t *testing.T) {
	s, _ := openTest(t)
	rows, err := s.Import([]Candidate{{Login: "a", Password: "pw", TOTPSecret: "totp", MailboxURL: "mail"}})
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	id := rows[0].ID
	if _, err := s.Claim(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateCandidateProxy(id, "http://new.example:8080"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("running accepted: %v", err)
	}
	if err := s.Update(id, Ready, []byte("session"), ""); err != nil {
		t.Fatal(err)
	}
	// ready is still editable: the candidate has not been handed to a worker
	if _, err := s.UpdateCandidateProxy(id, "http://new.example:8080"); err != nil {
		t.Fatalf("ready refused: %v", err)
	}
	if err := s.Update(id, Completed, nil, "acct"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateCandidateProxy(id, "http://other.example:8080"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("completed accepted: %v", err)
	}
}
