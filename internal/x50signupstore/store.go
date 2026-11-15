// Package x50signupstore persists 50x AI (ai.50x.energy) registration jobs.
//
// 明文 JSON 存储：邮箱、接码地址、代理、密码、会话与 API Key 以可读形式落盘，
// 便于直接查看与导出。文件权限 0600，依赖文件系统权限与 data 目录隔离。
//
// 一个 path 只属于一个 Store 实例；方法均为 goroutine-safe。
package x50signupstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("50x 注册任务不存在")
	ErrInvalid  = errors.New("50x 注册任务参数无效")
	ErrCorrupt  = errors.New("50x 注册任务存储损坏")
)

// Status is the lifecycle shown in the console and mirrored in the UI.
//
// The upstream flow is a single pass (register → confirm mail → verify → key),
// so the interesting intermediate state is "waiting for the confirm mail".
type Status string

const (
	Queued    Status = "queued"    // 待注册
	Running   Status = "running"   // 注册中
	Waiting   Status = "waiting"   // 已提交注册，等待确认邮件
	Verified  Status = "verified"  // 邮箱已确认，会话可用
	Keyed     Status = "keyed"     // 已创建 API Key，注册完成
	Failed    Status = "failed"    // 失败（可重试）
	Cancelled Status = "cancelled" // 已取消
)

// Done reports whether the row reached the success terminal state.
func (s Status) Done() bool { return s == Keyed }

// Retryable reports whether the console may offer a retry.
func (s Status) Retryable() bool {
	return s == Queued || s == Failed || s == Cancelled
}

