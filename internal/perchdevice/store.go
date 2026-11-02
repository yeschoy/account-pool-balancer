// Package perchdevice stores each Perch account's device identity.
//
// The identity itself is built in internal/perch (DeviceIdentity); this package
// only owns persistence. Two rules shape it:
//
//   - It is sealed, not plaintext. A machine id shared across accounts is the
//     linkage signal this whole layer exists to avoid, so data/perch-device.json
//     is AES-256-GCM sealed under a key derived from the same routing secret as
//     perch-secrets.json. Anyone who can read both the ciphertext and the
//     secret can still decrypt it — this is at-rest hygiene, not a vault.
//   - It is written by one writer. The store is opened by the single server
//     process, and writes are serialised under one mutex; a lost write here
//     costs a fresh machine id, which would read as a new device.
package perchdevice

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
	"os"
	"path/filepath"
	"sync"
	"time"

	"account-pool-balancer/internal/perch"
)

// State is one account's stored device state.
type State struct {
	perch.DeviceIdentity
	// UpdatedAt is when this record was last written.
	UpdatedAt time.Time `json:"updatedAt,omitzero"`
}

type envelope struct {
	Version   int    `json:"version"`
	UpdatedAt string `json:"updatedAt"`
	// DeviceID is the process-wide device id used where the pool has no
	// account (health probes, quota reads). It is separate on purpose: the
	// account ids must not be derivable from it, and vice versa.
	DeviceID string                 `json:"deviceId,omitempty"`
	Records  map[string]sealedEntry `json:"records"`
}

type sealedEntry struct {
	Nonce string `json:"nonce"`
	Data  string `json:"data"`
}

// Store is the sealed device-identity file.
type Store struct {
	path string
	key  []byte
	now  func() time.Time
	logf func(string, ...any)

	mu      sync.Mutex
	env     envelope
	process string // process-wide device id
	dirty   bool
	lastErr error
}

// Open loads (or starts) the store at path. key is the balancer's routing
// secret, the same material perchstore seals its sessions with.
func Open(path string, key []byte, logf func(string, ...any)) (*Store, error) {
	if len(key) < 16 {
		return nil, errors.New("perchdevice: sealing key is too short")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s := &Store{path: path, key: deriveKey(key), logf: logf, now: time.Now,
		env: envelope{Version: 1, Records: map[string]sealedEntry{}}}
	if err := s.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if s.process == "" {
		s.process = perch.NewMachineID()
		s.dirty = true
		if err := s.flushLocked(); err != nil {
			s.logf("perch-device: could not persist the process device id: %v", err)
		}
	}
	return s, nil
}

// SetClock replaces the clock (tests).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now != nil {
		s.now = now
	}
}

func deriveKey(material []byte) []byte {
	sum := sha256.Sum256(append([]byte("perch-device-sealing\x00"), material...))
	return sum[:]
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("perch-device.json: %w", err)
	}
	s.env = env
	if s.env.Records == nil {
		s.env.Records = map[string]sealedEntry{}
	}
	s.process = env.DeviceID
	// Records stay sealed in memory until asked for: a store that never serves
	// an account never decrypts it.
	s.dirty = false
	return nil
}

// ProcessDeviceID is the pool-level device id. It is deterministic per install
// and never changes unless the file is lost.
func (s *Store) ProcessDeviceID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.process
}

// Get returns the account's identity, minting and persisting a machine id the
// first time the account is seen. The returned identity is always usable: if
// the file cannot be written the value still holds for this process, and the
// failure is logged rather than returned.
func (s *Store) Get(accountID string) perch.DeviceIdentity {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := accountID
	if id == "" {
		id = "-"
	}
	var ident perch.DeviceIdentity
	if ent, ok := s.env.Records[s.slot(id)]; ok {
		if err := s.open(id, ent, &ident); err != nil {
			// A record that will not decrypt is replaced, not trusted: an
			// empty id would be worse, since it reads as a brand-new device
			// on every request.
			s.logf("perch-device: account record is unreadable (%v); a new device id will be minted", err)
			ident = perch.DeviceIdentity{}
		}
	}
	ident, changed := ident.EnsureMachineID(s.now())
	if changed {
		s.writeLocked(id, ident)
	}
	return ident
}

