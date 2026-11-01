// Package perchimport reads Perch account exports: one JSON file, several,
// or a ZIP of them.
//
// The format is the one the CLI writes and is kept verbatim — nothing is
// renamed or nested differently, so an account exported today still imports
// after this program changes. A file is validated before anything is stored,
// and every file is read on its own: one bad file never costs the others.
package perchimport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"

	"account-pool-balancer/internal/perch"
	"account-pool-balancer/internal/perchstore"
)

// maxEntry caps one JSON document; an account export is a few kilobytes.
const maxEntry = 4 << 20

// Document is the account JSON as exported. Every field is read as the file
// spells it; SchemaVersion and Provider are tolerated but not required, so a
// hand-written file with only the session imports too.
type Document struct {
	SchemaVersion string `json:"schema_version"`
	Provider      string `json:"provider"`
	AuthProvider  string `json:"auth_provider"`
	Account       struct {
		SupabaseUserID string  `json:"supabase_user_id"`
		Email          string  `json:"email"`
		GitHubUserID   *string `json:"github_user_id"`
		RegisteredAt   string  `json:"registered_at"`
	} `json:"account"`
	Session struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
	} `json:"session"`
	AuthContext struct {
		SupabaseURL string  `json:"supabase_url"`
		WorkspaceID *string `json:"workspace_id"`
		// SupabaseAnonKey is an extension: when the file was exported from a
		// project other than the public one, its publishable key rides along.
		SupabaseAnonKey string `json:"supabase_anon_key"`
	} `json:"auth_context"`
	// extras keeps fields this program does not model, so a preview can say
	// the file was understood without inventing anything.
	extras map[string]bool
}

// Parsed is one document after validation.
type Parsed struct {
	// UserID is the account's identity: Supabase's user id.
	UserID       string
	Email        string
	AuthProvider string
	SupabaseURL  string
	SupabaseKey  string
	AccessToken  string
	RefreshToken string
	ExpiresAt    int64
}

// FileError explains why one entry was refused.
type FileError struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// Result is one import run.
type Result struct {
	// Files: how many documents were read.
	Files int `json:"files"`
	// Parsed: those that passed validation.
	Parsed int `json:"parsed"`
	// Failed: entries refused, with the reason each was refused for.
	Failed []FileError `json:"failed"`
	// ParsedDocs is the validated input, in file order.
	ParsedDocs []Parsed `json:"-"`
	// Sources records which file each parsed document came from (parallel to
	// ParsedDocs), so a stored outcome can name it.
	Sources []string `json:"-"`
}

// preview of what importing would do, computed before anything is written.
type Item struct {
	File     string `json:"file"`
	OK       bool   `json:"ok"`
	Reason   string `json:"reason,omitempty"`
	UserID   string `json:"userId,omitempty"`
	Email    string `json:"email,omitempty"`
	Provider string `json:"provider,omitempty"`
	// Session: the access token's end as the file states it.
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	Expired   bool      `json:"expired,omitempty"`
	// HasRefresh marks a file that can be kept alive automatically.
	HasRefresh bool `json:"hasRefresh"`
	// DuplicateOf names the earlier file with the same Supabase user id, or
	// "stored" when the account is already in the pool.
	DuplicateOf string `json:"duplicateOf,omitempty"`
	// AccountID is the config account this document binds to, when the pool
	// already holds it.
	AccountID string `json:"accountId,omitempty"`
}

