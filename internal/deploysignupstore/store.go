// Package deploysignupstore persists Deploy (deploy.com) registration candidates
// and finished accounts.
//
// 明文 JSON 存储：邮箱、接码地址、代理、密码、会话 cookie 全部以可读形式落盘，
// 便于直接查看、导出和手工编辑。权限 0600，依靠文件系统权限与 data 目录隔离，
// 不做额外加密。
//
// A path must be owned by one Store in one process; methods are goroutine-safe.
package deploysignupstore

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
	ErrNotFound = errors.New("deploy candidate not found")
	ErrInvalid  = errors.New("invalid deploy candidate or transition")
	ErrCorrupt  = errors.New("invalid deploy signup store json")
)

// Status mirrors the lifecycle shown in the UI.
type Status string

const (
	Queued     Status = "queued"     // 待注册
	Running    Status = "running"    // 注册中
	Registered Status = "registered" // 注册成功，待验证邮箱
	Verified   Status = "verified"   // 邮箱已验证，会话可用
	Failed     Status = "failed"
	Cancelled  Status = "cancelled"
)

// Card 是一张可用于绑定的银行卡。
type Card struct {
	Number  string `json:"number"`
	ExpMon  int    `json:"exp_mon"`
	ExpYear int    `json:"exp_year"`
	CVC     string `json:"cvc"`
	Zip     string `json:"zip"`
	Country string `json:"country"` // ISO2，如 US
	// Brand 由 BIN 推断（visa/mastercard/amex/...），仅作展示
	Brand string `json:"brand,omitempty"`
}

// Masked 返回只保留后四位的展示串。
func (c Card) Masked() string {
	if len(c.Number) < 4 {
		return "****"
	}
	return "**** **** **** " + c.Number[len(c.Number)-4:]
}

// Candidate is one registration attempt / one account.
type Candidate struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Password string `json:"password,omitempty"`
	// MailboxURL 是接码地址（含 token），用于取验证邮件
	MailboxURL string `json:"mailbox_url"`
	ProxyURL   string `json:"proxy_url,omitempty"`
	Status     Status `json:"status"`

	// 注册结果
	AccountID     string `json:"account_id,omitempty"`
	OrgID         string `json:"org_id,omitempty"`
	IsVerified    bool   `json:"is_verified,omitempty"`
	PersonalOrgID string `json:"personal_org_id,omitempty"`

	// 会话：hive-portal-production 的值，用于后续调 portal API
	SessionCookie string `json:"session_cookie,omitempty"`

	// 以下三项来自 POST /api/organization/{org}/tokens，是入池（路由）用的凭据。
	// Deploy 的路由使用 Secret Key（不是 access_key）。Secret Key 只在创建时返回一次，
	// 创建后门票接口只回 access_key，所以必须在这里持久化。
	AccessKey      string `json:"access_key,omitempty"`
	SecretKey      string `json:"secret_key,omitempty"`
	ImportedAcctID string `json:"imported_account_id,omitempty"`

	// 指纹（注册时绑定，后续登录必须复用）
	UA        string `json:"ua,omitempty"`
	Browser   string `json:"browser,omitempty"`
	Platform  string `json:"platform,omitempty"`
	JA3       string `json:"ja3,omitempty"`
	UserAgentSHA string `json:"ua_sha,omitempty"`

	// 出口
	ExitIP  string `json:"exit_ip,omitempty"`
	Country string `json:"country,omitempty"`

	// 支付
	Card          *Card  `json:"card,omitempty"`
	Recharged     bool   `json:"recharged,omitempty"`
	CreditBalance string `json:"credit_balance,omitempty"`

	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	CreatedBy string    `json:"created_by,omitempty"`
}

// Settings 保存注册机运行参数。
type Settings struct {
	// MinIntervalSec 两次注册之间的最小间隔（秒），防止 429
	MinIntervalSec float64 `json:"min_interval_sec"`
	// MaxRetries 遇 429 的重试次数
	MaxRetries int `json:"max_retries"`
	// VerifyTimeoutSec 等待验证邮件的超时
	VerifyTimeoutSec int `json:"verify_timeout_sec"`
	// Transport curl_cffi 或 urllib
	Transport string `json:"transport"`
	// ProxyPool 代理池（每行一个），按候选顺序轮转
	ProxyPool []string `json:"proxy_pool"`
	// AutoVerify 注册后自动取信验证
	AutoVerify bool `json:"auto_verify"`
	// SysProxy 本机系统代理（Clash 等），上游代理经它出网
	SysProxy string `json:"sys_proxy"`
	// BridgePort 本地代理桥起始端口
	BridgePort int `json:"bridge_port"`
	// ImportToPool 注册成功后自动创建 API Key 并入池
	ImportToPool bool `json:"import_to_pool"`
	// PythonBin 注册机解释器路径
	PythonBin string `json:"python_bin"`
}

