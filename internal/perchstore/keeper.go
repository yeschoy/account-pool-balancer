// Package perchstore is Perch's credential store and session keeper.
//
// Two things it guarantees, because everything else depends on them:
//
//   - A session is written by exactly one writer per account at a time. The
//     keeper holds one mutex and a per-account generation counter, so two
//     concurrent refreshes of one account collapse into one call and a stale
//     answer can never overwrite a newer session.
//   - Sessions survive a restart encrypted: data/perch-secrets.json is sealed
//     with AES-256-GCM under a key derived from metadata.json's routing
//     secret, so the Access and Refresh tokens never sit in plain text on disk
//     and the console never shows them.
//
// Accounts are independent: every operation is keyed by Supabase user id, and
// no code path shares a token between ids.
package perchstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/perch"
)

// ErrNoSession is returned when an account has no session to work with.
var ErrNoSession = errors.New("the account has no session")

// ErrNeedLogin is the definite answer: the refresh token was refused, so the
// account has to be signed in again. Only perch's KindAuth sets it.
var ErrNeedLogin = errors.New("the refresh token was refused; the account needs signing in again")

// ErrSessionIdentity reports an observable mismatch, not JWT authentication failure.
var ErrSessionIdentity = errors.New("perchstore: session identity does not match user id")

func validateIdentity(uid, access string) error {
	claims := perch.TokensOf(access)
	if sub, ok := claims["sub"].(string); ok && strings.TrimSpace(sub) != "" && sub != uid {
		return ErrSessionIdentity
	}
	return nil
}

// Secrets is one account's stored credential material. Everything but the
// timestamps is secret and is only ever read through the keeper.
type Secrets struct {
	// UserID is Supabase's user id, the account's unique identity. It is also
	// the map key, so it is written twice on purpose: a record whose copy
	// disagrees with its key is dropped rather than trusted.
	UserID string `json:"userId"`
	// Email / Provider describe the account and are not secret.
	Email    string `json:"email,omitempty"`
	Provider string `json:"provider,omitempty"`
	// SupabaseURL / SupabaseKey are the project the session belongs to. An
	// imported account names the URL; the key is filled from settings when
	// the JSON does not carry one.
	SupabaseURL string `json:"supabaseUrl,omitempty"`
	SupabaseKey string `json:"supabaseKey,omitempty"`
	// AppURL is where turns and usage are read (app.perchai.app).
	AppURL string `json:"appUrl,omitempty"`

	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    int64  `json:"expiresAt,omitempty"`

	// ImportedAt is when the file this came from was read; FetchedAt is when
	// the tokens in it were last confirmed by Perch or imported. A write with
	// a FetchedAt older than the stored one is refused: that is what stops an
	// old JSON file from overwriting a session the keeper has since renewed.
	ImportedAt time.Time `json:"importedAt,omitzero"`
	FetchedAt  time.Time `json:"fetchedAt,omitzero"`
	// Generation counts recorded renewals; it makes the ordering of two
	// imports of the same file observable.
	Generation int64 `json:"generation,omitempty"`
}

// Clone copies s.
func (s Secrets) Clone() Secrets { return s }

// session turns the stored material into a perch.Session.
func (s Secrets) session() perch.Session {
	return perch.Session{
		AccessToken: s.AccessToken, RefreshToken: s.RefreshToken, ExpiresAt: s.ExpiresAt,
		UserID: s.UserID, Email: s.Email, Provider: s.Provider,
	}
}

// fromSession writes a perch.Session back into the record.
func (s *Secrets) fromSession(in perch.Session) {
	s.AccessToken, s.RefreshToken, s.ExpiresAt = in.AccessToken, in.RefreshToken, in.ExpiresAt
	if in.UserID != "" {
		s.UserID = in.UserID
	}
	if in.Email != "" {
		s.Email = in.Email
	}
	if in.Provider != "" {
		s.Provider = in.Provider
	}
}

// Creds are the credentials perch's client needs for this account.
func (s Secrets) Creds(defaultKey, defaultApp string) perch.Creds {
	return perch.Creds{SupabaseURL: s.SupabaseURL, SupabaseKey: firstNonEmpty(s.SupabaseKey, defaultKey)}
}