// ParseJSON reads one document. The error says precisely which field is
// wrong, because an operator importing a hundred files needs to fix the one
// that failed without guessing.
func ParseJSON(raw []byte) (Parsed, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Parsed{}, errors.New("the file is empty")
	}
	// A file may be a single object or an array of them (a batch export).
	if raw[0] == '[' {
		var many []json.RawMessage
		if err := json.Unmarshal(raw, &many); err != nil {
			return Parsed{}, fmt.Errorf("not valid JSON: %v", err)
		}
		if len(many) != 1 {
			return Parsed{}, fmt.Errorf("holds %d accounts; split it into one file per account", len(many))
		}
		return ParseJSON(many[0])
	}
	var doc Document
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&doc); err != nil {
		return Parsed{}, fmt.Errorf("not valid JSON: %v", err)
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(raw, &all) == nil {
		doc.extras = map[string]bool{}
		for k := range all {
			doc.extras[k] = true
		}
	}

	p := Parsed{
		UserID:       strings.TrimSpace(doc.Account.SupabaseUserID),
		Email:        strings.TrimSpace(doc.Account.Email),
		AuthProvider: strings.TrimSpace(doc.AuthProvider),
		SupabaseURL:  strings.TrimSpace(doc.AuthContext.SupabaseURL),
		SupabaseKey:  strings.TrimSpace(doc.AuthContext.SupabaseAnonKey),
		AccessToken:  strings.TrimSpace(doc.Session.AccessToken),
		RefreshToken: strings.TrimSpace(doc.Session.RefreshToken),
		ExpiresAt:    doc.Session.ExpiresAt,
	}
	if p.UserID == "" {
		return Parsed{}, errors.New("account.supabase_user_id is missing; that is what identifies an account")
	}
	if p.AccessToken == "" && p.RefreshToken == "" {
		return Parsed{}, errors.New("session.access_token and session.refresh_token are both empty")
	}
	if p.ExpiresAt <= 0 && p.AccessToken != "" {
		// the file did not say when it ends; the token does
		if exp, ok := perch.ExpiryOf(p.AccessToken); ok {
			p.ExpiresAt = exp.Unix()
		}
	}
	if p.AuthProvider == "" {
		p.AuthProvider = doc.AuthProvider
	}
	if p.SupabaseURL == "" {
		p.SupabaseURL = perch.DefaultSupabaseURL
	}
	if p.Email == "" {
		if claims := perch.TokensOf(p.AccessToken); claims != nil {
			if e, ok := claims["email"].(string); ok {
				p.Email = strings.TrimSpace(e)
			}
		}
	}
	return p, nil
}

// Secrets turns a parsed document into the store's record.
//
// FetchedAt is derived from the tokens themselves, not from the moment of
// import: the access token's end says when the pair was issued (a Supabase
// access token lives an hour), so a file exported months ago is dated months
// ago and can never overwrite a session the keeper has renewed since. This is
// what makes "an old JSON must not undo a renewal" hold even when the file is
// uploaded today.
func (p Parsed) Secrets() perchstore.Secrets {
	return perchstore.Secrets{
		UserID: p.UserID, Email: p.Email, Provider: p.AuthProvider,
		SupabaseURL: p.SupabaseURL, SupabaseKey: p.SupabaseKey,
		AccessToken: p.AccessToken, RefreshToken: p.RefreshToken, ExpiresAt: p.ExpiresAt,
		FetchedAt: p.FetchedAt(),
	}
}

// accessTokenLifetime is how long a Supabase access token lives; the issue
// time of a session is inferred from its stated end.
const accessTokenLifetime = time.Hour

// FetchedAt estimates when the session's tokens were confirmed. It is the
// access token's expiry less one lifetime, which is what the CLI's own
// session records imply. A record with no expiry yields the zero time, and
// the store then falls back to the import's arrival.
func (p Parsed) FetchedAt() time.Time {
	if p.ExpiresAt <= 0 {
		return time.Time{}
	}
	issued := time.Unix(p.ExpiresAt, 0).Add(-accessTokenLifetime)
	if issued.After(time.Now()) {
		// a clock-skewed file must not claim a future fetch time
		return time.Now().UTC()
	}
	return issued.UTC()
}

// ReadFiles reads a batch of uploaded files: JSON documents are parsed, and a
// ZIP is opened and each of its JSON entries parsed. Entries are read
// independently, so a corrupt member costs only itself.
func ReadFiles(files map[string][]byte) Result {
	out := Result{Failed: []FileError{}}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		body := files[name]
		if isZip(name, body) {
			out.Files++
			sub := readZip(name, body)
			out.Parsed += sub.Parsed
			out.ParsedDocs = append(out.ParsedDocs, sub.ParsedDocs...)
			out.Sources = append(out.Sources, sub.Sources...)
			out.Failed = append(out.Failed, sub.Failed...)
			continue
		}
		out.Files++
		p, err := ParseJSON(body)
		if err != nil {
			out.Failed = append(out.Failed, FileError{File: name, Reason: err.Error()})
			continue
		}
		out.Parsed++
		out.ParsedDocs = append(out.ParsedDocs, p)
		out.Sources = append(out.Sources, name)
	}
	return out
}

