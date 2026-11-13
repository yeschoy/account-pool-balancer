// Package mistralsignupstore persists Mistral registration results.
//
// 明文 JSON 存储：账号、密码、API Key 全部以可读形式落盘，便于直接查看、
// 导出和手工编辑。权限 0600，依靠文件系统权限与 data 目录隔离，不做额外加密。
//
// A path must be owned by one Store in one process; methods are goroutine-safe.
package mistralsignupstore

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
	ErrNotFound = errors.New("mistral candidate not found")
	ErrInvalid  = errors.New("invalid mistral candidate or transition")
	ErrCorrupt  = errors.New("invalid mistral signup store json")
)

// Status mirrors the lifecycle a UI shows.
type Status string

const (
	Queued     Status = "queued"     // 等待注册
	Running    Status = "running"    // 注册中
	Registered Status = "registered" // 注册成功，等待订阅
	Ready      Status = "ready"      // 三把 Key 就绪
	Failed     Status = "failed"
	Cancelled  Status = "cancelled"
)

// Keys holds the API key material.
type Keys struct {
	API    string `json:"api,omitempty"`
	Vibe   string `json:"vibe,omitempty"`
	Studio string `json:"studio,omitempty"`
	// VibeCLI 是 chat.mistral.ai 的 CLI Key，需要订阅后才拿得到
	VibeCLI string `json:"vibe_cli,omitempty"`
}

// Candidate is one registration attempt.
type Candidate struct {
	ID          string `json:"id"`
	Email       string `json:"email"`
	Password    string `json:"password,omitempty"`
	MailboxURL  string `json:"mailbox_url"`
	Region      string `json:"region"`
	ProxyURL    string `json:"proxy_url,omitempty"`
	Status      Status `json:"status"`
	OrgUUID     string `json:"org_uuid,omitempty"`
	WorkspaceID string `json:"workspace_uuid,omitempty"`
	Keys        Keys   `json:"keys"`
	Error       string `json:"error,omitempty"`
	Detail      string `json:"detail,omitempty"`
	// ImportToPool 表示注册成功后自动加入 Mistral 账号池。
	ImportToPool bool `json:"import_to_pool,omitempty"`
	// ProxyIndex 是分配到的代理序号（按 candidates 顺序轮转）。
	ProxyIndex int `json:"proxy_index"`
	// ExitIP 是本次注册实际使用的出口 IP（cloudflare trace 实测）。
	// 用于"一 IP 一号"：同一个出口 IP 不允许注册第二个账号。
	ExitIP string `json:"exit_ip,omitempty"`
	// Country 是出口 IP 所在国，设备画像与账单地址都按它生成。
	Country string `json:"country,omitempty"`
	// ImportedAccountID 是入池后的账号 ID，空表示未入池。
	ImportedAccountID string    `json:"imported_account_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	CreatedBy         string    `json:"created_by,omitempty"`
}

// Store is a single plaintext JSON file.
type Store struct {
	mu    sync.Mutex
	path  string
	rows  []Candidate
	dirty bool
}

type envelope struct {
	Version int         `json:"version"`
	Rows    []Candidate `json:"rows"`
}

// Open loads (or creates) the store at path. secret 保留参数位置以兼容调用方，
// 明文存储不再使用它。
func Open(path string, _ []byte) (*Store, error) {
	s := &Store{path: path}

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

// Add appends a new candidate.
func (s *Store) Add(c Candidate) (Candidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Email == "" {
		return Candidate{}, fmt.Errorf("%w: email required", ErrInvalid)
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
	env := envelope{Version: 1, Rows: s.rows}
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
		return fmt.Sprintf("mst-%d", time.Now().UnixNano())
	}
	return "mst-" + hex.EncodeToString(b)
}
