package server

import (
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
)

// The Perch event log. Every entry names what happened to which account; none
// carries a token, a refresh token, or a Supabase key. The redaction below is
// a second line of defence for the one field that could carry free text (the
// error a provider returned) — nothing in this file reads a credential.

// perchLogRecord is one line of the "perch" log kind.
type perchLogRecord struct {
	TS        time.Time `json:"ts"`
	Provider  string    `json:"provider"`
	AccountID string    `json:"accountId,omitempty"`
	UserID    string    `json:"userId,omitempty"`
	Email     string    `json:"email,omitempty"`
	Event     string    `json:"event"`
	Outcome   string    `json:"outcome,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	// ImportBatch groups the documents of one import.
	ImportBatch string `json:"importBatch,omitempty"`
	// Counts for a batch summary.
	Created   int `json:"created,omitempty"`
	Updated   int `json:"updated,omitempty"`
	Duplicate int `json:"duplicate,omitempty"`
	Skipped   int `json:"skipped,omitempty"`
	Failed    int `json:"failed,omitempty"`
}

// LogKeys are the filters this kind accepts (see logstore.Filters["perch"]).
func (r *perchLogRecord) LogKeys() map[string]string {
	return map[string]string{"provider": r.Provider, "accountId": r.AccountID,
		"event": r.Event, "userId": r.UserID, "outcome": r.Outcome, "kind": r.Kind}
}

// tokenShape matches anything that looks like a JWT, a Supabase publishable
// key, or a pasted "Bearer …" — the shapes a leaked credential takes if one
// ever reaches a detail string.
var tokenShape = regexp.MustCompile(`(Bearer\s+[A-Za-z0-9._\-]+)|(eyJ[A-Za-z0-9._\-]{20,})|(sb_publishable_[A-Za-z0-9_\-]+)|(sb_secret_[A-Za-z0-9_\-]+)`)

// scrub removes anything token-shaped and caps the length.
func scrub(s string) string {
	s = tokenShape.ReplaceAllString(s, "[已隐去]")
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 500 {
		s = string([]rune(s)[:500]) + "…"
	}
	return s
}

// logf is a package-level logger used for operational trouble (a store that
// would not flush); it never carries a credential.
func (s *Server) logf(format string, args ...any) {
	log.Printf("perch: "+format, args...)
}

// logPerch appends one Perch event.
func (s *Server) logPerch(accountID, event, detail string) {
	if s.logs == nil {
		return
	}
	rec := &perchLogRecord{TS: time.Now(), Provider: config.ProviderPerch, AccountID: accountID,
		Event: event, Detail: scrub(detail)}
	if a, ok := s.cfg.Get().Account(accountID); ok {
		rec.UserID, rec.Email = a.PerchUserID, a.Email
	}
	s.logs.Append("perch", rec.TS, rec)
}

// logPerchOutcome is logPerch plus the three-way outcome a batch summary uses.
func (s *Server) logPerchOutcome(accountID, event, outcome, kind, detail string) {
	if s.logs == nil {
		return
	}
	rec := &perchLogRecord{TS: time.Now(), Provider: config.ProviderPerch, AccountID: accountID,
		Event: event, Outcome: outcome, Kind: kind, Detail: scrub(detail)}
	if a, ok := s.cfg.Get().Account(accountID); ok {
		rec.UserID, rec.Email = a.PerchUserID, a.Email
	}
	s.logs.Append("perch", rec.TS, rec)
}

// logPerchBatch records one import run's summary.
func (s *Server) logPerchBatch(batchID string, sum any, detail string) {
	if s.logs == nil {
		return
	}
	rec := &perchLogRecord{TS: time.Now(), Provider: config.ProviderPerch, Event: "import",
		ImportBatch: batchID, Detail: scrub(detail)}
	// the summary's counts are copied by name so the log line stays small
	if m, ok := sum.(interface {
		Counts() (created, updated, duplicate, skipped, failed int)
	}); ok {
		rec.Created, rec.Updated, rec.Duplicate, rec.Skipped, rec.Failed = m.Counts()
	} else if raw, err := json.Marshal(sum); err == nil {
		var parsed struct {
			Created   int `json:"created"`
			Updated   int `json:"updated"`
			Duplicate int `json:"duplicate"`
			Skipped   int `json:"skipped"`
			Failed    int `json:"failed"`
		}
		if json.Unmarshal(raw, &parsed) == nil {
			rec.Created, rec.Updated, rec.Duplicate, rec.Skipped, rec.Failed = parsed.Created, parsed.Updated, parsed.Duplicate, parsed.Skipped, parsed.Failed
		}
	}
	s.logs.Append("perch", rec.TS, rec)
}
