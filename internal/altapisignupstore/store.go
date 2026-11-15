// Package altapisignupstore persists altapi.ai registration candidates and
// finished accounts.
//
// 明文 JSON 存储：邮箱、接码地址、代理、密码、API Key、session cookie 全部以
// 可读形式落盘，便于直接查看、导出和手工编辑。权限 0600，依靠文件系统权限与
// data 目录隔离，不做额外加密。
//
// A path must be owned by one Store in one process; methods are goroutine-safe.
package altapisignupstore

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
	"strings"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("altapi candidate not found")
	ErrInvalid  = errors.New("invalid altapi candidate or transition")
	ErrCorrupt  = errors.New("invalid altapi signup store json")
)

// Status mirrors the lifecycle shown in the UI.
type Status string

const (
	Queued     Status = "queued"     // 待注册
	Running    Status = "running"    // 注册中
	SentCode   Status = "sent_code"  // 已发验证码，等信
	Registered Status = "registered" // 已注册，未登录
	Verified   Status = "verified"   // 已登录，session 可用
	KeyCreated Status = "key_created" // 已生成 API Key，可入池
	Failed     Status = "failed"
	Cancelled  Status = "cancelled"
)

// Candidate is one registration attempt / one account.
//
// altapi.ai 的注册只有 5 步（发码→收码→注册→登录→建 Key），没有邮箱验证跳转、
// 没有绑卡，所以比 Deploy 的候选模型简单。session cookie 是后续读真实余额的
// 唯一凭据，必须持久化。
type Candidate struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Password string `json:"password,omitempty"`
	Username string `json:"username,omitempty"`
	// MailboxURL 是接码地址（含 token），用于取验证邮件
	MailboxURL string `json:"mailbox_url"`
	ProxyURL   string `json:"proxy_url,omitempty"`
	Status     Status `json:"status"`

	// 注册结果
	AccountID string `json:"account_id,omitempty"` // new-api 的 user id
	Group     string `json:"group,omitempty"`     // default | vip
	// Quota / UsedQuota 是 /api/user/self 的原始数值；BalanceUSD 是按
	// quota_per_unit 换算后与页面 "Current balance" 一致的数字。
	Quota          float64   `json:"quota,omitempty"`
	UsedQuota      float64   `json:"used_quota,omitempty"`
	QuotaPerUnit   float64   `json:"quota_per_unit,omitempty"`
	BalanceUSD     float64   `json:"balance_usd,omitempty"`
	QuotaCheckedAt time.Time `json:"quota_checked_at,omitempty"`

	// 会话：altapi.ai 的 session cookie，用于 /api/* 控制面（读余额）
	SessionCookie string `json:"session_cookie,omitempty"`
	// APIKey 是入池（路由）用的推理凭据，48 字符裸值
	APIKey string `json:"api_key,omitempty"`
	// TokenID 是 new-api 里这把 Key 的 id，便于后续删除/重置
	TokenID string `json:"token_id,omitempty"`

	// ImportedAcctID 入池后对应的 config.Account.ID，避免重复导入
	ImportedAcctID string `json:"imported_account_id,omitempty"`

	// 指纹（注册时绑定，后续登录必须复用）
	UA           string `json:"ua,omitempty"`
	Platform     string `json:"platform,omitempty"`
	UserAgentSHA string `json:"ua_sha,omitempty"`

	// 出口
	ExitIP  string `json:"exit_ip,omitempty"`
	Country string `json:"country,omitempty"`

	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`

	// RegisterAttemptAt 是最近一次真正提交注册的时刻，重发验证码时作为
	// "只认这个时刻之后的邮件"的基准。
	RegisterAttemptAt time.Time `json:"register_attempt_at,omitempty"`
	// LastResendAt 是最近一次手动"重发验证码"的时刻，仅用于 UI 提示。
	LastResendAt time.Time `json:"last_resend_at,omitempty"`

	// Attempts 是注册尝试次数，用于重试上限。
	Attempts int `json:"attempts,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// Settings 保存注册机运行参数。