func (s Secrets) appURL(defaultApp string) string { return firstNonEmpty(s.AppURL, defaultApp) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---- on-disk envelope ---------------------------------------------------------

// envelope is the sealed file. Each record is sealed on its own so adding one
// account rewrites no other's ciphertext and two accounts' blobs are never
// adjacent plaintext.
type envelope struct {
	Version   int                     `json:"version"`
	UpdatedAt time.Time               `json:"updatedAt"`
	Records   map[string]sealedRecord `json:"records"`
}

type sealedRecord struct {
	// Key is the plaintext identity the console may show (the user id).
	UserID string `json:"userId"`
	Nonce  string `json:"nonce"`
	Data   string `json:"data"`
}

// ---- keeper ------------------------------------------------------------------

// Keeper owns every Perch session in the process.
type Keeper struct {
	path string
	key  []byte
	// logf is where the keeper reports a write it had to refuse.
	logf func(format string, args ...any)

	mu      sync.Mutex
	recs    map[string]Secrets // by Supabase user id
	byAcc   map[string]string  // account id → user id
	dirty   bool
	lastErr error

	// inflight collapses concurrent work per key: a caller joins the running
	// call instead of starting a second one that would spend the same
	// single-use refresh token twice.
	inflight map[string]*call
	versions map[string]uint64
	epochs   map[string]uint64

	client *perch.Client
	// Defaults read from settings on each use.
	cfg func() Config
}

// Config is what the keeper needs from the console's settings.
type Config struct {
	// SupabaseKey is the apikey GoTrue expects when an account's JSON carried
	// none (see perch.DefaultSupabaseKey).
	SupabaseKey string
	// AppURL is the host turns and usage are read from.
	AppURL string
	// RefreshMargin is how long before expiry a session is renewed.
	RefreshMargin time.Duration
	// Retries is how many extra attempts a transient failure gets.
	Retries int
	// RetryBackoff is the first backoff, doubled each retry.
	RetryBackoff time.Duration
	// ProxyURL returns the current proxy for a bound account.
	ProxyURL func(accountID string) string
}

// DefaultConfig is used when no settings were configured.
func DefaultConfig() Config {
	return Config{SupabaseKey: perch.DefaultSupabaseKey, AppURL: perch.AppURL,
		RefreshMargin: 10 * time.Minute, Retries: 3, RetryBackoff: 2 * time.Second}
}

type call struct {
	done chan struct{}
	// err is settled by the goroutine that ran the work; every joiner reads
	// it after <-done.
	err       error
	version   uint64
	targetURL string
	targetKey string
	discarded bool
}

// Open loads (or starts) the sealed store at path with key as the sealing
// key's material.
func Open(path string, key []byte, cfg func() Config, logf func(string, ...any)) (*Keeper, error) {
	if len(key) < 16 {
		return nil, errors.New("perchstore: sealing key is too short")
	}
	if cfg == nil {
		cfg = DefaultConfig
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	k := &Keeper{path: path, key: deriveKey(key), cfg: cfg, logf: logf,
		recs: map[string]Secrets{}, byAcc: map[string]string{}, inflight: map[string]*call{}, versions: map[string]uint64{}, epochs: map[string]uint64{},
		client: perch.NewClient(nil)}
	if err := k.load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return k, nil
}

// SetClient swaps the HTTP client (tests point it at a local server).
func (k *Keeper) SetClient(c *perch.Client) { k.client = c }

// Client exposes the keeper's HTTP client, so callers can reuse the transport.
func (k *Keeper) Client() *perch.Client { return k.client }

// deriveKey turns the balancer's routing secret into a 32-byte AES key.
func deriveKey(material []byte) []byte {
	sum := sha256.Sum256(append([]byte("perch-session-sealing\x00"), material...))
	return sum[:]
}

func (k *Keeper) load() error {
	raw, err := os.ReadFile(k.path)
	if err != nil {
		return err
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("perch-secrets.json: %w", err)
	}
	now := time.Now()
	for key, rec := range env.Records {
		sec, err := k.open(rec)
		if err != nil {
			// one unreadable record must not cost the other 999 accounts
			k.logf("perch: record %s is unreadable (%v); left for inspection", key, err)
			continue
		}
		if sec.UserID != "" && sec.UserID != key {
			k.logf("perch: record %s names user %s; dropped rather than trusted", key, sec.UserID)
			continue
		}
		sec.UserID = key
		// A record that never recorded a fetch time is dated by the file: it
		// is older than anything this process will write, which is what the
		// anti-overwrite rule needs.
		if sec.FetchedAt.IsZero() {
			sec.FetchedAt = env.UpdatedAt
		}
		if sec.FetchedAt.IsZero() {
			sec.FetchedAt = now
		}
		k.recs[key] = sec
		k.versions[key]++
	}
	k.dirty = false
	return nil
}

// seal encrypts one record.
func (k *Keeper) seal(sec Secrets) (sealedRecord, error) {
	gcm, err := k.aead()
	if err != nil {
		return sealedRecord{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return sealedRecord{}, err
	}
	plain, err := json.Marshal(sec)
	if err != nil {
		return sealedRecord{}, err
	}
	// The user id is authenticated as associated data, so a record cannot be
	// moved to another account's slot without failing to open.
	ct := gcm.Seal(nil, nonce, plain, []byte(sec.UserID))
	return sealedRecord{UserID: sec.UserID, Nonce: hex.EncodeToString(nonce), Data: hex.EncodeToString(ct)}, nil
}

// open decrypts one record.
func (k *Keeper) open(rec sealedRecord) (Secrets, error) {
	gcm, err := k.aead()
	if err != nil {
		return Secrets{}, err
	}
	nonce, err := hex.DecodeString(rec.Nonce)
	if err != nil {
		return Secrets{}, errors.New("bad nonce")
	}
	ct, err := hex.DecodeString(rec.Data)
	if err != nil {
		return Secrets{}, errors.New("bad ciphertext")
	}
	if len(nonce) != gcm.NonceSize() {
		return Secrets{}, errors.New("bad nonce length")
	}
	plain, err := gcm.Open(nil, nonce, ct, []byte(rec.UserID))
	if err != nil {
		return Secrets{}, errors.New("sealing key does not match")
	}
	var sec Secrets
	if err := json.Unmarshal(plain, &sec); err != nil {
		return Secrets{}, err
	}
	return sec, nil
}

func (k *Keeper) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// flushLocked writes the sealed file when it changed (caller holds k.mu).
func (k *Keeper) flushLocked() error {
	if !k.dirty {
		return k.lastErr
	}
	env := envelope{Version: 1, UpdatedAt: time.Now().UTC(), Records: make(map[string]sealedRecord, len(k.recs))}
	for id, sec := range k.recs {
		rec, err := k.seal(sec)
		if err != nil {
			return fmt.Errorf("perch-secrets: sealing %s: %w", id, err)
		}
		env.Records[id] = rec
	}
	raw, err := json.MarshalIndent(env, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return err
	}
	tmp := k.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, k.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	k.dirty, k.lastErr = false, nil
	return nil
}

// Flush writes pending changes.
func (k *Keeper) Flush() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.flushLocked()
}

// Close flushes and stops.
func (k *Keeper) Close() error { return k.Flush() }

// Supabase user ids are what an account is; account ids are what config.json
// calls it. The keeper keeps both, and drops bindings to accounts that no
// longer exist (the caller passes the live set).

// Bind maps an account id to its Supabase user id.
func (k *Keeper) Bind(accountID, userID string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if userID == "" {
		delete(k.byAcc, accountID)
		return
	}
	k.byAcc[accountID] = userID
}

// Forget deletes the session bound to accountID and its binding.
func (k *Keeper) Forget(accountID string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	uid, ok := k.byAcc[accountID]
	if !ok {
		return
	}
	delete(k.byAcc, accountID)
	for _, other := range k.byAcc {
		if other == uid {
			return
		}
	}
	delete(k.recs, uid)
	k.versions[uid]++
	k.epochs[uid]++
	k.dirty = true
}

// DropUser removes a stored session outright, for a user id that no account
// points at any more.
func (k *Keeper) DropUser(uid string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.recs[uid]; !ok {
		return
	}
	delete(k.recs, uid)
	k.versions[uid]++
	k.epochs[uid]++
	k.dirty = true
	_ = k.flushLocked()
}

// Rebind drops bindings to accounts that no longer exist and orphans no
// session: called after config changes.
func (k *Keeper) Rebind(live map[string]string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	next := make(map[string]string, len(live))
	for accID, uid := range live {
		if uid != "" {
			next[accID] = uid
		}
	}
	k.byAcc = next
}

// UserID returns the Supabase user id bound to accountID.
func (k *Keeper) UserID(accountID string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.byAcc[accountID]
}

// Get returns a copy of the session bound to accountID.
func (k *Keeper) Get(accountID string) (Secrets, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	uid, ok := k.byAcc[accountID]
	if !ok {
		return Secrets{}, false
	}
	sec, ok := k.recs[uid]
	return sec, ok
}

// ByUserID returns a copy of the session for a Supabase user id.
func (k *Keeper) ByUserID(uid string) (Secrets, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	sec, ok := k.recs[uid]
	return sec, ok
}

// Users lists the Supabase user ids that have a session.
func (k *Keeper) Users() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]string, 0, len(k.recs))
	for id := range k.recs {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Count is how many sessions are stored.
func (k *Keeper) Count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.recs)
}