// Row is one mailbox → account attempt.
type Row struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	// MailboxURL is the iCloud HME share link carrying the read token.
	MailboxURL string `json:"mailbox_url"`
	// MailboxToken is the extracted share token; it is what actually reads mail.
	MailboxToken string `json:"mailbox_token,omitempty"`

	Password string `json:"password,omitempty"`
	ProxyURL string `json:"proxy_url,omitempty"`
	Status   Status `json:"status"`

	// Registration results.
	SessionToken string    `json:"session_token,omitempty"`
	APIKey       string    `json:"api_key,omitempty"`
	KeyID        int64     `json:"key_id,omitempty"`
	KeyPrefix    string    `json:"key_prefix,omitempty"`
	Plan         string    `json:"plan,omitempty"`
	Tier         string    `json:"tier,omitempty"`
	BalanceUSD   string    `json:"balance_usd,omitempty"`
	VerifiedAt   time.Time `json:"verified_at,omitempty"`
	KeyedAt      time.Time `json:"keyed_at,omitempty"`

	// Stage is the human-readable position in the flow.
	Stage string `json:"stage,omitempty"`
	// Error is the last failure reason. It never contains credentials.
	Error string `json:"error,omitempty"`
	// Attempts counts completed registration passes.
	Attempts int `json:"attempts,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Settings holds registration-machine defaults and the ordered proxy pool.
//
// The proxy pool is consumed in order: row N is bound to the least-loaded
// proxy, and a proxy may serve many rows. Binding is persisted on the row so a
// retry reuses the same egress instead of silently switching IP.
type Settings struct {
	// Concurrency is how many registrations run at once (1–10).
	Concurrency int `json:"concurrency"`
	// MailTimeoutSec bounds the wait for the confirm mail.
	MailTimeoutSec int `json:"mail_timeout_sec"`
	// MailPollSec is the shared-mailbox poll cadence.
	MailPollSec int `json:"mail_poll_sec"`
	// RequestTimeoutSec bounds one HTTP request.
	RequestTimeoutSec int `json:"request_timeout_sec"`
	// KeyName is the API key name created per account.
	KeyName string `json:"key_name"`
	// ProxyPool is the ordered list; empty means direct egress.
	ProxyPool []string `json:"proxy_pool"`
	// AutoBindProxy assigns the next proxy in order on import.
	AutoBindProxy bool `json:"auto_bind_proxy"`
	// PasswordMode is "random" (generated) or "email" (same as address).
	PasswordMode string `json:"password_mode"`
	// APIBase / MailBase override the upstream hosts. Empty keeps the official
	// endpoints; setting them lets a run target a mirror or a local fixture.
	APIBase  string `json:"api_base,omitempty"`
	MailBase string `json:"mail_base,omitempty"`
}

// DefaultSettings returns usable defaults matching the verified flow.
func DefaultSettings() Settings {
	return Settings{
		Concurrency:       2,
		MailTimeoutSec:    240,
		MailPollSec:       3,
		RequestTimeoutSec: 30,
		KeyName:           "registrar",
		AutoBindProxy:     true,
		PasswordMode:      "random",
	}
}

const (
	MinConcurrency = 1
	MaxConcurrency = 10
)

type envelope struct {
	Version  int      `json:"version"`
	Rows     []Row    `json:"rows"`
	Settings Settings `json:"settings"`
}

// Store is a single plaintext JSON file.
type Store struct {
	mu       sync.Mutex
	path     string
	rows     []Row
	settings Settings
	dirty    bool
}

// Open loads (or creates) the store at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, settings: DefaultSettings()}
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if len(raw) == 0 {
		return s, nil
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	s.rows = env.Rows
	if env.Settings.Concurrency > 0 {
		s.settings = env.Settings
	}
	return s, nil
}

// List returns all rows newest first.
func (s *Store) List() []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Row, len(s.rows))
	copy(out, s.rows)
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns one row.
func (s *Store) Get(id string) (Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return Row{}, ErrNotFound
}

// Snapshot returns a copy of every stored row in insertion order. The runner
// uses it to compute proxy distribution without racing the console.
func (s *Store) Snapshot() []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Row, len(s.rows))
	copy(out, s.rows)
	return out
}

// Add appends one row. Email must be unique (case-insensitive).
func (s *Store) Add(r Row) (Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addLocked(r)
}

// AddMany appends rows atomically and reports per-line rejections. A failure
// to persist rolls back the whole batch: partial import is worse than none.
func (s *Store) AddMany(rows []Row) (added []Row, errs []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		got, aerr := s.addLocked(r)
		if aerr != nil {
			errs = append(errs, r.Email+": "+aerr.Error())
			continue
		}
		added = append(added, got)
	}
	return added, errs, nil
}

func (s *Store) addLocked(r Row) (Row, error) {
	if r.Email == "" {
		return Row{}, fmt.Errorf("%w: 邮箱必填", ErrInvalid)
	}
	for _, exist := range s.rows {
		if equalFold(exist.Email, r.Email) {
			return Row{}, fmt.Errorf("%w: 该邮箱已存在", ErrInvalid)
		}
	}
	now := time.Now().UTC()
	if r.ID == "" {
		r.ID = newID()
	}
	if r.Status == "" {
		r.Status = Queued
	}
	r.CreatedAt = now
	r.UpdatedAt = now
	s.rows = append(s.rows, r)
	s.dirty = true
	if err := s.flushLocked(); err != nil {
		s.rows = s.rows[:len(s.rows)-1]
		return Row{}, err
	}
	return r, nil
}

// Update mutates one row atomically; a callback error leaves the row untouched.
func (s *Store) Update(id string, fn func(*Row) error) (Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].ID != id {
			continue
		}
		prev := s.rows[i]
		if err := fn(&s.rows[i]); err != nil {
			s.rows[i] = prev
			return Row{}, err
		}
		s.rows[i].UpdatedAt = time.Now().UTC()
		s.dirty = true
		if err := s.flushLocked(); err != nil {
			s.rows[i] = prev
			return Row{}, err
		}
		return s.rows[i], nil
	}
	return Row{}, ErrNotFound
}

// Delete removes one row.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].ID == id {
			removed := s.rows[i]
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			s.dirty = true
			if err := s.flushLocked(); err != nil {
				// restore ordering-independent contents
				s.rows = append(s.rows, removed)
				return err
			}
			return nil
		}
	}
	return ErrNotFound
}

// ClearFinished removes every terminal successfully-keyed row.
func (s *Store) ClearFinished() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.rows[:0]
	removed := 0
	for _, r := range s.rows {
		if r.Status == Keyed {
			removed++
			continue
		}
		kept = append(kept, r)
	}
	if removed == 0 {
		return 0, nil
	}
	old := append([]Row(nil), s.rows...)
	s.rows = kept
	s.dirty = true
	if err := s.flushLocked(); err != nil {
		s.rows = old
		return 0, err
	}
	return removed, nil
}

// Settings returns a copy of the current settings.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.settings
	out.ProxyPool = append([]string(nil), s.settings.ProxyPool...)
	return out
}

// SaveSettings replaces the settings after normalizing out-of-range values.
func (s *Store) SaveSettings(in Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Concurrency == 0 {
		in.Concurrency = DefaultSettings().Concurrency
	}
	if in.Concurrency < MinConcurrency {
		in.Concurrency = MinConcurrency
	}
	if in.Concurrency > MaxConcurrency {
		return Settings{}, fmt.Errorf("%w: 并发必须在 %d-%d 之间", ErrInvalid, MinConcurrency, MaxConcurrency)
	}
	if in.MailTimeoutSec <= 0 {
		in.MailTimeoutSec = 240
	}
	if in.MailPollSec <= 0 {
		in.MailPollSec = 3
	}
	if in.RequestTimeoutSec <= 0 {
		in.RequestTimeoutSec = 30
	}
	if in.KeyName == "" {
		in.KeyName = "registrar"
	}
	if in.PasswordMode != "email" {
		in.PasswordMode = "random"
	}
	// Copy the pool so the caller cannot mutate stored state through the slice.
	in.ProxyPool = append([]string(nil), in.ProxyPool...)
	s.settings = in
	s.dirty = true
	if err := s.flushLocked(); err != nil {
		return Settings{}, err
	}
	return in, nil
}

// Flush persists pending changes.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.dirty {
		return nil
	}
	return s.flushLocked()
}

func (s *Store) flushLocked() error {
	env := envelope{Version: 1, Rows: s.rows, Settings: s.settings}
	enc, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, enc, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func newID() string {
	b := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return fmt.Sprintf("x50-%d", time.Now().UnixNano())
	}
	return "x50-" + hex.EncodeToString(b)
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
