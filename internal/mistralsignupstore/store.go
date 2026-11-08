// Package mistralsignupstore persists Mistral registration results.
//
// Mirrors perchsignupstore: AES-GCM at rest, one Store per path per process,
// goroutine-safe. No response or log carries a plaintext API key without an
// explicit admin reveal request.
package mistralsignupstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
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
	ErrCorrupt  = errors.New("cannot authenticate mistral signup store")
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

// Keys holds the API key material. Never serialized without encryption.
type Keys struct {
	API    string `json:"api,omitempty"`
	Vibe   string `json:"vibe,omitempty"`
	Studio string `json:"studio,omitempty"`
	// VibeCLI 是 chat.mistral.ai 的 CLI Key，需要订阅后才拿得到
	VibeCLI string `json:"vibe_cli,omitempty"`
}

// Candidate is one registration attempt.
type Candidate struct {
	ID          string    `json:"id"`
	Email       string    `json:"email"`
	Password    string    `json:"password,omitempty"` // 加密存储，默认不回传
	MailboxURL  string    `json:"mailbox_url"`
	Region      string    `json:"region"`
	ProxyURL    string    `json:"proxy_url,omitempty"`
	Status      Status    `json:"status"`
	OrgUUID     string    `json:"org_uuid,omitempty"`
	WorkspaceID string    `json:"workspace_uuid,omitempty"`
	Keys        Keys      `json:"keys"`
	Error       string    `json:"error,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedBy   string    `json:"created_by,omitempty"`
}

// Public returns a copy safe for list responses: secrets stripped.
func (c Candidate) Public() Candidate {
	c.Password = ""
	c.Keys = Keys{
		API:     maskKey(c.Keys.API),
		Vibe:    maskKey(c.Keys.Vibe),
		Studio:  maskKey(c.Keys.Studio),
		VibeCLI: maskKey(c.Keys.VibeCLI),
	}
	return c
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "****"
	}
	return "****" + k[len(k)-4:]
}

// Store is a single-file encrypted store.
type Store struct {
	mu    sync.Mutex
	path  string
	gcm   cipher.AEAD
	rows  []Candidate
	dirty bool
}

type envelope struct {
	Version int         `json:"version"`
	Rows    []Candidate `json:"rows"`
}

// Open loads (or creates) the store at path using secret as AES-256 key material.
func Open(path string, secret []byte) (*Store, error) {
	if len(secret) < 16 {
		return nil, fmt.Errorf("mistral store: secret too short")
	}
	key := sha256.Sum256(secret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, gcm: gcm}

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

	plain, err := s.decrypt(raw)
	if err != nil {
		return nil, err
	}
	var env envelope
	if err := json.Unmarshal(plain, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	s.rows = env.Rows
	return s, nil
}

func (s *Store) encrypt(plain []byte) ([]byte, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.gcm.Seal(nonce, nonce, plain, nil), nil
}

func (s *Store) decrypt(raw []byte) ([]byte, error) {
	ns := s.gcm.NonceSize()
	if len(raw) < ns {
		return nil, ErrCorrupt
	}
	plain, err := s.gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return plain, nil
}

// List returns all candidates, newest first, with secrets masked.
func (s *Store) List() []Candidate {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Candidate, 0, len(s.rows))
	for _, r := range s.rows {
		out = append(out, r.Public())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

// Get returns one candidate including secrets (for a reveal request).
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
	plain, err := json.Marshal(env)
	if err != nil {
		return err
	}
	enc, err := s.encrypt(plain)
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