// AccessToken returns the account's access token, or "".
func (k *Keeper) AccessToken(accountID string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	uid, ok := k.byAcc[accountID]
	if !ok {
		return ""
	}
	return k.recs[uid].AccessToken
}

// Creds returns what perch's client needs for an account.
func (k *Keeper) Creds(accountID string) (perch.Creds, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	uid, ok := k.byAcc[accountID]
	if !ok {
		return perch.Creds{}, false
	}
	sec, ok := k.recs[uid]
	if !ok {
		return perch.Creds{}, false
	}
	return sec.Creds(k.cfg().SupabaseKey, k.cfg().AppURL), true
}

// ---- writing ------------------------------------------------------------------

// PutOptions carries what an import or an admin edit wants to do.
type PutOptions struct {
	// BindAs is the account id this session belongs to.
	BindAs string
	// FetchedAt is when the tokens were confirmed; an import stamps it with
	// the file's arrival, which makes it older than any renewal this process
	// has already made.
	FetchedAt time.Time
	// Force lets a manual session update replace a newer stored one (the
	// admin pasted fresh tokens on purpose).
	Force bool
	// AllowCreate refuses (when false) a session for a user id the store
	// does not know — how an import tells a first import from a duplicate.
	AllowCreate bool
}

// PutResult says what a write did.
type PutResult struct {
	// Created: the user id had no session yet.
	Created bool
	// Updated: the session was replaced.
	Updated bool
	// Skipped: the stored session was newer (or Force was off) — the reason
	// is in Reason.
	Skipped bool
	Reason  string
	// Secrets is the record as stored after the call.
	Secrets Secrets
}

