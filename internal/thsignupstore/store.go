// Package thsignupstore persists Token Harbor registration attempts.
//
// The file holds the credentials of every attempt — the mailbox password, the
// API key, the browser session — so it is written exactly like the Perch
// signup store: one AES-GCM envelope keyed from the routing secret, published
// atomically with a 0600 permission. Nothing in this package prints a
// credential; summaries carry a masked key and the mailbox address only.
//
// Attempts are calls of the registration machine. An attempt may register
// several mailboxes at once; each of them is a Row and shares the attempt's
// egress. Rows move queued → running → registered | imported | failed, and an
// interrupted run (the process died mid-flight) is rewritten to uncertain on
// open: claiming it again must be an explicit operator decision.
package thsignupstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
)

// Status is where one registration row stands.
type Status string

const (
	Queued     Status = "queued"
	Running    Status = "running"
	Registered Status = "registered"
	// Unverified is a registered account whose confirmation mail has not been
	// used yet. Its key answers /v1/models but refuses chat with
	// "email_verification_required", so it must not enter the pool as usable.
	Unverified Status = "unverified"
	Imported   Status = "imported"
	Failed     Status = "failed"
	Uncertain  Status = "uncertain"
)

var (
	ErrNotFound = errors.New("registration row not found")
	ErrInvalid  = errors.New("invalid registration row or transition")
	ErrCorrupt  = errors.New("cannot authenticate Token Harbor signup store")
)

// Cookie is one session cookie of a registered account.
type Cookie struct{ Name, Value string }