// DefaultSettings 返回可用的默认值。
func DefaultSettings() Settings {
	return Settings{
		MinIntervalSec:   8,
		MaxRetries:       6,
		VerifyTimeoutSec: 240,
		Transport:        "curl_cffi",
		AutoVerify:       true,
		PythonBin:        "python3",
		SysProxy:         "127.0.0.1:10808",
		BridgePort:       17500,
	}
}

// Store is a single plaintext JSON file.
type Store struct {
	mu       sync.Mutex
	path     string
	rows     []Candidate
	settings Settings
	dirty    bool
}

type envelope struct {
	Version  int         `json:"version"`
	Rows     []Candidate `json:"rows"`
	Settings Settings    `json:"settings"`
}

// Open loads (or creates) the store at path.
func Open(path string, _ []byte) (*Store, error) {
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
	if env.Settings.MinIntervalSec > 0 {
		s.settings = env.Settings
	}
	return s, nil
}

// List returns all candidates, newest first.
func (s *Store) List() []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Candidate, len(s.rows))
	copy(out, s.rows)
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Get returns one candidate.
func (s *Store) Get(id string) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return Candidate{}, ErrNotFound
}

// FindByEmail returns one candidate by email (case-insensitive).
func (s *Store) FindByEmail(email string) (Candidate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if equalFold(r.Email, email) {
			return r, true
		}
	}
	return Candidate{}, false
}

// Add appends a new candidate.
func (s *Store) Add(c Candidate) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Email == "" {
		return Candidate{}, fmt.Errorf("%w: email required", ErrInvalid)
	}
	for _, r := range s.rows {
		if equalFold(r.Email, c.Email) {
			return Candidate{}, fmt.Errorf("%w: email already exists", ErrInvalid)
		}
	}
	now := time.Now().UTC()
	if c.ID == "" {
		c.ID = newID()
	}
	if c.Status == "" {
		c.Status = Queued
	}
	c.CreatedAt = now
	c.UpdatedAt = now
	s.rows = append(s.rows, c)
	s.dirty = true
	if err := s.flushLocked(); err != nil {
		return Candidate{}, err
	}
	return c, nil
}

// Update mutates one candidate atomically.
func (s *Store) Update(id string, fn func(*Candidate) error) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].ID != id {
			continue
		}
		if err := fn(&s.rows[i]); err != nil {
			return Candidate{}, err
		}
		s.rows[i].UpdatedAt = time.Now().UTC()
		s.dirty = true
		if err := s.flushLocked(); err != nil {
			return Candidate{}, err
		}
		return s.rows[i], nil
	}
	return Candidate{}, ErrNotFound
}

// Delete removes one candidate.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].ID == id {
			s.rows = append(s.rows[:i], s.rows[i+1:]...)
			s.dirty = true
			return s.flushLocked()
		}
	}
	return ErrNotFound
}

// Settings returns a copy of current settings.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.settings
	out.ProxyPool = append([]string(nil), s.settings.ProxyPool...)
	return out
}

// SaveSettings replaces settings.
func (s *Store) SaveSettings(in Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.MinIntervalSec <= 0 {
		in.MinIntervalSec = 8
	}
	if in.MaxRetries <= 0 {
		in.MaxRetries = 6
	}
	if in.VerifyTimeoutSec <= 0 {
		in.VerifyTimeoutSec = 240
	}
	if in.Transport == "" {
		in.Transport = "curl_cffi"
	}
	if in.PythonBin == "" {
		in.PythonBin = "python3"
	}
	s.settings = in
	s.dirty = true
	return s.flushLocked()
}

// Flush writes the store if dirty.
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
		return fmt.Sprintf("dep-%d", time.Now().UnixNano())
	}
	return "dep-" + hex.EncodeToString(b)
}

// equalFold 是大小写不敏感的相等比较（避免引入 strings.EqualFold 的可见差异）。
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