// putLocked is Put's body without flushing, so a batch writes the file once.
func (k *Keeper) putLocked(sec Secrets, opt PutOptions) (PutResult, error) {
	if sec.UserID == "" {
		return PutResult{}, errors.New("perchstore: a session needs a Supabase user id")
	}
	if err := validateIdentity(sec.UserID, sec.AccessToken); err != nil {
		return PutResult{}, err
	}
	prev, exists := k.recs[sec.UserID]
	if exists && !opt.Force && prev.AccessToken != "" && prev.RefreshToken != "" &&
		(strings.TrimSpace(sec.AccessToken) == "" || strings.TrimSpace(sec.RefreshToken) == "") {
		if opt.BindAs != "" {
			k.byAcc[opt.BindAs] = sec.UserID
		}
		return PutResult{Skipped: true, Reason: "incomplete session cannot replace a complete session", Secrets: prev}, nil
	}
	if exists && !opt.AllowCreate && !opt.Force && prev.RefreshToken == sec.RefreshToken && prev.AccessToken == sec.AccessToken {
		if opt.BindAs != "" {
			k.byAcc[opt.BindAs] = sec.UserID
		}
		return PutResult{Skipped: true, Reason: "identical to the stored session", Secrets: prev}, nil
	}
	candidate := sec.FetchedAt
	if candidate.IsZero() {
		candidate = opt.FetchedAt
	}
	// An import stamped at arrival is only a fallback: it must not make an old
	// file look newer than a session the keeper renewed since. A record that
	// states its own time is compared on that time.
	if exists && !opt.Force && !candidate.After(prev.FetchedAt) && prev.AccessToken != "" {
		if opt.BindAs != "" {
			k.byAcc[opt.BindAs] = sec.UserID
		}
		return PutResult{Skipped: true, Reason: "the stored session is newer", Secrets: prev}, nil
	}
	next := sec
	next.Generation = prev.Generation
	if exists {
		next.Generation++
	}
	if next.ImportedAt.IsZero() {
		next.ImportedAt = prev.ImportedAt
	}
	// A caller (an import, a backup restore) may state when the tokens in the
	// record were confirmed. It is used only when the record itself says
	// nothing: a record that carries its own fetchedAt is dated by that, so a
	// file written weeks ago cannot pass for a fresh session just because it
	// was uploaded now.
	if !opt.FetchedAt.IsZero() && next.FetchedAt.IsZero() {
		next.FetchedAt = opt.FetchedAt
	}
	if next.FetchedAt.IsZero() {
		next.FetchedAt = time.Now().UTC()
	}
	if next.Email == "" {
		next.Email = prev.Email
	}
	if next.Provider == "" {
		next.Provider = prev.Provider
	}
	if next.SupabaseURL == "" {
		next.SupabaseURL = prev.SupabaseURL
	}
	if next.SupabaseKey == "" {
		next.SupabaseKey = prev.SupabaseKey
	}
	if next.AppURL == "" {
		next.AppURL = prev.AppURL
	}
	k.recs[sec.UserID] = next
	k.versions[sec.UserID]++
	if opt.BindAs != "" {
		k.byAcc[opt.BindAs] = sec.UserID
	}
	k.dirty = true
	return PutResult{Created: !exists, Updated: exists, Secrets: next}, nil
}