// MarkFirstPrompt stamps the account's first-message time once and reports
// whether this call was the first. It is the pool's cli-first-prompt marker.
func (s *Store) MarkFirstPrompt(accountID string) bool {
	s.mu.Lock()
	var ident perch.DeviceIdentity
	id := accountID
	if id == "" {
		id = "-"
	}
	if ent, ok := s.env.Records[s.slot(id)]; ok {
		if err := s.open(id, ent, &ident); err != nil {
			s.mu.Unlock()
			s.logf("perch-device: account record is unreadable (%v)", err)
			return false
		}
	}
	ident, first := ident.MarkFirstPrompt(s.now())
	changed := first
	if changed {
		ident, _ = ident.EnsureMachineID(s.now())
		s.writeLocked(id, ident)
	}
	s.mu.Unlock()
	return first
}

// writeLocked stores and seals one record (caller holds s.mu).
func (s *Store) writeLocked(accountID string, ident perch.DeviceIdentity) {
	ent, err := s.seal(accountID, State{DeviceIdentity: ident, UpdatedAt: s.now().UTC()})
	if err != nil {
		s.logf("perch-device: sealing %s: %v", accountID, err)
		return
	}
	s.env.Records[s.slot(accountID)] = ent
	s.dirty = true
	if err := s.flushLocked(); err != nil {
		s.logf("perch-device: writing %s: %v", s.path, err)
	}
}

// slot is the envelope key for an account: a keyed digest, so the file does
// not list which accounts exist. Seal and open bind the record to the same
// digest as associated data, which is what stops one account's record being
// moved into another's slot.
func (s *Store) slot(accountID string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte("perch-device-slot\x00"))
	mac.Write([]byte(accountID))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func (s *Store) seal(accountID string, st State) (sealedEntry, error) {
	gcm, err := s.aead()
	if err != nil {
		return sealedEntry{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return sealedEntry{}, err
	}
	plain, err := json.Marshal(st)
	if err != nil {
		return sealedEntry{}, err
	}
	// The account id is associated data, so a record cannot be moved into
	// another account's slot without failing to open: that is what keeps one
	// account's machine id from being lent to another.
	ct := gcm.Seal(nil, nonce, plain, []byte(s.slot(accountID)))
	return sealedEntry{Nonce: hex.EncodeToString(nonce), Data: hex.EncodeToString(ct)}, nil
}

func (s *Store) open(accountID string, ent sealedEntry, out *perch.DeviceIdentity) error {
	gcm, err := s.aead()
	if err != nil {
		return err
	}
	nonce, err := hex.DecodeString(ent.Nonce)
	if err != nil {
		return errors.New("bad nonce")
	}
	if len(nonce) != gcm.NonceSize() {
		return errors.New("bad nonce length")
	}
	ct, err := hex.DecodeString(ent.Data)
	if err != nil {
		return errors.New("bad ciphertext")
	}
	plain, err := gcm.Open(nil, nonce, ct, []byte(s.slot(accountID)))
	if err != nil {
		return errors.New("sealing key does not match")
	}
	var st State
	if err := json.Unmarshal(plain, &st); err != nil {
		return err
	}
	*out = st.DeviceIdentity
	return nil
}

func (s *Store) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// flushLocked writes the file when it changed (caller holds s.mu).
func (s *Store) flushLocked() error {
	if !s.dirty {
		return s.lastErr
	}
	env := s.env
	env.Version = 1
	env.UpdatedAt = s.now().UTC().Format(time.RFC3339)
	env.DeviceID = s.process
	raw, err := json.MarshalIndent(env, "", " ")
	if err != nil {
		s.lastErr = err
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		s.lastErr = err
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		s.lastErr = err
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		s.lastErr = err
		return err
	}
	s.dirty = false
	s.lastErr = nil
	return nil
}

// Flush persists pending writes (exported for tests and for shutdown).
func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirty = true
	return s.flushLocked()
}
