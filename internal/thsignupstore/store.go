// Package thsignupstore persists Token Harbor registration attempts.
//
// The file holds the credentials of every attempt — the mailbox password, the
// API key, the browser session — so it is written exactly like the Perch
// signup store: legacy email rows use an AES-GCM envelope keyed from the
// routing secret. Explicitly opted-in GitHub rows are plaintext alongside it.
// Both are published atomically with 0600 permissions. Nothing prints a
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
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thregister"
)

// Status is where one registration row stands.
type Status string

const (
	Saved      Status = "saved"
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

// Cookie is one session cookie of a registered account. Old files containing
// only Name/Value remain readable; new snapshots preserve replay attributes.
type Cookie struct {
	Name, Value      string
	Domain, Path     string
	Expires          time.Time
	Secure, HTTPOnly bool
	SameSite         int
}

// Checkout is a sensitive hosted payment session, stored only in the encrypted
// file and revealed through the explicit administrator checkout endpoint.
type Checkout struct {
	Kind      string    `json:"kind"`
	Product   string    `json:"product"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"createdAt"`
}

func (Checkout) String() string   { return "[REDACTED]" }
func (Checkout) GoString() string { return "[REDACTED]" }

// GitHubAuth keeps the authorization identity and its independent IPv4 route.
// GitHub rows are deliberately stored as plaintext (0600), by operator choice.
type GitHubAuth struct {
	Login                string   `json:"login"`
	TOTPSecret           string   `json:"totpSecret,omitempty"`
	ProxyURL             string   `json:"proxyUrl,omitempty"`
	Cookies              []Cookie `json:"cookies,omitempty"`
	ProviderToken        string   `json:"providerToken,omitempty"`
	ProviderRefreshToken string   `json:"providerRefreshToken,omitempty"`
	OAuthComplete        bool     `json:"oauthComplete,omitempty"`
}

func (GitHubAuth) String() string   { return "[REDACTED]" }
func (GitHubAuth) GoString() string { return "[REDACTED]" }

// Row is one mailbox's attempt. It carries credentials; JSON never shows them.
type Row struct {
	ID       string      `json:"id"`
	Attempt  string      `json:"attempt"`
	Email    string      `json:"email"`
	Password string      `json:"password,omitempty"`
	GitHub   *GitHubAuth `json:"github,omitempty"`
	// MailboxURL is an iCloud HME or MailCat shared receive page.
	MailboxURL string `json:"mailboxUrl,omitempty"`
	// ProxyBound distinguishes an explicitly bound direct route from legacy
	// rows whose empty URL still inherits the settings default.
	ProxyURL     string                  `json:"proxyUrl,omitempty"`
	ProxyBound   bool                    `json:"proxyBound,omitempty"`
	Status       Status                  `json:"status"`
	Detail       string                  `json:"detail,omitempty"`
	APIKey       string                  `json:"apiKey,omitempty"`
	Account      string                  `json:"accountId,omitempty"`
	Cookies      []Cookie                `json:"cookies,omitempty"`
	Checkout     *Checkout               `json:"checkout,omitempty"`
	Subscription thregister.Subscription `json:"subscription"`
	// Group is the pool group the row joins when it is imported.
	Group string `json:"group,omitempty"`
	// Verified records that the confirmation mail was used. Metadata access
	// alone does not establish verification or paid inference authorization.
	Verified   bool      `json:"verified,omitempty"`
	FreeModels bool      `json:"freeModels,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Egress never changes a new row's saved binding when defaults change.
func (r Row) Egress(fallback string) string {
	if r.ProxyBound || r.ProxyURL != "" {
		return r.ProxyURL
	}
	return fallback
}

func (Row) String() string               { return "[REDACTED]" }
func (Row) GoString() string             { return "[REDACTED]" }
func (Row) MarshalJSON() ([]byte, error) { return []byte(`"[REDACTED]"`), nil }

// Summary is the console's view: no password, no cookie, a masked key.
type Summary struct {
	ID               string                  `json:"id"`
	Attempt          string                  `json:"attempt"`
	Email            string                  `json:"email"`
	Status           Status                  `json:"status"`
	Detail           string                  `json:"detail,omitempty"`
	Proxy            string                  `json:"proxy"`
	Group            string                  `json:"group,omitempty"`
	KeyMasked        string                  `json:"keyMasked,omitempty"`
	HasMailbox       bool                    `json:"hasMailbox"`
	HasSession       bool                    `json:"hasSession"`
	AuthProvider     string                  `json:"authProvider,omitempty"`
	GitHubLogin      string                  `json:"githubLogin,omitempty"`
	GitHubProxy      string                  `json:"githubProxy,omitempty"`
	HasGitHubSession bool                    `json:"hasGithubSession,omitempty"`
	HasCheckout      bool                    `json:"hasCheckout"`
	Subscription     thregister.Subscription `json:"subscription"`
	Verified         bool                    `json:"verified"`
	FreeModels       bool                    `json:"freeModels"`
	AccountID        string                  `json:"accountId,omitempty"`
	CreatedAt        time.Time               `json:"createdAt"`
	UpdatedAt        time.Time               `json:"updatedAt"`
}

// diskRow is Row's storage shape. Row deliberately marshals to a redacted
// string (so nothing that formats a row by accident can leak it), which means
// the file needs a plain twin to serialize.
type diskRow struct {
	ID           string                  `json:"id"`
	Attempt      string                  `json:"attempt"`
	Email        string                  `json:"email"`
	Password     string                  `json:"password,omitempty"`
	GitHub       *GitHubAuth             `json:"github,omitempty"`
	MailboxURL   string                  `json:"mailboxUrl,omitempty"`
	ProxyURL     string                  `json:"proxyUrl,omitempty"`
	ProxyBound   bool                    `json:"proxyBound,omitempty"`
	Status       Status                  `json:"status"`
	Detail       string                  `json:"detail,omitempty"`
	APIKey       string                  `json:"apiKey,omitempty"`
	Account      string                  `json:"accountId,omitempty"`
	Cookies      []Cookie                `json:"cookies,omitempty"`
	Checkout     *Checkout               `json:"checkout,omitempty"`
	Subscription thregister.Subscription `json:"subscription"`
	Group        string                  `json:"group,omitempty"`
	Verified     bool                    `json:"verified,omitempty"`
	FreeModels   bool                    `json:"freeModels,omitempty"`
	CreatedAt    time.Time               `json:"createdAt"`
	UpdatedAt    time.Time               `json:"updatedAt"`
}

func (r Row) disk() diskRow {
	return diskRow{
		ID: r.ID, Attempt: r.Attempt, Email: r.Email, Password: r.Password, GitHub: r.GitHub,
		MailboxURL: r.MailboxURL, ProxyURL: r.ProxyURL, ProxyBound: r.ProxyBound, Status: r.Status,
		Detail: r.Detail, APIKey: r.APIKey, Account: r.Account,
		Cookies: r.Cookies, Checkout: r.Checkout, Subscription: r.Subscription, Group: r.Group, Verified: r.Verified,
		FreeModels: r.FreeModels, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (d diskRow) row() Row {
	return Row{
		ID: d.ID, Attempt: d.Attempt, Email: d.Email, Password: d.Password, GitHub: d.GitHub,
		MailboxURL: d.MailboxURL, ProxyURL: d.ProxyURL, ProxyBound: d.ProxyBound, Status: d.Status,
		Detail: d.Detail, APIKey: d.APIKey, Account: d.Account,
		Cookies: d.Cookies, Checkout: d.Checkout, Subscription: d.Subscription, Group: d.Group, Verified: d.Verified,
		FreeModels: d.FreeModels, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
	}
}

// MaxAttemptNoteLength bounds administrator-authored task notes, in Unicode characters.
const MaxAttemptNoteLength = 200

type diskData struct {
	Version      int               `json:"version"`
	Rows         []diskRow         `json:"rows"`
	Proxies      []string          `json:"proxies,omitempty"`
	AttemptNotes map[string]string `json:"attemptNotes,omitempty"`
}

type envelope struct {
	Version int    `json:"version"`
	Nonce   []byte `json:"nonce"`
	Data    []byte `json:"data"`
	// Do not migrate/decrypt legacy email rows. Only new GitHub rows opt into
	// plaintext; one atomic file publication still covers both row kinds.
	GitHubRows []diskRow `json:"githubRows,omitempty"`
}

// Store is one file, one process. Methods are goroutine-safe.
type Store struct {
	mu      sync.Mutex
	path    string
	aead    cipher.AEAD
	rows    []Row
	proxies []string
	// Notes are task metadata, separate from row UpdatedAt/source guards.
	attemptNotes map[string]string
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
	rows := make([]Row, 0, len(data.Rows)+len(e.GitHubRows))
	for _, d := range data.Rows {
		if d.GitHub != nil {
			return nil, ErrCorrupt
		}
		rows = append(rows, d.row())
	}
	for _, d := range e.GitHubRows {
		if d.GitHub == nil || d.GitHub.Login == "" {
			return nil, ErrCorrupt
		}
		rows = append(rows, d.row())
	}
	if !validRows(rows) {
		return nil, ErrCorrupt
	}
	s.rows = rows
	for _, proxy := range data.Proxies {
		if err := ValidateProxy(proxy); err != nil {
			return nil, ErrCorrupt
		}
	}
	s.proxies = append([]string(nil), data.Proxies...)
	s.attemptNotes = data.AttemptNotes
	for _, note := range s.attemptNotes {
		if !utf8.ValidString(note) || utf8.RuneCountInString(note) > MaxAttemptNoteLength {
			return nil, ErrCorrupt
		}
	}
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
	case Saved, Queued, Running, Registered, Unverified, Imported, Failed, Uncertain:
		return true
	}
	return false
}

func validRows(rs []Row) bool {
	ids, emails := map[string]bool{}, map[string]bool{}
	for _, r := range rs {
		key := rowIdentity(r)
		if r.ID == "" || ids[r.ID] || key == "" || emails[key] || !validStatus(r.Status) {
			return false
		}
		ids[r.ID], emails[key] = true, true
	}
	return true
}

func rowIdentity(r Row) string {
	if r.GitHub != nil {
		return "github:" + strings.ToLower(strings.TrimSpace(r.GitHub.Login))
	}
	return strings.ToLower(strings.TrimSpace(r.Email))
}

// Add inserts rows from one attempt. A mailbox that already has a row is
// returned as is and not reset: re-submitting a list must not silently
// re-register an account that is already in the pool.
func (s *Store) Add(attempt string, rows []Row) ([]Summary, error) {
	return s.add(attempt, rows, Queued)
}

// AddSaved keeps credentials and assigned egress without scheduling any work.
func (s *Store) AddSaved(attempt string, rows []Row) ([]Summary, error) {
	return s.add(attempt, rows, Saved)
}

func (s *Store) add(attempt string, rows []Row, status Status) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range s.rows {
		seen[rowIdentity(r)] = true
	}
	next := append([]Row(nil), s.rows...)
	now := time.Now()
	out := make([]Summary, 0, len(rows))
	for _, r := range rows {
		email := strings.ToLower(strings.TrimSpace(r.Email))
		if email == "" || strings.Contains(email, " ") {
			return nil, ErrInvalid
		}
		identity := rowIdentity(r)
		if seen[identity] {
			for _, old := range next {
				if rowIdentity(old) == identity {
					out = append(out, SummaryOf(old))
					break
				}
			}
			continue
		}
		seen[identity] = true
		nr := cloneRow(r)
		nr.Email = email
		nr.Attempt = attempt
		nr.Status = status
		nr.ProxyBound = true
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

// ForAccount returns a unique, exact account/key link without projecting all
// rows or guessing by email. The snapshot is detached and remains sensitive.
func (s *Store) ForAccount(accountID, key string) (Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *Row
	for i := range s.rows {
		r := &s.rows[i]
		if r.Account != accountID {
			continue
		}
		if found != nil || r.APIKey != key || key == "" || r.Status != Imported {
			return Row{}, ErrInvalid
		}
		found = r
	}
	if found == nil {
		return Row{}, ErrNotFound
	}
	return cloneRow(*found), nil
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

// SetProxy changes only the registration egress. Pool accounts keep their own
// proxy after admission; queued/running work may already hold the old route.
func (s *Store) SetProxy(id, proxyURL string) (Row, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if err := ValidateProxy(proxyURL); err != nil {
		return Row{}, err
	}
	return s.Update(id, func(r *Row) error {
		switch r.Status {
		case Saved, Registered, Unverified, Imported, Failed, Uncertain:
		default:
			return ErrInvalid
		}
		if !r.ProxyBound || r.ProxyURL != proxyURL {
			r.Subscription = thregister.Subscription{}
		}
		r.ProxyURL = proxyURL
		r.ProxyBound = true
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

// Retry requeues a finished row. Saved credentials are never erased: the
// registrar resumes that session instead of submitting another signup.
func (s *Store) Retry(id string) (Row, error) {
	return s.Update(id, func(r *Row) error {
		switch r.Status {
		case Saved, Failed, Uncertain, Registered, Unverified:
		default:
			return ErrInvalid
		}
		r.Status = Queued
		r.Detail = ""
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
		notes := maps.Clone(s.attemptNotes)
		remaining := false
		for _, row := range next {
			if row.Attempt == r.Attempt {
				remaining = true
				break
			}
		}
		if !remaining {
			delete(notes, r.Attempt)
		}
		if err := s.write(next, s.proxies, notes, "thsignupstore/v1"); err != nil {
			return err
		}
		s.rows, s.attemptNotes = next, notes
		return nil
	}
	return ErrNotFound
}

// AttemptNote returns the administrator's note for a registration task.
func (s *Store) AttemptNote(attempt string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attemptNotes[attempt]
}

// SetAttemptNote changes only task metadata, even while its rows are running.
// Empty notes clear the annotation; an unknown task never creates orphan data.
func (s *Store) SetAttemptNote(attempt, note string) (string, error) {
	note = strings.TrimSpace(note)
	if !utf8.ValidString(note) || utf8.RuneCountInString(note) > MaxAttemptNoteLength {
		return "", ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, row := range s.rows {
		if row.Attempt == attempt {
			found = true
			break
		}
	}
	if !found {
		return "", ErrNotFound
	}
	notes := maps.Clone(s.attemptNotes)
	if notes == nil {
		notes = make(map[string]string)
	}
	if note == "" {
		delete(notes, attempt)
	} else {
		notes[attempt] = note
	}
	if err := s.write(s.rows, s.proxies, notes, "thsignupstore/v1"); err != nil {
		return "", err
	}
	s.attemptNotes = notes
	return note, nil
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
	hasSession := false
	for _, c := range r.Cookies {
		if c.Value != "" && thregister.IsSessionCookie(c.Name) {
			hasSession = true
			break
		}
	}
	out := Summary{
		ID: r.ID, Attempt: r.Attempt, Email: r.Email, Status: r.Status, Detail: r.Detail,
		Proxy:        ProxyLabel(r.ProxyURL),
		Group:        r.Group,
		KeyMasked:    MaskKey(r.APIKey),
		HasMailbox:   r.MailboxURL != "",
		HasSession:   hasSession,
		HasCheckout:  r.Checkout != nil && r.Checkout.URL != "",
		Subscription: r.Subscription,
		Verified:     r.Verified, FreeModels: r.FreeModels, AccountID: r.Account,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.GitHub != nil {
		out.AuthProvider, out.GitHubLogin = "github", r.GitHub.Login
		out.GitHubProxy = ProxyLabel(r.GitHub.ProxyURL)
		out.HasGitHubSession = r.GitHub.ProviderToken != ""
		for _, c := range r.GitHub.Cookies {
			if c.Name == "user_session" && c.Value != "" {
				out.HasGitHubSession = true
			}
		}
	}
	return out
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

// Proxies returns a detached copy for trusted registration code only.
func (s *Store) Proxies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.proxies...)
}

// SaveProxies publishes the proxy pool in the same encrypted envelope as rows.
func (s *Store) SaveProxies(proxies []string) error {
	if len(proxies) > 1000 {
		return errors.New("最多保存 1000 个代理")
	}
	next := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		proxy = strings.TrimSpace(proxy)
		if proxy == "" || len(proxy) > 4096 {
			return errors.New("代理地址不能为空或过长")
		}
		if err := ValidateProxy(proxy); err != nil {
			return err
		}
		next = append(next, proxy)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.write(s.rows, next, s.attemptNotes, "thsignupstore/v1"); err != nil {
		return err
	}
	s.proxies = next
	return nil
}

func (s *Store) save(rows []Row) error {
	return s.write(rows, s.proxies, s.attemptNotes, "thsignupstore/v1")
}

func cloneRow(r Row) Row {
	r.Cookies = append([]Cookie(nil), r.Cookies...)
	if r.GitHub != nil {
		gh := *r.GitHub
		gh.Cookies = append([]Cookie(nil), gh.Cookies...)
		r.GitHub = &gh
	}
	if r.Checkout != nil {
		checkout := *r.Checkout
		r.Checkout = &checkout
	}
	return r
}

func (s *Store) write(rows []Row, proxies []string, notes map[string]string, aad string) error {
	var disk, githubRows []diskRow
	for _, r := range rows {
		if r.GitHub != nil {
			githubRows = append(githubRows, r.disk())
		} else {
			disk = append(disk, r.disk())
		}
	}
	plain, err := json.Marshal(diskData{Version: 1, Rows: disk, Proxies: proxies, AttemptNotes: notes})
	if err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	b, err := json.Marshal(envelope{Version: 1, Nonce: nonce, Data: s.aead.Seal(nil, nonce, plain, []byte(aad)), GitHubRows: githubRows})
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
