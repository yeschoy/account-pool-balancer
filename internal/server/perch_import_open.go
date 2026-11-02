package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perchimport"
)

// The open Perch import endpoint.
//
// A sign-up bot produces the account export directly, so this endpoint takes
// that format rather than an API key: a Perch account IS a Supabase session,
// and nothing else identifies it. It shares the manual import's pipeline
// (perchimport.ParseJSON → planPerchImport → applyPerchImport), so the rules an
// operator relies on hold here too:
//
//   - a Supabase user id is the identity: importing it again is a duplicate,
//     never a second account;
//   - an older session never overwrites a newer one (the store enforces
//     fetchedAt again on write);
//   - one bad document costs only itself.
//
// Authentication reuses the existing import keys, so a bot needs one key and
// nothing else, and the same failed-auth rate limit applies.

const (
	// maxPerchImportBatch bounds one request; a bot batching more should
	// split it so a single failure costs less.
	maxPerchImportBatch = 200
)

// perchImportRequest is one document or a batch of them.
//
//   - session: the export format exactly as the CLI writes it
//   - accounts: several of them in one call
//   - flattened fields (supabaseUserId / accessToken / …) are accepted too,
//     because a bot generating them itself is easier to write that way
type perchImportRequest struct {
	// A single export document, inline.
	PerchImportDoc
	// Accounts is the batch form; each entry may be a full export or the
	// flattened shape.
	Accounts []json.RawMessage `json:"accounts"`

	// Group overrides the key's default group for this call only.
	Group string `json:"group"`
	// Name is a console label override (defaults to the account's email).
	Name string `json:"name"`
	// Tags are attached to the imported accounts.
	Tags []string `json:"tags"`
}

// PerchImportDoc is one account: either the export shape (account/session/
// auth_context) or the same fields flattened onto the object.
type PerchImportDoc struct {
	// ---- export shape ----
	SchemaVersion string `json:"schema_version"`
	Provider      string `json:"provider"`
	AuthProvider  string `json:"auth_provider"`
	Account       *struct {
		SupabaseUserID string  `json:"supabase_user_id"`
		Email          string  `json:"email"`
		GitHubUserID   *string `json:"github_user_id"`
		RegisteredAt   string  `json:"registered_at"`
	} `json:"account"`
	Session *struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
	} `json:"session"`
	AuthContext *struct {
		SupabaseURL     string  `json:"supabase_url"`
		WorkspaceID     *string `json:"workspace_id"`
		SupabaseAnonKey string  `json:"supabase_anon_key"`
	} `json:"auth_context"`

	// ---- flattened shape ----
	SupabaseUserID string `json:"supabaseUserId"`
	UserID         string `json:"userId"`
	Email          string `json:"email"`
	AccessToken    string `json:"accessToken"`
	RefreshToken   string `json:"refreshToken"`
	ExpiresAt      int64  `json:"expiresAt"`
	SupabaseURL    string `json:"supabaseUrl"`
	SupabaseKey    string `json:"supabaseAnonKey"`
	// SessionJSON is an export document as a string, for a bot that holds the
	// original file and would rather post it verbatim.
	SessionJSON string `json:"sessionJson"`
}

