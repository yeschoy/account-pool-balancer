// Package perchsignupstore persists registration candidates independently of sessions.
// A path must be owned by one Store in one process; methods are goroutine-safe.
package perchsignupstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var (
	ErrNoQueued = errors.New("no queued candidate")
	ErrNoProxy  = errors.New("no proxy available for queued candidate")
	ErrNotFound = errors.New("candidate not found")
	ErrInvalid  = errors.New("invalid candidate or transition")
	ErrCorrupt  = errors.New("cannot authenticate signup store")
)

type Status string

const (
	Queued    Status = "queued"
	Running   Status = "running"
	Ready     Status = "ready"
	Completed Status = "completed"
	Failed    Status = "failed"
	Uncertain Status = "uncertain"
)

// Candidate contains sensitive material; JSON and formatted output redact it.
type Candidate struct{ Login, Password, TOTPSecret, MailboxURL, ProxyURL string }

func (Candidate) String() string               { return "[REDACTED]" }
func (Candidate) GoString() string             { return "[REDACTED]" }
func (Candidate) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Record is only for trusted worker code. Its serialized and formatted form is redacted.
type Record struct {
	ID        string
	Candidate Candidate
	Status    Status
	Session   []byte
	AccountID string
}

func (Record) String() string               { return "[REDACTED]" }
func (Record) GoString() string             { return "[REDACTED]" }
func (Record) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Summary exposes non-secret candidate metadata and session presence.
type Summary struct {
	ID         string `json:"id"`
	Status     Status `json:"status"`
	Login      string `json:"login"`
	Proxy      string `json:"proxy"`
	HasSession bool   `json:"hasSession"`
}

// ProxyRecord is returned only to trusted code; JSON and formatting redact its URL.
type ProxyRecord struct{ ID, URL string }

func (ProxyRecord) String() string               { return "[REDACTED]" }
func (ProxyRecord) GoString() string             { return "[REDACTED]" }
func (ProxyRecord) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// ProxySummary contains a generated ID and a credential-free proxy address.
type ProxySummary struct {
	ID    string `json:"id"`
	Proxy string `json:"proxy"`
}
type diskProxy struct{ ID, URL string }

