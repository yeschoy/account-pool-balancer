package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perchstore"
)

// Perch backup and restore.
//
// A backup is the whole pool: every session plus the account metadata that
// gives them meaning (id, email, group, tags, enabled). It is sealed with a
// passphrase the operator chooses — deliberately not the keeper's own key,
// because the point of a backup is to survive losing the data directory.

const (
	// backupIter/mode are recorded in the file so a later release can raise
	// them without orphaning old backups.
	backupIter = 210000
	backupInfo = "perch-pool-backup"
)

// backupEnvelope is the file on disk (or the JSON body).
type backupEnvelope struct {
	SchemaVersion string    `json:"schema_version"`
	Provider      string    `json:"provider"`
	CreatedAt     time.Time `json:"created_at"`
	// KDF is what the passphrase was stretched with.
	KDF   string `json:"kdf"`
	Iter  int    `json:"iter"`
	Salt  string `json:"salt"`
	Nonce string `json:"nonce"`
	Data  string `json:"data"`
	// Accounts is the plaintext inventory: how many are inside. It leaks no
	// credential and lets a restore say what it found.
	Accounts int `json:"accounts"`
	// Note reminds whoever opens the file what it holds.
	Note string `json:"note"`
}

// backupRecord is one account's entry inside the sealed payload. It carries
// the session's secrets, so only the sealed payload holds it.
type backupRecord struct {
	perchstore.Secrets
	AccountID string   `json:"accountId"`
	Name      string   `json:"name,omitempty"`
	Group     string   `json:"group,omitempty"`
	Enabled   bool     `json:"enabled"`
	Tags      []string `json:"tags,omitempty"`
	Note      string   `json:"note,omitempty"`
	Weight    int      `json:"weight,omitempty"`
	Priority  int      `json:"priority,omitempty"`
}

type backupPayload struct {
	Version  int            `json:"version"`
	Created  time.Time      `json:"createdAt"`
	Records  []backupRecord `json:"records"`
	Settings *config.Perch  `json:"settings,omitempty"`
}

// backupPerch seals every session and the metadata around it.
func (s *Server) backupPerch(passphrase string) (backupEnvelope, error) {
	if s.perch == nil {
		return backupEnvelope{}, errors.New("the perch pool is not running")
	}
	cfg := s.cfg.Get()
	payload := backupPayload{Version: 1, Created: time.Now().UTC()}
	perchCfg := cfg.Perch
	// the Supabase key is a credential too: it is inside the sealed payload
	// and never in the envelope
	payload.Settings = &perchCfg
	for _, a := range cfg.Accounts {
		if a.Provider != config.ProviderPerch {
			continue
		}
		sec, ok := s.perch.keeper.Get(a.ID)
		if !ok {
			// an account with no session still belongs in the backup: its
			// group and tags are what an operator would miss
			sec = perchstore.Secrets{UserID: a.PerchUserID}
		}
		payload.Records = append(payload.Records, backupRecord{
			Secrets: sec, AccountID: a.ID, Name: a.Name, Group: a.Group, Enabled: a.Enabled,
			Tags: a.PerchTags, Note: a.Note, Weight: a.Weight, Priority: a.Priority,
		})
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return backupEnvelope{}, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return backupEnvelope{}, err
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, backupIter, 32)
	if err != nil {
		return backupEnvelope{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return backupEnvelope{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return backupEnvelope{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return backupEnvelope{}, err
	}
	sealed := gcm.Seal(nil, nonce, plain, []byte(backupInfo))
	return backupEnvelope{
		SchemaVersion: "1.0", Provider: "perchai", CreatedAt: time.Now().UTC(),
		KDF: "pbkdf2-sha256", Iter: backupIter,
		Salt: hex.EncodeToString(salt), Nonce: hex.EncodeToString(nonce),
		Data: hex.EncodeToString(sealed), Accounts: len(payload.Records),
		Note: "Perch 账号池加密备份：包含 Session（Access Token / Refresh Token）与账号元数据，口令是唯一恢复途径。",
	}, nil
}

// restorePerch opens a backup and merges it in. Sessions the pool already has
// are only replaced when the backup's are newer, so restoring an old backup
// cannot undo a renewal made since.
func (s *Server) restorePerch(passphrase string, raw []byte) (created, updated, skipped int, errs []string, err error) {
	if s.perch == nil {
		return 0, 0, 0, nil, errors.New("the perch pool is not running")
	}
	var env backupEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, 0, 0, nil, fmt.Errorf("不是有效的备份文件：%v", err)
	}
	if env.KDF != "pbkdf2-sha256" || env.Iter < 10000 {
		return 0, 0, 0, nil, errors.New("备份的加密参数不受支持")
	}
	salt, err1 := hex.DecodeString(env.Salt)
	nonce, err2 := hex.DecodeString(env.Nonce)
	ct, err3 := hex.DecodeString(env.Data)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, nil, errors.New("备份文件已损坏")
	}
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, env.Iter, 32)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	plain, err := gcm.Open(nil, nonce, ct, []byte(backupInfo))
	if err != nil {
		return 0, 0, 0, nil, errors.New("口令不对，或备份文件被改动过")
	}
	var payload backupPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return 0, 0, 0, nil, errors.New("备份内容无法解析")
	}
	cfg := s.cfg.Get()
	byUser := map[string]config.Account{}
	for _, a := range cfg.Accounts {
		if a.Provider == config.ProviderPerch && a.PerchUserID != "" {
			byUser[a.PerchUserID] = a
		}
	}
	for _, rec := range payload.Records {
		if rec.UserID == "" {
			errs = append(errs, "备份中有一条记录缺少 Supabase User ID，已跳过")
			continue
		}
		if rec.AccessToken == "" && rec.RefreshToken == "" {
			skipped++
			continue
		}
		if a, exists := byUser[rec.UserID]; exists {
			res, perr := s.perch.keeper.Put(rec.Secrets, perchstore.PutOptions{
				BindAs: a.ID, FetchedAt: rec.FetchedAt,
			})
			if perr != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", rec.UserID, perr))
				continue
			}
			if res.Skipped {
				skipped++
			} else {
				updated++
				s.afterPerchSession(a.ID)
			}
			continue
		}
		// a user the pool does not hold: bring the account back too
		id := config.NewID("acc_")
		if _, perr := s.perch.keeper.Put(rec.Secrets, perchstore.PutOptions{
			BindAs: id, FetchedAt: rec.FetchedAt, AllowCreate: true, Force: true,
		}); perr != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", rec.UserID, perr))
			continue
		}
		name := rec.Name
		if name == "" {
			name = rec.Email
		}
		if name == "" {
			name = rec.UserID
		}
		group := rec.Group
		if group == "" {
			group = config.GroupForProvider(config.ProviderPerch)
		}
		a := config.Account{
			ID: id, Provider: config.ProviderPerch, Name: name, Email: rec.Email,
			UserID: rec.UserID, PerchUserID: rec.UserID, Group: group, Enabled: rec.Enabled,
			PerchTags:  rec.Tags,
			Note:       rec.Note,
			Weight:     rec.Weight,
			Priority:   rec.Priority,
			RuntimeKey: config.PerchKeyMarker, CreatedAt: time.Now(),
		}
		if _, perr := s.cfg.Update(-1, func(c *config.Config) error {
			for _, other := range c.Accounts {
				if other.PerchUserID == rec.UserID {
					return fmt.Errorf("Supabase 用户 %s 已存在", rec.UserID)
				}
			}
			c.Accounts = append(c.Accounts, a)
			return nil
		}); perr != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", rec.UserID, perr))
			continue
		}
		created++
		s.afterPerchSession(id)
	}
	// settings travel with the backup, but the Supabase key stays as given
	if payload.Settings != nil {
		_, _ = s.cfg.Update(-1, func(c *config.Config) error {
			keepKey := c.Perch.SupabaseKey
			c.Perch = *payload.Settings
			if c.Perch.SupabaseKey == "" {
				c.Perch.SupabaseKey = keepKey
			}
			return nil
		})
	}
	s.perch.sched.Wake()
	return created, updated, skipped, errs, nil
}

