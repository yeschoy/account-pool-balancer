// Package telnyxsignupstore persists Telnyx (telnyx.com) registration jobs.
//
// 明文 JSON 存储：邮箱、邮箱凭据（clientId / refreshToken / shareToken）、
// 代理、Portal 会话与 API Key 以可读形式落盘，便于直接查看与导出。
// 文件权限 0600，依赖文件系统权限与 data 目录隔离。
//
// 一个 path 只属于一个 Store 实例；方法均为 goroutine-safe。
package telnyxsignupstore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("telnyx 注册任务不存在")
	ErrInvalid  = errors.New("telnyx 注册任务参数无效")
	ErrCorrupt  = errors.New("telnyx 注册任务存储损坏")
)

// Status is the lifecycle shown in the console.
//
// The Telnyx flow has one extra durable stop the 50x flow does not have: the
// one-time portal link expires within hours, so "waiting for mail" is a real
// state a row can sit in, and a row that missed its window needs a resend.
type Status string

const (
	Queued    Status = "queued"    // 待注册
	Running   Status = "running"   // 注册中
	Waiting   Status = "waiting"   // 已提交注册，等待验证邮件
	Linked    Status = "linked"    // 已兑换 Portal 会话
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

// Mail mode constants. A row carries either a ready share token or the
// Microsoft OAuth pair that resolves into one.
const (
	// MailModeShareToken means ShareToken is already usable.
	MailModeShareToken = "share"
	// MailModeMsa means ClientID+RefreshToken must be resolved first.
	MailModeMsa = "msa"
)

// Row is one mailbox → account attempt.
type Row struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	// MailboxPassword is the Outlook password as exported alongside the token.
	// It is kept for operator reference only; the flow never logs in with it.
	MailboxPassword string `json:"mailbox_password,omitempty"`

	// MailMode is MailModeShareToken or MailModeMsa.
	MailMode string `json:"mail_mode,omitempty"`
	// ShareToken is the "s" value of tool.yaoyiyaocoffee.cn/inbox#s=<token>.
	ShareToken string `json:"share_token,omitempty"`
	// ClientID / RefreshToken are the MsaArtifacts pair.
	//
	// RefreshToken rotates on every resolve call: the value stored here is
	// always the newest one the provider returned, and the previous value is
	// dead. Losing it means the mailbox must be re-exported by hand.
	ClientID     string `json:"client_id,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`

	ProxyURL string `json:"proxy_url,omitempty"`
	Status   Status `json:"status"`

	// Registration results.
	SessionToken string    `json:"session_token,omitempty"`
	APIKey       string    `json:"api_key,omitempty"`
	KeyID        string    `json:"key_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	AccountLevel string    `json:"account_level,omitempty"`
	CreditsUSD   string    `json:"credits_usd,omitempty"`
	EgressIP     string    `json:"egress_ip,omitempty"`
	LinkedAt     time.Time `json:"linked_at,omitempty"`
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
// Every Telnyx request for one account must leave from the same egress: the
// one-time portal token is bound to the address that submitted the signup, so
// a proxy switch mid-run produces a 401 on ai_dev_event_sessions even when the
// link is fresh. ProxyPool is therefore consumed in order and pinned per row.
type Settings struct {
	// Concurrency is how many registrations run at once (1–10).
	//
	// Values above 2 are not recommended: the Turnstile solver farm returns
	// lower-quality tokens under load and Telnyx rejects them as
	// "your browser could not be authenticated". The runner retries with a
	// fresh token, but throughput still degrades.
	Concurrency int `json:"concurrency"`
	// MailTimeoutSec bounds the wait for the sign-in mail.
	MailTimeoutSec int `json:"mail_timeout_sec"`
	// MailPollSec is the inbox poll cadence.
	MailPollSec int `json:"mail_poll_sec"`
	// RequestTimeoutSec bounds one HTTP request.
	RequestTimeoutSec int `json:"request_timeout_sec"`

	// CaptchaToken is the captcha.run API token.
	CaptchaToken string `json:"captcha_token,omitempty"`
	// SiteKey / TurnstileAction must match the widget on /sign-up.
	SiteKey         string `json:"site_key,omitempty"`
	TurnstileAction string `json:"turnstile_action,omitempty"`
	// CaptchaAttempts is how many fresh tokens one signup may burn.
	CaptchaAttempts int `json:"captcha_attempts,omitempty"`

	// KeyTags is the POST /v2/api_keys body tags list.
	KeyTags string `json:"key_tags,omitempty"`
	// OptInSubscriptions maps to subscription_opt_in.
	OptInSubscriptions bool `json:"opt_in_subscriptions"`

	// ProxyPool is the ordered list; empty means direct egress.
	ProxyPool []string `json:"proxy_pool"`
	// AutoBindProxy assigns the next proxy in order on import.
	AutoBindProxy bool `json:"auto_bind_proxy"`

	// SiteOrigin / APIBase / PortalBase / MailBase override the upstream hosts.
	// Empty keeps the official endpoints; setting them lets a run target a
	// mirror or a local fixture.
	SiteOrigin string `json:"site_origin,omitempty"`
	APIBase    string `json:"api_base,omitempty"`
	PortalBase string `json:"portal_base,omitempty"`
	MailBase   string `json:"mail_base,omitempty"`
}

// DefaultSettings returns usable defaults matching the verified flow.
func DefaultSettings() Settings {
	return Settings{
		Concurrency:        2,
		MailTimeoutSec:     240,
		MailPollSec:        6,
		RequestTimeoutSec:  45,
		SiteKey:            "0x4AAAAAABkzL3BycQIQz_Ti",
		TurnstileAction:    "signup",
		CaptchaAttempts:    3,
		KeyTags:            "new",
		OptInSubscriptions: true,
		AutoBindProxy:      true,
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

const storeVersion = 1

// Open loads (or creates) the store at path.
func Open(path string) (*Store, error) {
	s := &Store{path: path, settings: DefaultSettings()}
	buf, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, err
	}
	if len(buf) == 0 {
		return s, nil
	}
	var env envelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	s.rows = env.Rows
	s.settings = mergeSettings(env.Settings)
	return s, nil
}

// mergeSettings fills zero fields from defaults so an older file still loads.
func mergeSettings(in Settings) Settings {
	def := DefaultSettings()
	if in.Concurrency == 0 {
		in.Concurrency = def.Concurrency
	}
	if in.MailTimeoutSec == 0 {
		in.MailTimeoutSec = def.MailTimeoutSec
	}
	if in.MailPollSec == 0 {
		in.MailPollSec = def.MailPollSec
	}
	if in.RequestTimeoutSec == 0 {
		in.RequestTimeoutSec = def.RequestTimeoutSec
	}
	if in.CaptchaAttempts == 0 {
		in.CaptchaAttempts = def.CaptchaAttempts
	}
	if in.SiteKey == "" {
		in.SiteKey = def.SiteKey
	}
	if in.TurnstileAction == "" {
		in.TurnstileAction = def.TurnstileAction
	}
	if in.KeyTags == "" {
		in.KeyTags = def.KeyTags
	}
	return in
}

// NewRow builds a queued row with a fresh ID and timestamps.
func NewRow(email string) Row {
	now := time.Now().UTC()
	return Row{
		ID:        newID(),
		Email:     email,
		Status:    Queued,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// List returns a copy of every row, oldest first.
func (s *Store) List() []Row {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Row, len(s.rows))
	copy(out, s.rows)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Snapshot is an alias kept for the console's naming.
func (s *Store) Snapshot() []Row { return s.List() }

// Get returns one row by ID.
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

// Add inserts one row.
func (s *Store) Add(r Row) (Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addLocked(r)
}

// AddMany inserts rows, skipping duplicate emails.
func (s *Store) AddMany(rows []Row) (added []Row, errs []string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rows {
		got, aerr := s.addLocked(r)
		if aerr != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", r.Email, aerr))
			continue
		}
		added = append(added, got)
	}
	if len(added) > 0 {
		if ferr := s.flushLocked(); ferr != nil {
			return added, errs, ferr
		}
	}
	return added, errs, nil
}

func (s *Store) addLocked(r Row) (Row, error) {
	if r.Email == "" {
		return Row{}, ErrInvalid
	}
	for _, x := range s.rows {
		if equalFold(x.Email, r.Email) {
			return Row{}, fmt.Errorf("邮箱已存在")
		}
	}
	if r.ID == "" {
		r.ID = newID()
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	s.rows = append(s.rows, r)
	s.dirty = true
	return r, nil
}

// Update mutates one row through fn.
func (s *Store) Update(id string, fn func(*Row) error) (Row, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].ID != id {
			continue
		}
		if err := fn(&s.rows[i]); err != nil {
			return s.rows[i], err
		}
		s.rows[i].UpdatedAt = time.Now().UTC()
		s.dirty = true
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
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			s.dirty = true
			return nil
		}
	}
	return ErrNotFound
}

// ClearFinished drops every terminal row and reports how many went.
func (s *Store) ClearFinished() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.rows[:0]
	removed := 0
	for _, r := range s.rows {
		switch r.Status {
		case Keyed, Failed, Cancelled:
			removed++
		default:
			kept = append(kept, r)
		}
	}
	s.rows = kept
	if removed > 0 {
		s.dirty = true
	}
	return removed, nil
}

// Settings returns the current defaults.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// SaveSettings validates and stores the defaults.
func (s *Store) SaveSettings(in Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Concurrency < MinConcurrency {
		in.Concurrency = MinConcurrency
	}
	if in.Concurrency > MaxConcurrency {
		in.Concurrency = MaxConcurrency
	}
	if in.MailTimeoutSec <= 0 {
		in.MailTimeoutSec = 240
	}
	if in.MailPollSec <= 0 {
		in.MailPollSec = 6
	}
	if in.RequestTimeoutSec <= 0 {
		in.RequestTimeoutSec = 45
	}
	if in.CaptchaAttempts <= 0 {
		in.CaptchaAttempts = 3
	}
	in = mergeSettings(in)
	s.settings = in
	s.dirty = true
	return in, nil
}

// Flush writes the store to disk.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

func (s *Store) flushLocked() error {
	if !s.dirty {
		return nil
	}
	env := envelope{Version: storeVersion, Rows: s.rows, Settings: s.settings}
	buf, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.dirty = false
	return nil
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
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