// Row is one mailbox's attempt. It carries credentials; JSON never shows them.
type Row struct {
	ID       string `json:"id"`
	Attempt  string `json:"attempt"`
	Email    string `json:"email"`
	Password string `json:"password,omitempty"`
	// MailboxURL is the iCloud HME receive page ("…/messages/<alias>?token=…").
	MailboxURL string `json:"mailboxUrl,omitempty"`
	// ProxyURL is the attempt's egress; empty means the settings default.
	ProxyURL string   `json:"proxyUrl,omitempty"`
	Status   Status   `json:"status"`
	Detail   string   `json:"detail,omitempty"`
	APIKey   string   `json:"apiKey,omitempty"`
	Account  string   `json:"accountId,omitempty"`
	Cookies  []Cookie `json:"cookies,omitempty"`
	// Group is the pool group the row joins when it is imported.
	Group string `json:"group,omitempty"`
	// Verified records that the confirmation mail was used; the key works
	// without it, but the site keeps asking for it.
	Verified   bool      `json:"verified,omitempty"`
	FreeModels bool      `json:"freeModels,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (Row) String() string               { return "[REDACTED]" }
func (Row) GoString() string             { return "[REDACTED]" }
func (Row) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Summary is the console's view: no password, no cookie, a masked key.
type Summary struct {
	ID         string    `json:"id"`
	Attempt    string    `json:"attempt"`
	Email      string    `json:"email"`
	Status     Status    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	Proxy      string    `json:"proxy"`
	Group      string    `json:"group,omitempty"`
	KeyMasked  string    `json:"keyMasked,omitempty"`
	HasMailbox bool      `json:"hasMailbox"`
	Verified   bool      `json:"verified"`
	FreeModels bool      `json:"freeModels"`
	AccountID  string    `json:"accountId,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// diskRow is Row's storage shape. Row deliberately marshals to a redacted
// string (so nothing that formats a row by accident can leak it), which means
// the file needs a plain twin to serialize.
type diskRow struct {
	ID         string    `json:"id"`
	Attempt    string    `json:"attempt"`
	Email      string    `json:"email"`
	Password   string    `json:"password,omitempty"`
	MailboxURL string    `json:"mailboxUrl,omitempty"`
	ProxyURL   string    `json:"proxyUrl,omitempty"`
	Status     Status    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	APIKey     string    `json:"apiKey,omitempty"`
	Account    string    `json:"accountId,omitempty"`
	Cookies    []Cookie  `json:"cookies,omitempty"`
	Group      string    `json:"group,omitempty"`
	Verified   bool      `json:"verified,omitempty"`
	FreeModels bool      `json:"freeModels,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (r Row) disk() diskRow {
	return diskRow{
		ID: r.ID, Attempt: r.Attempt, Email: r.Email, Password: r.Password,
		MailboxURL: r.MailboxURL, ProxyURL: r.ProxyURL, Status: r.Status,
		Detail: r.Detail, APIKey: r.APIKey, Account: r.Account,
		Cookies: r.Cookies, Group: r.Group, Verified: r.Verified,
		FreeModels: r.FreeModels, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (d diskRow) row() Row {
	return Row{
		ID: d.ID, Attempt: d.Attempt, Email: d.Email, Password: d.Password,
		MailboxURL: d.MailboxURL, ProxyURL: d.ProxyURL, Status: d.Status,
		Detail: d.Detail, APIKey: d.APIKey, Account: d.Account,
		Cookies: d.Cookies, Group: d.Group, Verified: d.Verified,
		FreeModels: d.FreeModels, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

type diskData struct {
	Version int       `json:"version"`
	Rows    []diskRow `json:"rows"`
}

type envelope struct {
	Version int    `json:"version"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
}

// Store is one file, one process. Methods are goroutine-safe.
type Store struct {
	mu   sync.Mutex
	path string
	aead cipher.AEAD
	rows []Row
}

func (*Store) String() string               { return "[REDACTED]" }
func (*Store) GoString() string             { return "[REDACTED]" }
func (*Store) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Open reads and authenticates the file; routingSecret must be >= 16 bytes.
// Rows left mid-flight become uncertain.
func Open(path string, routingSecret []byte) (*Store, error) {
	if path == "" || len(routingSecret) < 16 {
		return nil, ErrInvalid
	}
	h := hmac.New(sha256.New, routingSecret)
	h.Write([]byte("account-pool-balancer/thsignupstore/v1"))
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
	plain, err := aead.Open(nil, e.Nonce, e.Data, []byte("thsignupstore/v1"))
	if err != nil {
		return nil, ErrCorrupt
	}
	var data diskData
	if json.Unmarshal(plain, &data) != nil || data.Version != 1 {
		return nil, ErrCorrupt
	}
	rows := make([]Row, len(data.Rows))
	for i, d := range data.Rows {
		rows[i] = d.row()
	}
	if !validRows(rows) {
		return nil, ErrCorrupt
	}
	s.rows = rows
	dirty := false
	for i := range s.rows {
		if s.rows[i].Status == Running {
			s.rows[i].Status = Uncertain
			s.rows[i].Detail = "进程中断，未完成"
			s.rows[i].UpdatedAt = time.Now()
			dirty = true
		}
	}
	if dirty {
		if err := s.save(s.rows); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func validStatus(s Status) bool {
	switch s {
	case Queued, Running, Registered, Unverified, Imported, Failed, Uncertain:
		return true
	}
	return false
}

func validRows(rs []Row) bool {
	ids, emails := map[string]bool{}, map[string]bool{}
	for _, r := range rs {
		key := strings.ToLower(strings.TrimSpace(r.Email))
		if r.ID == "" || ids[r.ID] || key == "" || emails[key] || !validStatus(r.Status) {
			return false
		}
		ids[r.ID], emails[key] = true, true
	}
	return true
}

// Add inserts rows from one attempt. A mailbox that already has a row is
// returned as is and not reset: re-submitting a list must not silently
// re-register an account that is already in the pool.
func (s *Store) Add(attempt string, rows []Row) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range s.rows {
		seen[strings.ToLower(strings.TrimSpace(r.Email))] = true
	}
	next := append([]Row(nil), s.rows...)
	now := time.Now()
	out := make([]Summary, 0, len(rows))
	for _, r := range rows {
		email := strings.ToLower(strings.TrimSpace(r.Email))
		if email == "" || strings.Contains(email, " ") {
			return nil, ErrInvalid
		}
		if seen[email] {
			for _, old := range next {
				if strings.EqualFold(old.Email, email) {
					out = append(out, SummaryOf(old))
					break
				}
			}
			continue
		}
		seen[email] = true
		nr := r
		nr.Email = email
		nr.Attempt = attempt
		nr.Status = Queued
		nr.Detail = ""
		nr.Cookies = nil
		nr.CreatedAt, nr.UpdatedAt = now, now
		if nr.ID == "" {
			nr.ID = config.NewID("thr_")
		}
		next = append(next, nr)
		out = append(out, SummaryOf(nr))
	}
	if err := s.save(next); err != nil {
		return nil, err
	}
	s.rows = next
	return out, nil
}

// List returns every row, newest attempt first.
func (s *Store) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, SummaryOf(r))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Get returns the full row for trusted callers (the registrar).
func (s *Store) Get(id string) (Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.ID == id {
			return cloneRow(r), nil
		}
	}
	return Row{}, ErrNotFound
}

