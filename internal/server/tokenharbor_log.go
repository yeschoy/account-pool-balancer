package server

import (
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
)

// thLogHub keeps the registration log in memory as well as in the log store,
// so the console can tail it without paging the durable log. It is bounded and
// holds no credential: entries come from flow step labels and scrubbed reasons.
type thLogHub struct {
	mu   sync.Mutex
	seq  int64
	ring []thLogEntry
}

// thLogEntry is one line of the live log.
type thLogEntry struct {
	Seq    int64     `json:"seq"`
	TS     time.Time `json:"ts"`
	Email  string    `json:"email"`
	Stage  string    `json:"stage"`
	Detail string    `json:"detail,omitempty"`
	Proxy  string    `json:"proxy,omitempty"`
	Level  string    `json:"level"`
}

const thLogRing = 500

// append adds one line and returns its sequence number.
func (h *thLogHub) append(e thLogEntry) int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	e.Seq = h.seq
	h.ring = append(h.ring, e)
	if len(h.ring) > thLogRing {
		h.ring = h.ring[len(h.ring)-thLogRing:]
	}
	return h.seq
}

// prune drops expired live-tail entries without resetting sequence cursors.
func (h *thLogHub) prune(cut time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	kept := h.ring[:0]
	for _, entry := range h.ring {
		if !entry.TS.Before(cut) {
			kept = append(kept, entry)
		}
	}
	h.ring = append([]thLogEntry(nil), kept...)
}

// since returns entries with Seq > after, newest last.
func (h *thLogHub) since(after int64) ([]thLogEntry, int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]thLogEntry, 0, 32)
	for _, e := range h.ring {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out, h.seq
}

func (s *Server) thLog() *thLogHub {
	s.thLogMu.Lock()
	defer s.thLogMu.Unlock()
	if s.thLogs == nil {
		s.thLogs = &thLogHub{}
	}
	return s.thLogs
}

// thLive writes one live-and-durable registration log line.
func (s *Server) thLive(email, stage, detail, proxy, level string) {
	entry := thLogEntry{TS: time.Now(), Email: email, Stage: stage,
		Detail: thRedact(detail), Proxy: thProxyLabel(proxy), Level: level}
	hub := s.thLog()
	hub.append(entry)
	hub.prune(time.Now().Add(-time.Duration(s.cfg.Get().Logs.RetentionDays) * 24 * time.Hour))
	if s.logs == nil {
		return
	}
	s.logs.Append("tokenharbor", entry.TS, &thLogRecord{
		TS: entry.TS, Provider: config.ProviderTokenHarbor, Email: email,
		Event: "register", Kind: stage, Outcome: level, Detail: entry.Detail, Proxy: entry.Proxy,
	})
}

// The Token Harbor event log. It shares the "tokenharbor" log kind and names
// what happened to which mailbox; it never carries a password, a key, a cookie
// or a captcha token — the detail is scrubbed and capped by thRedact.

type thLogRecord struct {
	TS       time.Time `json:"ts"`
	Provider string    `json:"provider"`
	Email    string    `json:"email,omitempty"`
	Event    string    `json:"event"`
	Outcome  string    `json:"outcome,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Proxy    string    `json:"proxy,omitempty"`
}

// LogKeys are the filters the log kind accepts.
func (r *thLogRecord) LogKeys() map[string]string {
	return map[string]string{"provider": r.Provider, "email": r.Email, "event": r.Event, "kind": r.Kind, "outcome": r.Outcome}
}

// logTHDiagnostic records one registration stage outcome. Proxy is the
// credential-free label, never the address with its password.
func (s *Server) logTHDiagnostic(email, stage, proxy string, err error) {
	if s.logs == nil {
		return
	}
	outcome := "success"
	detail := ""
	if err != nil {
		outcome = "failure"
		detail = thRedact(err.Error())
	}
	s.logs.Append("tokenharbor", time.Now(), &thLogRecord{
		TS:       time.Now(),
		Provider: config.ProviderTokenHarbor,
		Email:    email,
		Event:    "register",
		Outcome:  outcome,
		Kind:     stage,
		Detail:   detail,
		Proxy:    thProxyLabel(proxy),
	})
}

// thProxyLabel strips credentials from an egress address.
func thProxyLabel(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "直连"
	}
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		if j := strings.Index(raw, "://"); j >= 0 {
			return raw[:j+3] + raw[i+1:]
		}
		return raw[i+1:]
	}
	return raw
}