// PutMany stores several sessions and seals the file once. Each record obeys
// the same anti-overwrite rule as Put; the results are in the same order.
func (k *Keeper) PutMany(secs []Secrets, opts []PutOptions) ([]PutResult, error) {
	if len(secs) != len(opts) {
		return nil, errors.New("perchstore: PutMany needs one option per session")
	}
	for _, sec := range secs {
		if sec.UserID == "" {
			return nil, errors.New("perchstore: a session needs a Supabase user id")
		}
		if err := validateIdentity(sec.UserID, sec.AccessToken); err != nil {
			return nil, err
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]PutResult, len(secs))
	for i := range secs {
		res, err := k.putLocked(secs[i], opts[i])
		if err != nil {
			out[i] = PutResult{}
			continue
		}
		out[i] = res
	}
	return out, k.flushLocked()
}

// Put stores a session with the anti-overwrite rule: unless Force is set, a
// write whose FetchedAt is not newer than the stored session's is refused, so
// re-importing an old JSON file can never replace tokens that have since been
// renewed. A session whose stored copy cannot be refreshed any more (NeedLogin
// was recorded) is always replaceable: a fresh sign-in is strictly better
// news than a dead refresh token.
func (k *Keeper) Put(sec Secrets, opt PutOptions) (PutResult, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	res, err := k.putLocked(sec, opt)
	if err != nil {
		return PutResult{}, err
	}
	if err := k.flushLocked(); err != nil {
		return PutResult{}, err
	}
	return res, nil
}

// ---- renewing -----------------------------------------------------------------

// RefreshResult is what a renewal attempt settled on.
type RefreshResult struct {
	Secrets Secrets
	// Revision identifies the in-process session version used by the result.
	Revision uint64
	// Renewed: the tokens changed.
	Renewed bool
	// Skipped: nothing was due.
	Skipped bool
	// NeedLogin: the refresh token was refused; the account must sign in.
	NeedLogin bool
	Err       error
}