type Settings struct {
	// MinIntervalSec 两次发码之间的最小间隔（秒）。站点实测同邮箱 30s。
	MinIntervalSec float64 `json:"min_interval_sec"`
	// MaxRetries 遇 429 / CF 拦截的重试次数
	MaxRetries int `json:"max_retries"`
	// VerifyTimeoutSec 等待验证邮件的超时
	VerifyTimeoutSec int `json:"verify_timeout_sec"`
	// ProxyPool 代理池（每行一个），按候选顺序轮转
	ProxyPool []string `json:"proxy_pool"`
	// AutoVerify 注册后自动取信验证
	AutoVerify bool `json:"auto_verify"`
	// ImportToPool 注册成功后自动创建 API Key 并入池
	ImportToPool bool `json:"import_to_pool"`
	// TokenGroup 建 Key 时使用的分组（default | vip）
	TokenGroup string `json:"token_group"`
	// TokenName 建 Key 时的名称
	TokenName string `json:"token_name"`
	// AffCode 可选的邀请码
	AffCode string `json:"aff_code,omitempty"`
	// BaseURL 站点根地址（不带 /v1）
	BaseURL string `json:"base_url"`
	// PythonBin 注册机解释器路径
	PythonBin string `json:"python_bin"`
	// MaxAttempts 单个候选最多尝试几次
	MaxAttempts int `json:"max_attempts"`
	// Workers 并发数（受代理池大小限制）
	Workers int `json:"workers"`
}

// DefaultSettings 返回可用的默认值。
func DefaultSettings() Settings {
	return Settings{
		MinIntervalSec:   32, // 站点限流 30s，留 2s 余量
		MaxRetries:       3,
		VerifyTimeoutSec: 180,
		AutoVerify:       true,
		ImportToPool:     true,
		TokenGroup:       "vip",
		TokenName:        "newapi",
		BaseURL:          "https://altapi.ai",
		MaxAttempts:      2,
		Workers:          2,
	}
}

type file struct {
	Version    int         `json:"version"`
	Settings   Settings    `json:"settings"`
	Candidates []Candidate `json:"candidates"`
}

// Store is one JSON file's owner.
type Store struct {
	mu   sync.Mutex
	path string
	f    file
}

// Open loads (or creates) the store. Missing file is not an error.
func Open(path string) (*Store, error) {
	s := &Store{path: path, f: file{Version: 1, Settings: DefaultSettings()}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(b, &s.f); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	def := DefaultSettings()
	// A zero value from an older file inherits the default, never 0.
	if s.f.Settings.MinIntervalSec == 0 {
		s.f.Settings.MinIntervalSec = def.MinIntervalSec
	}
	if s.f.Settings.MaxRetries == 0 {
		s.f.Settings.MaxRetries = def.MaxRetries
	}
	if s.f.Settings.VerifyTimeoutSec == 0 {
		s.f.Settings.VerifyTimeoutSec = def.VerifyTimeoutSec
	}
	if s.f.Settings.MaxAttempts == 0 {
		s.f.Settings.MaxAttempts = def.MaxAttempts
	}
	if s.f.Settings.Workers == 0 {
		s.f.Settings.Workers = def.Workers
	}
	if s.f.Settings.TokenGroup == "" {
		s.f.Settings.TokenGroup = def.TokenGroup
	}
	if s.f.Settings.TokenName == "" {
		s.f.Settings.TokenName = def.TokenName
	}
	if s.f.Settings.BaseURL == "" {
		s.f.Settings.BaseURL = def.BaseURL
	}
	return s, nil
}

// List returns every candidate, oldest first.
func (s *Store) List() []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Candidate, len(s.f.Candidates))
	copy(out, s.f.Candidates)
	return out
}

// Get returns one candidate by id.
func (s *Store) Get(id string) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.f.Candidates {
		if c.ID == id {
			return c, nil
		}
	}
	return Candidate{}, ErrNotFound
}