// raw is the document re-serialised in the export shape, so one parser reads
// every accepted form.
func (d PerchImportDoc) raw() ([]byte, error) {
	if strings.TrimSpace(d.SessionJSON) != "" {
		return []byte(d.SessionJSON), nil
	}
	out := map[string]any{
		"schema_version": firstNonEmpty(d.SchemaVersion, "1.0"),
		"provider":       firstNonEmpty(d.Provider, "perchai"),
		"auth_provider":  firstNonEmpty(d.AuthProvider, "github"),
	}
	userID := firstNonEmpty(d.SupabaseUserID, d.UserID)
	email := d.Email
	access, refresh, expires := d.AccessToken, d.RefreshToken, d.ExpiresAt
	sbURL, sbKey := d.SupabaseURL, d.SupabaseKey
	if d.Account != nil {
		userID = firstNonEmpty(d.Account.SupabaseUserID, userID)
		email = firstNonEmpty(d.Account.Email, email)
	}
	if d.Session != nil {
		access = firstNonEmpty(d.Session.AccessToken, access)
		refresh = firstNonEmpty(d.Session.RefreshToken, refresh)
		if d.Session.ExpiresAt > 0 {
			expires = d.Session.ExpiresAt
		}
	}
	if d.AuthContext != nil {
		sbURL = firstNonEmpty(d.AuthContext.SupabaseURL, sbURL)
		sbKey = firstNonEmpty(d.AuthContext.SupabaseAnonKey, sbKey)
	}
	if userID == "" && access == "" && refresh == "" {
		return nil, errors.New("no account could be read from this entry")
	}
	session := map[string]any{"access_token": access, "refresh_token": refresh}
	if expires > 0 {
		session["expires_at"] = expires
	}
	out["account"] = map[string]any{"supabase_user_id": userID, "email": email}
	out["session"] = session
	out["auth_context"] = map[string]any{"supabase_url": sbURL, "supabase_anon_key": sbKey}
	data, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// apiPerchOpenImport is POST /open/v1/perch/import.
func (s *Server) apiPerchOpenImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ip := clientIP(r)
	if s.openAuth.limited(ip) {
		w.Header().Set("Retry-After", "60")
		apiError(w, http.StatusTooManyRequests, errors.New("too many failed authentications, retry in 60s"))
		return
	}
	ik, ok := s.importKeyFromRequest(r)
	if !ok {
		s.openAuth.fail(ip)
		w.Header().Set("WWW-Authenticate", `Bearer realm="account-pool-balancer perch import"`)
		apiError(w, http.StatusUnauthorized, errors.New("invalid or missing import key"))
		return
	}
	// an import key scoped to another pool must not create Perch accounts
	pool := importKeyPool(s.cfg.Get(), ik)
	if pool != "" && pool != config.ProviderPerch {
		apiError(w, http.StatusForbidden, fmt.Errorf("this import key belongs to the %s pool", pool))
		return
	}

	var in perchImportRequest
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	// Decode each batch entry on its own: a bad entry becomes that entry's
	// error, the rest of the batch still imports.
	var docs []PerchImportDoc
	entryErrs := map[int]string{}
	for i, raw := range in.Accounts {
		var doc PerchImportDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			entryErrs[i] = "entry is not a Perch account object: " + err.Error()
			docs = append(docs, PerchImportDoc{})
			continue
		}
		docs = append(docs, doc)
	}
	// a single document posted at the top level
	if len(docs) == 0 && !in.PerchImportDoc.isZero() {
		docs = []PerchImportDoc{in.PerchImportDoc}
	}
	if len(docs) == 0 {
		apiError(w, http.StatusBadRequest, errors.New(`no account in the request: send an export document, or {"accounts":[…]}`))
		return
	}
	if len(docs) > maxPerchImportBatch {
		apiError(w, http.StatusBadRequest, fmt.Errorf("at most %d accounts per request", maxPerchImportBatch))
		return
	}

	// 1) parse each entry on its own, gathering the ones that are readable
	// Labels must be unique within the batch: the batch is read through a map
	// keyed by name, so two entries naming the same Supabase user id would
	// otherwise collapse into one and a duplicate would go unnoticed.
	files := map[string][]byte{}
	labels := make([]string, len(docs))
	taken := map[string]int{}
	failures := make([]perchimport.FileError, 0)
	for i, doc := range docs {
		label := entryLabel(i, doc)
		if n := taken[label]; n > 0 {
			label = fmt.Sprintf("%s#%d", label, n)
		}
		taken[entryLabel(i, doc)]++
		labels[i] = label
		if reason, bad := entryErrs[i]; bad {
			failures = append(failures, perchimport.FileError{File: label, Reason: reason})
			continue
		}
		data, err := doc.raw()
		if err != nil {
			failures = append(failures, perchimport.FileError{File: label, Reason: err.Error()})
			continue
		}
		files[label] = data
	}
	parsed := perchimport.ReadFiles(files)
	allFailed := append(failures, parsed.Failed...)

	// 2) plan and apply with the same pipeline the console uses
	group := strings.TrimSpace(in.Group)
	if group == "" {
		group = ik.Group
	}
	if group == "" || group == config.DefaultGroup {
		group = config.GroupForProvider(config.ProviderPerch)
	}
	sum, _ := s.importPerch(parsed, group, nil)
	// tags and a name override are per-call conveniences the manual import
	// does not need, so they are applied afterwards
	if len(in.Tags) > 0 || strings.TrimSpace(in.Name) != "" {
		s.applyPerchImportExtras(sum.Outcomes, in.Tags, in.Name)
	}

	batch := config.NewID("imp_")
	s.logPerchBatch(batch, sum, fmt.Sprintf("开放接口导入：%d 个账号，创建 %d、更新 %d、重复 %d、跳过 %d、失败 %d",
		len(docs), sum.Created, sum.Updated, sum.Duplicate, sum.Skipped, sum.Failed))

	// 3) answer in the open endpoint's shape: counts plus a per-entry result
	results := make([]map[string]any, 0, len(docs))
	byFile := map[string]perchimport.Outcome{}
	for _, o := range sum.Outcomes {
		byFile[o.File] = o
	}
	for i := range docs {
		label := labels[i]
		if reason, bad := entryErrs[i]; bad {
			results = append(results, map[string]any{"index": i, "status": "error", "error": reason})
			continue
		}
		if o, ok := byFile[label]; ok {
			results = append(results, map[string]any{
				"index": i, "status": mapImportStatus(o.Status), "accountId": o.AccountID,
				"userId": o.UserID, "email": o.Email, "error": o.Reason,
			})
			continue
		}
		reason := "unknown entry"
		for _, f := range allFailed {
			if f.File == label {
				reason = f.Reason
			}
		}
		results = append(results, map[string]any{"index": i, "status": "error", "error": reason})
	}
	// Counts come from the per-entry results, not from the summary: an entry
	// that failed to parse never reached the planner, so the summary cannot
	// see it.
	var added, updated, duplicate, failed int
	for _, r := range results {
		switch r["status"] {
		case "added":
			added++
		case "updated":
			updated++
		case "unchanged":
			duplicate++
		default:
			failed++
		}
	}
	status := http.StatusOK
	if added+updated+duplicate == 0 {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, map[string]any{
		"batch": batch, "total": len(docs),
		"added": added, "updated": updated, "duplicate": duplicate,
		"skipped": 0, "failed": failed,
		"results": results,
	})
}