// prunePerchLogs removes Perch events older than a cutoff, keeping the rest.
// The log store already has a retention window; this is the explicit "clean
// up now" an operator may ask for, and it refuses a cutoff in the future.
func (s *Server) prunePerchLogs(before string) (int, error) {
	cut, err := time.Parse(time.RFC3339, before)
	if err != nil {
		cut, err = time.Parse("2006-01-02", before)
	}
	if err != nil {
		return 0, errors.New("时间格式应为 2006-01-02 或 RFC3339")
	}
	if cut.After(time.Now()) {
		return 0, errors.New("清理时间点不能在未来")
	}
	if s.logs == nil || !s.logs.Ready() {
		return 0, errors.New("日志还在加载中")
	}
	// Read everything the store still holds, bucket by cutoff, then rewrite:
	// Clear + re-append is the only rewrite the store offers, so the
	// survivors are collected first and put back newest-first order intact.
	var keep []perchLogRecord
	removed, dropped := 0, 0
	var cursor int64
	for page := 0; ; page++ {
		p, err := s.logs.Query("perch", nil, time.Time{}, time.Time{}, 200, cursor)
		if err != nil {
			return removed, err
		}
		if len(p.Items) == 0 {
			break
		}
		for _, raw := range p.Items {
			var rec perchLogRecord
			if json.Unmarshal(raw, &rec) != nil {
				continue
			}
			if rec.TS.Before(cut) {
				removed++
				continue
			}
			keep = append(keep, rec)
		}
		if p.NextCursor == "" || p.NextCursor == fmt.Sprint(cursor) {
			break
		}
		next, perr := strconv.ParseInt(p.NextCursor, 10, 64)
		if perr != nil {
			break
		}
		cursor = next
		if page > 500 { // the retention window bounds this anyway
			break
		}
	}
	if removed == 0 {
		return 0, nil
	}
	if err := s.logs.Clear("perch"); err != nil {
		return removed, err
	}
	for i := len(keep) - 1; i >= 0; i-- {
		rec := keep[i]
		s.logs.Append("perch", rec.TS, &rec)
	}
	if dropped > 0 {
		return removed - dropped, nil
	}
	return removed, nil
}