// Update applies fn to one row atomically with persistence.
func (s *Store) Update(id string, fn func(*Row) error) (Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rows {
		if r.ID != id {
			continue
		}
		next := append([]Row(nil), s.rows...)
		row := cloneRow(next[i])
		if err := fn(&row); err != nil {
			return Row{}, err
		}
		row.ID = id
		row.UpdatedAt = time.Now()
		next[i] = row
		if err := s.save(next); err != nil {
			return Row{}, err
		}
		s.rows = next
		return cloneRow(row), nil
	}
	return Row{}, ErrNotFound
}

// SetProxy records a new egress before the attempt starts.
func (s *Store) SetProxy(id, proxyURL string) (Row, error) {
	return s.Update(id, func(r *Row) error {
		if r.Status != Queued {
			return ErrInvalid
		}
		r.ProxyURL = strings.TrimSpace(proxyURL)
		return nil
	})
}

// Claim atomically moves one queued row to running.
func (s *Store) Claim(id string) (Row, error) {
	return s.Update(id, func(r *Row) error {
		if r.Status != Queued {
			return ErrInvalid
		}
		r.Status = Running
		r.Detail = ""
		return nil
	})
}

// Retry requeues a finished row so it can be registered again.
func (s *Store) Retry(id string) (Row, error) {
	return s.Update(id, func(r *Row) error {
		switch r.Status {
		case Failed, Uncertain, Registered, Unverified:
		default:
			return ErrInvalid
		}
		r.Status = Queued
		r.Detail = ""
		r.APIKey = ""
		r.Cookies = nil
		r.Account = ""
		return nil
	})
}

// Delete removes a row that is not being worked on.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rows {
		if r.ID != id {
			continue
		}
		if r.Status == Running {
			return ErrInvalid
		}
		next := make([]Row, 0, len(s.rows)-1)
		next = append(next, s.rows[:i]...)
		next = append(next, s.rows[i+1:]...)
		if err := s.save(next); err != nil {
			return err
		}
		s.rows = next
		return nil
	}
	return ErrNotFound
}

// Summaries groups the console view by attempt, newest first.
func (s *Store) Summaries() map[string][]Summary {
	out := map[string][]Summary{}
	for _, r := range s.List() {
		out[r.Attempt] = append(out[r.Attempt], r)
	}
	return out
}

// SummaryOf is the credential-free view of one row.
func SummaryOf(r Row) Summary {
	return Summary{
		ID: r.ID, Attempt: r.Attempt, Email: r.Email, Status: r.Status, Detail: r.Detail,
		Proxy:      ProxyLabel(r.ProxyURL),
		Group:      r.Group,
		KeyMasked:  MaskKey(r.APIKey),
		HasMailbox: r.MailboxURL != "",
		Verified:   r.Verified, FreeModels: r.FreeModels, AccountID: r.Account,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// MaskKey shows the shape of a key and its last four characters.
func MaskKey(k string) string {
	k = strings.TrimSpace(k)
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "••••"
	}
	return k[:len(config.TokenHarborKeyPrefix)+4] + "…" + k[len(k)-4:]
}

// ProxyLabel names an egress without its credentials.
func ProxyLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "直连"
	}
	u, err := parseProxy(raw)
	if err != nil || u == "" {
		return "代理地址无效"
	}
	return u
}

func (s *Store) save(rows []Row) error {
	return s.write(rows, "thsignupstore/v1")
}

func cloneRow(r Row) Row {
	r.Cookies = append([]Cookie(nil), r.Cookies...)
	return r
}

func (s *Store) write(rows []Row, aad string) error {
	disk := make([]diskRow, len(rows))
	for i, r := range rows {
		disk[i] = r.disk()
	}
	plain, err := json.Marshal(diskData{Version: 1, Rows: disk})
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	b, err := json.Marshal(envelope{Version: 1, Nonce: nonce, Data: s.aead.Seal(nil, nonce, plain, []byte(aad))})
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".thsignup-*")
	if err != nil {
		return fmt.Errorf("create signup store: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0o600); err == nil {
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
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