// isZero reports an entry that carried no account at all.
func (d PerchImportDoc) isZero() bool {
	if d.Account != nil || d.Session != nil || d.AuthContext != nil {
		return false
	}
	return d.SupabaseUserID == "" && d.UserID == "" && d.AccessToken == "" &&
		d.RefreshToken == "" && d.SessionJSON == "" && d.Email == ""
}

// entryLabel names an entry in a response: its user id when known, else its
// position, so a bot can line results up with what it sent.
func entryLabel(i int, d PerchImportDoc) string {
	if id := firstNonEmpty(d.SupabaseUserID, d.UserID, accountUserIDOf(d)); id != "" {
		return id
	}
	return fmt.Sprintf("accounts[%d]", i)
}

func accountUserIDOf(d PerchImportDoc) string {
	if d.Account != nil {
		return d.Account.SupabaseUserID
	}
	return ""
}

// mapImportStatus puts the pool's outcome vocabulary into the open endpoint's.
func mapImportStatus(s string) string {
	switch s {
	case "created":
		return "added"
	case "updated":
		return "updated"
	case "duplicate":
		return "unchanged"
	case "skipped":
		return "unchanged"
	default:
		return "error"
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// applyPerchImportExtras sets tags (and a name override) on the accounts an
// import touched.
func (s *Server) applyPerchImportExtras(outs []perchimport.Outcome, tags []string, name string) {
	clean := make([]string, 0, len(tags))
	for _, t := range tags {
		if t = strings.TrimSpace(t); t != "" && len([]rune(t)) <= 30 {
			clean = append(clean, t)
		}
	}
	sort.Strings(clean)
	ids := map[string]bool{}
	for _, o := range outs {
		if o.AccountID != "" && (o.Status == "created" || o.Status == "updated") {
			ids[o.AccountID] = true
		}
	}
	if len(ids) == 0 {
		return
	}
	_, _ = s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			a := &c.Accounts[i]
			if !ids[a.ID] {
				continue
			}
			if len(clean) > 0 {
				a.PerchTags = clean
			}
			if n := strings.TrimSpace(name); n != "" && len([]rune(n)) <= 50 {
				a.Name = n
			}
		}
		return nil
	})
}