func isZip(name string, body []byte) bool {
	if len(body) >= 4 && bytes.Equal(body[:2], []byte("PK")) {
		return true
	}
	return strings.EqualFold(path.Ext(name), ".zip")
}

// readZip reads every .json member of an archive.
func readZip(name string, body []byte) Result {
	out := Result{Failed: []FileError{}}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		out.Failed = append(out.Failed, FileError{File: name, Reason: "not a readable ZIP: " + err.Error()})
		return out
	}
	var entries []*zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || strings.HasPrefix(path.Base(f.Name), ".") {
			continue
		}
		// __MACOSX and other archives' metadata resources are not accounts
		if strings.HasPrefix(f.Name, "__MACOSX/") {
			continue
		}
		if !strings.EqualFold(path.Ext(f.Name), ".json") {
			continue
		}
		entries = append(entries, f)
	}
	if len(entries) == 0 {
		out.Failed = append(out.Failed, FileError{File: name, Reason: "the ZIP holds no .json files"})
		return out
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	for _, f := range entries {
		label := name + ":" + f.Name
		rc, err := f.Open()
		if err != nil {
			out.Failed = append(out.Failed, FileError{File: label, Reason: "cannot be opened: " + err.Error()})
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(rc, maxEntry))
		_ = rc.Close()
		if err != nil {
			out.Failed = append(out.Failed, FileError{File: label, Reason: "cannot be read: " + err.Error()})
			continue
		}
		out.Files++
		p, perr := ParseJSON(raw)
		if perr != nil {
			out.Failed = append(out.Failed, FileError{File: label, Reason: perr.Error()})
			continue
		}
		out.Parsed++
		out.ParsedDocs = append(out.ParsedDocs, p)
		out.Sources = append(out.Sources, label)
	}
	return out
}

// Preview describes what an import would do, without writing anything. It
// marks files that collide with each other (same Supabase user id) and files
// that already have a session in the pool.
func Preview(res Result, stored func(userID string) bool) []Item {
	items := make([]Item, 0, len(res.ParsedDocs))
	for i, p := range res.ParsedDocs {
		item := Item{File: res.Sources[i], OK: true, UserID: p.UserID, Email: p.Email, Provider: p.AuthProvider,
			HasRefresh: p.RefreshToken != ""}
		if p.ExpiresAt > 0 {
			item.ExpiresAt = time.Unix(p.ExpiresAt, 0)
			item.Expired = time.Now().After(item.ExpiresAt)
		} else {
			item.Expired = true
		}
		for j := 0; j < i; j++ {
			if res.ParsedDocs[j].UserID == p.UserID {
				item.DuplicateOf = res.Sources[j]
				break
			}
		}
		if item.DuplicateOf == "" && stored != nil && stored(p.UserID) {
			item.DuplicateOf = "stored"
		}
		items = append(items, item)
	}
	for _, f := range res.Failed {
		items = append(items, Item{File: f.File, OK: false, Reason: f.Reason})
	}
	return items
}

// Outcome is one document's fate in an applied import.
type Outcome struct {
	File   string `json:"file"`
	UserID string `json:"userId"`
	Email  string `json:"email,omitempty"`
	// AccountID is the config account the session was bound to.
	AccountID string `json:"accountId,omitempty"`
	// Status: created | updated | duplicate | skipped | failed
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Summary counts an import's outcomes.
type Summary struct {
	Files     int       `json:"files"`
	Created   int       `json:"created"`
	Updated   int       `json:"updated"`
	Duplicate int       `json:"duplicate"`
	Skipped   int       `json:"skipped"`
	Failed    int       `json:"failed"`
	Outcomes  []Outcome `json:"outcomes"`
}

// Summarize counts outcomes.
func Summarize(files int, outs []Outcome) Summary {
	s := Summary{Files: files, Outcomes: outs}
	for _, o := range outs {
		switch o.Status {
		case "created":
			s.Created++
		case "updated":
			s.Updated++
		case "duplicate":
			s.Duplicate++
		case "skipped":
			s.Skipped++
		case "failed":
			s.Failed++
		}
	}
	return s
}
