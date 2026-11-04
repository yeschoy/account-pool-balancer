package server

import (
	"strings"
	"time"

	"account-pool-balancer/internal/config"
)

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