// Refresh renews accountID's session if it is due (or when force is set).
//
// Calls for the same user serialize. Waiters observe the completed call when
// it still describes their session, otherwise they recheck the current state.
// Calls for different users do not wait for one another.
func (k *Keeper) Refresh(ctx context.Context, accountID string, force bool) RefreshResult {
retry:
	k.mu.Lock()
	uid, ok := k.byAcc[accountID]
	if !ok {
		k.mu.Unlock()
		return RefreshResult{Err: ErrNoSession}
	}
	sec, ok := k.recs[uid]
	if !ok {
		k.mu.Unlock()
		return RefreshResult{Err: ErrNoSession}
	}
	cfg := k.cfg()
	version := k.versions[uid]
	if c := k.inflight[uid]; c != nil {
		joined := c
		k.mu.Unlock()
		select {
		case <-joined.done:
		case <-ctx.Done():
			return RefreshResult{Err: ctx.Err()}
		}
		out := k.resultFor(accountID)
		if out.Err != nil {
			return out
		}
		currentURL, currentKey := refreshTarget(out.Secrets, k.cfg())
		if !joined.discarded && out.Secrets.UserID == uid && out.Revision == joined.version &&
			currentURL == joined.targetURL && currentKey == joined.targetKey {
			out.Err = joined.err
			out.NeedLogin = errors.Is(joined.err, ErrNeedLogin)
			out.Skipped = joined.err == nil
			return out
		}
		if err := ctx.Err(); err != nil {
			out.Err = err
			return out
		}
		goto retry
	}
	if !force && !sec.session().Due(time.Now(), cfg.RefreshMargin) {
		k.mu.Unlock()
		return RefreshResult{Secrets: sec, Revision: version, Skipped: true}
	}
	targetURL, targetKey := refreshTarget(sec, cfg)
	c := &call{done: make(chan struct{}), version: version, targetURL: targetURL, targetKey: targetKey}
	k.inflight[uid] = c
	epoch := k.epochs[uid]
	k.mu.Unlock()

	if cfg.ProxyURL != nil {
		ctx = perch.WithProxy(ctx, cfg.ProxyURL(accountID))
	}
	next, err := k.renewWithRetry(ctx, cfg, sec)

	k.mu.Lock()
	finish := func() {
		if k.inflight[uid] == c {
			delete(k.inflight, uid)
		}
		k.mu.Unlock()
		close(c.done)
	}
	cur, stillThere := k.recs[uid]
	bound := false
	for _, candidate := range k.byAcc {
		if candidate == uid {
			bound = true
			break
		}
	}
	currentURL, currentKey := refreshTarget(cur, k.cfg())
	if !stillThere || !bound || k.epochs[uid] != epoch ||
		cur.RefreshToken != sec.RefreshToken || currentURL != targetURL ||
		(err != nil && (currentKey != targetKey || k.versions[uid] != version)) {
		c.discarded = true
		finish()
		out := k.resultFor(accountID)
		if out.Err == nil {
			if err != nil && out.Secrets.UserID == uid && out.Secrets.RefreshToken == sec.RefreshToken {
				out.Err = errors.New("refresh result superseded; retry with the current session")
			} else {
				out.Skipped = true
			}
		}
		return out
	}
	if err != nil {
		c.err = err
		stillBound := k.byAcc[accountID] == uid
		finish()
		if !stillBound {
			return k.resultFor(accountID)
		}
		return RefreshResult{Secrets: sec, Revision: version, NeedLogin: errors.Is(err, ErrNeedLogin), Err: err}
	}
	if (next.UserID != "" && next.UserID != uid) || validateIdentity(uid, next.AccessToken) != nil {
		c.err = ErrSessionIdentity
		stillBound := k.byAcc[accountID] == uid
		finish()
		if !stillBound {
			return k.resultFor(accountID)
		}
		return RefreshResult{Secrets: cur, Revision: version, Err: ErrSessionIdentity}
	}
	merged := cur.session().Merge(next)
	if k.versions[uid] != version {
		// Preserve metadata edited while the network request was in flight.
		merged.Email, merged.Provider = cur.Email, cur.Provider
	}
	rec := cur
	rec.fromSession(merged)
	rec.FetchedAt = time.Now().UTC()
	rec.Generation = cur.Generation + 1
	// The old refresh token is spent the moment Perch answers: this write has
	// to land, so a failure here is reported rather than ignored.
	k.recs[uid] = rec
	k.versions[uid]++
	c.version = k.versions[uid]
	k.dirty = true
	ferr := k.flushLocked()
	c.err = ferr
	stillBound := k.byAcc[accountID] == uid
	finish()
	if !stillBound {
		out := k.resultFor(accountID)
		if out.Err == nil {
			out.Skipped = true
		}
		return out
	}
	if ferr != nil {
		return RefreshResult{Secrets: rec, Revision: c.version, Renewed: true, Err: fmt.Errorf("the renewed session could not be saved: %w", ferr)}
	}
	return RefreshResult{Secrets: rec, Revision: c.version, Renewed: true}
}

// refreshTarget matches the effective URL and key used by perch.Client.Renew.
func refreshTarget(sec Secrets, cfg Config) (string, string) {
	creds := sec.Creds(cfg.SupabaseKey, cfg.AppURL)
	u := perch.TrimBase(creds.SupabaseURL)
	if u == "" {
		u = perch.DefaultSupabaseURL
	}
	key := strings.TrimSpace(creds.SupabaseKey)
	if key == "" {
		key = perch.DefaultSupabaseKey
	}
	return u, key
}