type diskCandidate struct{ Login, Password, TOTPSecret, MailboxURL, ProxyURL string }
type diskRecord struct {
	ID        string
	Candidate diskCandidate
	Status    Status
	Session   []byte
	AccountID string
}
type diskData struct {
	Version int
	Records []diskRecord
	Proxies []diskProxy `json:",omitempty"`
}
type envelope struct {
	Version int    `json:"version"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
}

type Store struct {
	mu      sync.Mutex
	path    string
	aead    cipher.AEAD
	records []diskRecord
	proxies []diskProxy
}

func (*Store) String() string               { return "[REDACTED]" }
func (*Store) GoString() string             { return "[REDACTED]" }
func (*Store) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Open reads and authenticates the file, marking interrupted work uncertain.
// routingSecret must be the decoded routing secret, at least 16 bytes long.
func Open(path string, routingSecret []byte) (*Store, error) {
	if path == "" || len(routingSecret) < 16 {
		return nil, ErrInvalid
	}
	h := hmac.New(sha256.New, routingSecret)
	h.Write([]byte("account-pool-balancer/perchsignupstore/v1"))
	block, err := aes.NewCipher(h.Sum(nil))
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, aead: aead}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var e envelope
	if json.Unmarshal(b, &e) != nil || e.Version != 1 || len(e.Nonce) != aead.NonceSize() {
		return nil, ErrCorrupt
	}
	plain, err := aead.Open(nil, e.Nonce, e.Data, []byte("perchsignupstore/v1"))
	if err != nil {
		return nil, ErrCorrupt
	}
	var data diskData
	if json.Unmarshal(plain, &data) != nil || data.Version != 1 || !validRecords(data.Records) || !validProxies(data.Proxies) {
		return nil, ErrCorrupt
	}
	s.records = data.Records
	s.proxies = data.Proxies
	dirty := false
	for i := range s.records {
		if s.records[i].Status == Running {
			s.records[i].Status = Uncertain
			dirty = true
		}
	}
	if dirty {
		if err := s.save(s.records, s.proxies); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func validStatus(s Status) bool {
	return s == Queued || s == Running || s == Ready || s == Completed || s == Failed || s == Uncertain
}
func loginKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
func validRecords(rs []diskRecord) bool {
	ids, logins := map[string]bool{}, map[string]bool{}
	for _, r := range rs {
		key := loginKey(r.Candidate.Login)
		if r.ID == "" || ids[r.ID] || key == "" || logins[key] || !validStatus(r.Status) {
			return false
		}
		ids[r.ID], logins[key] = true, true
	}
	return true
}

func proxyAddress(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.Port() == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", false
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", false
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

func validProxies(ps []diskProxy) bool {
	ids, urls := map[string]bool{}, map[string]bool{}
	for _, p := range ps {
		_, ok := proxyAddress(p.URL)
		if !ok || p.ID == "" || ids[p.ID] || urls[p.URL] || strings.TrimSpace(p.URL) != p.URL {
			return false
		}
		ids[p.ID], urls[p.URL] = true, true
	}
	return true
}

var publicLogin = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func summary(r diskRecord) Summary {
	// Strip credentials, path, query, and fragment from the proxy URL.
	proxy := ""
	if u, err := url.Parse(r.Candidate.ProxyURL); err == nil && u.Scheme != "" && u.Hostname() != "" {
		proxy = u.Scheme + "://" + u.Host
	}
	login := "[REDACTED]"
	if publicLogin.MatchString(r.Candidate.Login) {
		login = r.Candidate.Login
	}
	return Summary{ID: r.ID, Status: r.Status, Login: login, Proxy: proxy, HasSession: len(r.Session) != 0}
}
func record(r diskRecord) Record {
	return Record{ID: r.ID, Candidate: Candidate(r.Candidate), Status: r.Status, Session: append([]byte(nil), r.Session...), AccountID: r.AccountID}
}

// Import adds previously unseen logins atomically; existing entries are unchanged.
func (s *Store) Import(cs []Candidate) ([]Summary, error) { return s.importCandidates(cs, false) }

// ImportWithProxies assigns inventory proxies in round-robin order to newly
// imported candidates without a proxy, atomically with inventory changes.
func (s *Store) ImportWithProxies(cs []Candidate) ([]Summary, error) {
	return s.importCandidates(cs, true)
}

func (s *Store) importCandidates(cs []Candidate, assign bool) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := append([]diskRecord(nil), s.records...)
	seen := make(map[string]bool, len(rs))
	for _, r := range rs {
		seen[loginKey(r.Candidate.Login)] = true
	}
	var added []Summary
	for _, c := range cs {
		c.Login = strings.TrimSpace(c.Login)
		key := loginKey(c.Login)
		if key == "" || c.Password == "" || c.TOTPSecret == "" || c.MailboxURL == "" {
			return nil, ErrInvalid
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		if assign && c.ProxyURL == "" && len(s.proxies) > 0 {
			c.ProxyURL = s.proxies[len(rs)%len(s.proxies)].URL
		}
		id := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, id); err != nil {
			return nil, err
		}
		r := diskRecord{ID: hex.EncodeToString(id), Candidate: diskCandidate(c), Status: Queued}
		rs = append(rs, r)
		added = append(added, summary(r))
	}
	if len(added) > 0 {
		if err := s.save(rs, s.proxies); err != nil {
			return nil, err
		}
		s.records = rs
	}
	return added, nil
}

func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, summary(r))
	}
	return out
}
func (s *Store) Get(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.records {
		if r.ID == id {
			return record(r), nil
		}
	}
	return Record{}, ErrNotFound
}

// Delete removes a non-running candidate atomically.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.records {
		if r.ID != id {
			continue
		}
		if r.Status == Running {
			return ErrInvalid
		}
		rs := make([]diskRecord, 0, len(s.records)-1)
		rs = append(rs, s.records[:i]...)
		rs = append(rs, s.records[i+1:]...)
		if err := s.save(rs, s.proxies); err != nil {
			return err
		}
		s.records = rs
		return nil
	}
	return ErrNotFound
}

// ImportProxies atomically appends valid, previously unseen proxy URLs in order.
// Duplicates are compared after trimming surrounding whitespace.
func (s *Store) ImportProxies(rawURLs []string) ([]ProxySummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ps := append([]diskProxy(nil), s.proxies...)
	seen := make(map[string]bool, len(ps))
	for _, p := range ps {
		seen[p.URL] = true
	}
	var added []ProxySummary
	for _, raw := range rawURLs {
		raw = strings.TrimSpace(raw)
		address, ok := proxyAddress(raw)
		if !ok {
			return nil, ErrInvalid
		}
		if seen[raw] {
			continue
		}
		seen[raw] = true
		id := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, id); err != nil {
			return nil, err
		}
		p := diskProxy{ID: hex.EncodeToString(id), URL: raw}
		ps = append(ps, p)
		added = append(added, ProxySummary{ID: p.ID, Proxy: address})
	}
	if len(added) != 0 {
		if err := s.save(s.records, ps); err != nil {
			return nil, err
		}
		s.proxies = ps
	}
	return added, nil
}

// ListProxies returns inventory in stable insertion order without credentials.
func (s *Store) ListProxies() []ProxySummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ProxySummary, 0, len(s.proxies))
	for _, p := range s.proxies {
		addr, _ := proxyAddress(p.URL)
		out = append(out, ProxySummary{ID: p.ID, Proxy: addr})
	}
	return out
}

// GetProxy returns the complete proxy URL to trusted callers.
func (s *Store) GetProxy(id string) (ProxyRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.proxies {
		if p.ID == id {
			return ProxyRecord{ID: p.ID, URL: p.URL}, nil
		}
	}
	return ProxyRecord{}, ErrNotFound
}

// DeleteProxy removes inventory without changing previously assigned candidates.
func (s *Store) DeleteProxy(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.proxies {
		if p.ID != id {
			continue
		}
		ps := make([]diskProxy, 0, len(s.proxies)-1)
		ps = append(ps, s.proxies[:i]...)
		ps = append(ps, s.proxies[i+1:]...)
		if err := s.save(s.records, ps); err != nil {
			return err
		}
		s.proxies = ps
		return nil
	}
	return ErrNotFound
}

// Claim atomically persists queued -> running before returning credentials.
func (s *Store) Claim() (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.records {
		if r.Status == Queued {
			rs := append([]diskRecord(nil), s.records...)
			if rs[i].Candidate.ProxyURL == "" {
				if len(s.proxies) == 0 {
					return Record{}, ErrNoProxy
				}
				rs[i].Candidate.ProxyURL = s.proxies[i%len(s.proxies)].URL
			}
			rs[i].Status = Running
			if err := s.save(rs, s.proxies); err != nil {
				return Record{}, err
			}
			s.records = rs
			return record(rs[i]), nil
		}
	}
	return Record{}, ErrNoQueued
}

// ResumeUncertain explicitly claims an interrupted session for reconciliation.
// It never starts a fresh registration and requires an existing session.
func (s *Store) ResumeUncertain(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.records {
		if r.ID != id {
			continue
		}
		if r.Status != Uncertain || len(r.Session) == 0 {
			return Record{}, ErrInvalid
		}
		if r.Candidate.ProxyURL == "" {
			return Record{}, ErrNoProxy
		}
		rs := append([]diskRecord(nil), s.records...)
		rs[i].Status = Running
		if err := s.save(rs, s.proxies); err != nil {
			return Record{}, err
		}
		s.records = rs
		return record(rs[i]), nil
	}
	return Record{}, ErrNotFound
}

// Update persists a running checkpoint or advances to ready or a terminal state.
// Failed or uncertain work can be explicitly requeued. A non-nil session
// replaces the stored session, and a nonempty accountID replaces the account ID.
// Ready requires a session; Completed clears the session and signup credentials.
func (s *Store) Update(id string, status Status, session []byte, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.records {
		if r.ID == id {
			if !(r.Status == Running && (status == Running || status == Ready || status == Completed || status == Failed || status == Uncertain) || r.Status == Ready && (status == Completed || status == Failed || status == Uncertain) || (r.Status == Failed || r.Status == Uncertain) && status == Queued) {
				return ErrInvalid
			}
			rs := append([]diskRecord(nil), s.records...)
			rs[i].Status = status
			if session != nil {
				rs[i].Session = append([]byte(nil), session...)
			}
			if accountID != "" {
				rs[i].AccountID = accountID
			}
			if status == Ready && len(rs[i].Session) == 0 {
				return ErrInvalid
			}
			if status == Completed {
				rs[i].Session = nil
				rs[i].Candidate.Password = ""
				rs[i].Candidate.TOTPSecret = ""
				rs[i].Candidate.MailboxURL = ""
			}
			if err := s.save(rs, s.proxies); err != nil {
				return err
			}
			s.records = rs
			return nil
		}
	}
	return ErrNotFound
}

// save publishes only after the temporary file is fully written and synced.
func (s *Store) save(rs []diskRecord, ps []diskProxy) error {
	plain, err := json.Marshal(diskData{Version: 1, Records: rs, Proxies: ps})
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	b, err := json.Marshal(envelope{Version: 1, Nonce: nonce, Data: s.aead.Seal(nil, nonce, plain, []byte("perchsignupstore/v1"))})
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, ".perchsignup-*")
	if err != nil {
		return fmt.Errorf("create signup store: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write signup store: %w", err)
	}
	if err = os.Rename(name, s.path); err != nil {
		return fmt.Errorf("publish signup store: %w", err)
	}
	// Rename is the commit point: directory sync failures cannot safely be rolled back.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