// FindByEmail reports the candidate with this email (case-insensitive).
func (s *Store) FindByEmail(email string) (Candidate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.f.Candidates {
		if strings.EqualFold(c.Email, email) {
			return c, true
		}
	}
	return Candidate{}, false
}

// Add inserts a candidate, assigning an id when absent. A duplicate email is
// refused rather than silently replaced: two rows for one mailbox would both
// consume the same 30s send window and both fail.
func (s *Store) Add(c Candidate) (Candidate, error) {
	if strings.TrimSpace(c.Email) == "" {
		return Candidate{}, fmt.Errorf("%w: email required", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.f.Candidates {
		if strings.EqualFold(ex.Email, c.Email) {
			return Candidate{}, fmt.Errorf("%w: email %s already exists", ErrInvalid, c.Email)
		}
	}
	if c.ID == "" {
		c.ID = newID()
	}
	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if c.Status == "" {
		c.Status = Queued
	}
	s.f.Candidates = append(s.f.Candidates, c)
	return c, s.flushLocked()
}

// Update applies fn to one candidate and persists.
func (s *Store) Update(id string, fn func(*Candidate) error) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.f.Candidates {
		if s.f.Candidates[i].ID != id {
			continue
		}
		if err := fn(&s.f.Candidates[i]); err != nil {
			return s.f.Candidates[i], err
		}
		s.f.Candidates[i].UpdatedAt = time.Now()
		return s.f.Candidates[i], s.flushLocked()
	}
	return Candidate{}, ErrNotFound
}

// Delete removes one candidate.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.f.Candidates {
		if s.f.Candidates[i].ID == id {
			s.f.Candidates = append(s.f.Candidates[:i], s.f.Candidates[i+1:]...)
			return s.flushLocked()
		}
	}
	return ErrNotFound
}

// Settings returns a copy of the运行参数.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.f.Settings
	cp.ProxyPool = append([]string(nil), s.f.Settings.ProxyPool...)
	return cp
}

// SaveSettings replaces the运行参数.
func (s *Store) SaveSettings(in Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	in.ProxyPool = append([]string(nil), in.ProxyPool...)
	s.f.Settings = in
	return s.flushLocked()
}

// Summary 是号池概览的聚合数字。
type Summary struct {
	Total       int            `json:"total"`
	ByStatus    map[string]int `json:"byStatus"`
	WithKey     int            `json:"withKey"`
	WithSession int            `json:"withSession"`
	Imported    int            `json:"imported"`
	// BalanceUSD sums only accounts whose balance was actually read.
	BalanceUSD     float64 `json:"balanceUsd"`
	BalanceCount   int     `json:"balanceCount"`
	BalanceUnknown int     `json:"balanceUnknown"`
}

// Summarize aggregates the candidates for the overview page.
func (s *Store) Summarize() Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Summary{ByStatus: map[string]int{}}
	for _, c := range s.f.Candidates {
		out.Total++
		out.ByStatus[string(c.Status)]++
		if c.APIKey != "" {
			out.WithKey++
		}
		if c.SessionCookie != "" {
			out.WithSession++
		}
		if c.ImportedAcctID != "" {
			out.Imported++
		}
		// A balance is known only when /api/user/self actually answered: the
		// session cookie is the credential and QuotaCheckedAt records the read.
		// Everything else is unknown, never zero.
		if c.SessionCookie != "" && !c.QuotaCheckedAt.IsZero() {
			out.BalanceUSD += c.BalanceUSD
			out.BalanceCount++
		} else if c.Status == KeyCreated || c.Status == Verified {
			out.BalanceUnknown++
		}
	}
	return out
}

// NextQueued returns up to n candidates that still need work, oldest first.
func (s *Store) NextQueued(n int) []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Candidate
	for _, c := range s.f.Candidates {
		if len(out) >= n {
			break
		}
		if c.Status == Queued || (c.Status == Failed && c.Attempts < 2) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Flush writes the file now.
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

// flushLocked writes atomically: temp file in the same dir, then rename.
func (s *Store) flushLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".altapi-signup-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}

func newID() string {
	var b [8]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return fmt.Sprintf("c%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
