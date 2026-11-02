package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perch"
)

// perchDiagnostic is a strict projection: no provider text or credentials.
// Status is a client-reported status, which can be synthesized by the client.
type perchDiagnostic struct {
	TS          time.Time `json:"ts"`
	Provider    string    `json:"provider"`
	AccountID   string    `json:"accountId,omitempty"`
	Event       string    `json:"event"`
	Kind        string    `json:"kind"`
	Outcome     string    `json:"outcome"`
	ProxySource string    `json:"proxySource"`
	Target      string    `json:"target"`
	Status      int       `json:"status,omitempty"`
	DurationMs  int64     `json:"durationMs"`
	ErrorClass  string    `json:"errorClass,omitempty"`
}

func (r *perchDiagnostic) LogKeys() map[string]string {
	return map[string]string{"provider": r.Provider, "accountId": r.AccountID,
		"event": r.Event, "kind": r.Kind, "outcome": r.Outcome}
}

// Only known hosts become labels. Even a syntactically valid custom hostname
// can contain sensitive data, so it is never copied to a diagnostic.
func perchDiagnosticTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return "unknown"
	}
	switch strings.ToLower(u.Hostname()) {
	case "app.perchai.app":
		return "perch-app"
	case "chat.perchai.app":
		return "perch-chat"
	case "zlfuvsfjtgsdtqcaykia.supabase.co":
		return "supabase-default"
	default:
		return "unknown"
	}
}

func perchDiagnosticError(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	if errors.Is(err, context.Canceled) {
		return 0, "cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 0, "timeout"
	}
	var modelErr *perch.ModelError
	if errors.As(err, &modelErr) {
		status := modelErr.Status
		if status < 100 || status > 599 {
			status = 0
		}
		switch modelErr.Code {
		case "timeout":
			return status, "timeout"
		case "transport_error", "bad_proxy":
			return status, "transport"
		case "stream_error", "incomplete_stream", "bad_upstream_body":
			return status, "response"
		}
		return status, perchDiagnosticStatusClass(status)
	}
	var clientErr *perch.Error
	if errors.As(err, &clientErr) {
		status := clientErr.Status
		if status < 100 || status > 599 {
			status = 0
		}
		switch clientErr.Kind {
		case perch.KindAuth:
			return status, "auth"
		case perch.KindRateLimited:
			return status, "rate_limited"
		case perch.KindTransient:
			return status, "transient"
		case perch.KindRejected:
			return status, "rejected"
		case perch.KindNotFound:
			return status, "not_found"
		}
		return status, "unknown"
	}
	return 0, "unknown"
}

func perchDiagnosticStatusClass(status int) string {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "auth"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status >= 500:
		return "upstream"
	case status >= 400:
		return "rejected"
	default:
		return "unknown"
	}
}

// logPerchDiagnostic describes one completed high-level call, not HTTP attempts.
// proxySource is configuration provenance, not a claim about the actual route.
func (s *Server) logPerchDiagnostic(accountID, stage, proxySource, target string, started time.Time, err error) {
	if s.logs == nil {
		return
	}
	switch stage {
	case "register", "activate", "privacy", "refresh", "quota", "models", "model":
	default:
		return
	}
	switch proxySource {
	case "account_proxy", "explicit_proxy", "direct", "unconfigured_unknown":
	default:
		proxySource = "unconfigured_unknown"
	}
	status, class := perchDiagnosticError(err)
	outcome := "success"
	if err != nil {
		outcome = "failure"
	}
	duration := time.Since(started).Milliseconds()
	if duration < 0 {
		duration = 0
	}
	rec := &perchDiagnostic{TS: time.Now(), Provider: config.ProviderPerch, AccountID: accountID,
		Event: "egress", Kind: stage, Outcome: outcome, ProxySource: proxySource,
		Target: perchDiagnosticTarget(target), Status: status, DurationMs: duration, ErrorClass: class}
	s.logs.Append("perch", rec.TS, rec)
}