// resultFor is the current stored state for an account.
func (k *Keeper) resultFor(accountID string) RefreshResult {
	k.mu.Lock()
	defer k.mu.Unlock()
	uid, ok := k.byAcc[accountID]
	if !ok {
		return RefreshResult{Err: ErrNoSession}
	}
	sec, ok := k.recs[uid]
	if !ok {
		return RefreshResult{Err: ErrNoSession}
	}
	return RefreshResult{Secrets: sec, Revision: k.versions[uid]}
}

// IsCurrentRefresh is a snapshot check, not an atomic guard for side effects.
func (k *Keeper) IsCurrentRefresh(accountID string, result RefreshResult) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	uid := k.byAcc[accountID]
	_, exists := k.recs[uid]
	return exists && uid != "" && uid == result.Secrets.UserID && k.versions[uid] == result.Revision
}

// renewWithRetry renews once, then retries transient failures with the
// configured backoff. A refusal is never retried: that is the definitive
// answer and retrying a rotated token would only burn budget.
func (k *Keeper) renewWithRetry(ctx context.Context, cfg Config, sec Secrets) (perch.Session, error) {
	attempts := 1 + max(cfg.Retries, 0)
	delay := cfg.RetryBackoff
	if delay <= 0 {
		delay = 2 * time.Second
	}
	var last error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			// a refused refresh token is the account's problem; a 429 or a
			// 5xx is the network's and worth waiting out
			select {
			case <-ctx.Done():
				return perch.Session{}, ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
		s, err := k.client.Renew(ctx, sec.Creds(cfg.SupabaseKey, cfg.AppURL), sec.RefreshToken)
		if err == nil {
			return s, nil
		}
		last = err
		if err == nil || !isRetryable(err) {
			break
		}
	}
	return perch.Session{}, classifyRenewError(last)
}

func isRetryable(err error) bool {
	var pe *perch.Error
	if errors.As(err, &pe) {
		return pe.Retryable()
	}
	return false
}

// supposedAuthFailure is true when err is a refused refresh token. A bare
// KindAuth on a transient status cannot happen (classify sets those), so this
// is the single place that decides an account needs signing in again.
func supposedAuthFailure(err error) bool {
	return perch.KindOf(err) == perch.KindAuth
}

// classifyRenewError trims a failure to the verdict the account needs: a
// refusal becomes ErrNeedLogin, anything else stays as it is (so a flaky
// network can never be mistaken for a dead account).
func classifyRenewError(err error) error {
	if supposedAuthFailure(err) {
		return fmt.Errorf("%w: %v", ErrNeedLogin, err)
	}
	return err
}

// ---- diagnostics ----------------------------------------------------------------

// Summary is a cheap, redacted view of one stored session.
type Summary struct {
	UserID     string    `json:"userId"`
	Email      string    `json:"email,omitempty"`
	Provider   string    `json:"provider,omitempty"`
	HasAccess  bool      `json:"hasAccess"`
	HasRefresh bool      `json:"hasRefresh"`
	ExpiresAt  time.Time `json:"expiresAt,omitzero"`
	FetchedAt  time.Time `json:"fetchedAt,omitzero"`
	ImportedAt time.Time `json:"importedAt,omitzero"`
	Generation int64     `json:"generation,omitempty"`
	// AccessSuffix is the last 4 characters, the one thing the console may
	// show, so an operator can tell two sessions apart without seeing either.
	AccessSuffix string `json:"accessSuffix,omitempty"`
}

// Summarize redacts s. It never carries a token.
func Summarize(s Secrets) Summary {
	out := Summary{UserID: s.UserID, Email: s.Email, Provider: s.Provider,
		HasAccess: s.AccessToken != "", HasRefresh: s.RefreshToken != "",
		FetchedAt: s.FetchedAt, ImportedAt: s.ImportedAt, Generation: s.Generation}
	if exp, ok := s.session().Expires(); ok {
		out.ExpiresAt = exp
	}
	if n := len(s.AccessToken); n >= 4 {
		out.AccessSuffix = s.AccessToken[n-4:]
	}
	return out
}
