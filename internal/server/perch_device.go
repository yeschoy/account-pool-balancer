package server

// Perch device identity and telemetry, as the pool uses them.
//
// Three pieces live here:
//
//   - telemetrySettings: console settings → perchtelemetry.Settings, including
//     the CLI's own opt-out detection from the process environment.
//   - auditSink: the reporter's audit log. Every report the pool builds lands
//     in the Perch event log, whether or not it was sent; with egress off this
//     is the only trace, and it is where an operator checks what would go out.
//   - perchIdentity / perchCallIdentity: the per-account identity a model call
//     presents (machine id, cwd thread, UA, locale).
//
// The rule these all keep: a value that identifies a device is per account,
// stable, and sealed on disk — never shared between accounts and never equal to
// a request id.

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perch"
	"account-pool-balancer/internal/perchtelemetry"
)

// telemetrySettings is the reporter's view of the console settings.
func (s *Server) telemetrySettings(cfg *config.Config) perchtelemetry.Settings {
	p := cfg.Perch
	return perchtelemetry.Settings{
		Egress:      p.TelemetryEgress,
		PostHogKey:  strings.TrimSpace(p.PostHogKey),
		PostHogHost: p.PostHogHost,
		// The CLI's opt-outs are environment variables; the console switch is
		// added on top so an operator can turn PostHog off without restarting.
		OptedOut: p.TelemetryOptOut || perch.OptedOut(os.Getenv),
		// The CLI reports process.platform; this is the same fact for a Go
		// build.
		Platform: runtime.GOOS,
		Version:  p.CLIVersion,
	}
}

// refreshTelemetrySettings re-reads the console settings into the reporter.
// It is called after a settings save so a switch takes effect immediately.
func (s *Server) refreshTelemetrySettings(cfg *config.Config) {
	if s.perch == nil || s.perch.telemetry == nil {
		return
	}
	s.perch.telemetry.SetSettings(s.telemetrySettings(cfg))
}

// auditSink adapts the server to perchtelemetry.Repo.
type auditSink struct{ s *Server }

// Audit writes one Perch log line per report. detail is already redacted by the
// reporter: prompt and answer arrive as digests, never as text.
func (a auditSink) Audit(provider, accountID, kind, detail string) {
	if a.s == nil || a.s.logs == nil {
		return
	}
	event := "telemetry"
	if kind == "cli-turn" {
		event = "cli_turn"
	}
	a.s.logPerchOutcome(accountID, event, "", kind, detail)
}

// perchIdentity returns the account's device identity. A missing store (it
// failed to open at boot) degrades to the process-wide id, which is better than
// minting a new one per call but is deliberately NOT per account — the console
// shows the difference.
func (s *Server) perchIdentity(accountID string) perch.DeviceIdentity {
	if s.perch == nil {
		return perch.DeviceIdentity{}
	}
	if s.perch.devices == nil {
		return perch.DeviceIdentity{MachineID: ""}
	}
	// Two config accounts may point at the same Perch user; key on the user id
	// so the identity follows the account's actual Perch account rather than
	// the local row that happens to serve the request.
	key := accountID
	if a, ok := s.cfg.Get().Account(accountID); ok && strings.TrimSpace(a.PerchUserID) != "" {
		key = a.PerchUserID
	}
	return s.perch.devices.Get(key)
}

// perchProcessDeviceID is the per-install id used where no account is involved
// (health probes, quota reads).
func (s *Server) perchProcessDeviceID() string {
	if s.perch == nil || s.perch.devices == nil {
		return ""
	}
	return s.perch.devices.ProcessDeviceID()
}

// perchThreadID resolves the thread id a turn belongs to: the caller's
// conversation when there is one, else the CLI's cwd-derived thread when the
// setting is on, else "".
//
// The "working directory" is the data directory. It is a real, stable path on
// this machine, which is what the CLI hashes; it is not the caller's cwd (the
// pool has no way to know it) and is not a path taken from a request, so no
// client-supplied text can be steered into the digest.
func (s *Server) perchThreadID(cfg *config.Config, conversation string) string {
	if v := strings.TrimSpace(conversation); v != "" {
		return v
	}
	if !cfg.Perch.CwdThreadID {
		return ""
	}
	dir := s.dataDir
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return perch.CwdThreadID(dir)
}

// perchCallIdentity is the CLI identity a model call presents: the UA that
// names the CLI version, and the locale header. Both are per call because the
// console can change them without a restart.
func (s *Server) perchCallIdentity(cfg *config.Config) (ua, locale string) {
	return perch.UserAgentFor(cfg.Perch.CLIVersion), perch.Locale(cfg.Perch.Locale)
}

// perchTurn is what one Perch attempt asks and answers, kept on the attempt
// result so the record is reported once the outcome is known. The text lives
// here only between the call and the reporter, which redacts it.
type perchTurn struct {
	Thread string
	// Prompt is the whole conversation; Answer is what Perch returned.
	Prompt string
	Answer string
	RunID  string
	Mode   string
}

// reportPerchTurn builds the cli-turn record for a finished attempt and hands
// it to the reporter. It reports the conversation only when the setting is on;
// the five anonymous PostHog events follow the same switch.
//
// Only attempts that reached the model call with a bound account and token
// are reported; local validation failures do not emit account telemetry.
func (s *Server) reportPerchTurn(cfg *config.Config, acct config.Account, rec *requestLog, res *attemptResult, started time.Time) {
	if s.perch == nil || s.perch.telemetry == nil || res == nil || !res.turnStarted || strings.TrimSpace(res.turnToken) == "" || acct.ID == "" {
		return
	}
	if !cfg.Perch.CLITurnReport && strings.TrimSpace(cfg.Perch.PostHogKey) == "" {
		return
	}
	t := res.turn
	if t.Thread == "" {
		t.Thread = s.perchThreadID(cfg, "")
	}
	if t.RunID == "" {
		t.RunID = perch.NewRunID()
	}
	status := perch.TurnStatusComplete
	var errText *string
	switch {
	case rec.Result == "success":
	case rec.Result == "client_cancelled":
		status = perch.TurnStatusAborted
	case res.kind == outFailed:
		status = perch.TurnStatusError
		e := rec.Result
		if b := strings.TrimSpace(string(res.body)); b != "" {
			e = b
		}
		errText = &e
	default:
		// delivered-stream that ended in a failure: the answer went out but
		// the turn did not complete
		status = perch.TurnStatusError
		e := "upstream"
		errText = &e
	}
	duration := time.Since(started).Milliseconds()
	turn := perch.CLITurn{
		Prompt:          t.Prompt,
		AssistantText:   t.Answer,
		ThreadID:        t.Thread,
		RunID:           t.RunID,
		ChatMode:        t.Mode,
		Status:          status,
		DurationMs:      duration,
		Error:           errText,
		Events:          []any{},
		SystemInitiated: false,
		Attachments:     []any{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if cfg.Perch.CLITurnReport {
		endpoint := perch.CLITurnURL(strings.TrimSpace(cfg.Perch.AppURL))
		if endpoint == "" {
			endpoint = perch.CLITurnURL(perch.AppURL)
		}
		s.perch.telemetry.CLITurnWithProxy(ctx, endpoint, res.turnToken, turn, acct.ProxyURL)
	}
	// The anonymous events are the CLI's own coarse ones; they name no
	// conversation. first_message fires once per account, which is why the
	// device store keeps the marker.
	if key := strings.TrimSpace(cfg.Perch.PostHogKey); key != "" {
		dev := s.perchIdentity(acct.ID)
		s.perch.telemetry.PostHogWithProxy(ctx, perch.BuildPostHogEvent(key, perch.EventMessageSent, dev.MachineID,
			map[string]any{"surface": "cli"}), acct.ProxyURL)
		if status == perch.TurnStatusComplete && s.perch.devices != nil {
			if s.perch.devices.MarkFirstPrompt(s.perchDeviceKey(acct)) {
				s.perch.telemetry.PostHogWithProxy(ctx, perch.BuildPostHogEvent(key, perch.EventFirstMessage, dev.MachineID,
					map[string]any{"surface": "cli"}), acct.ProxyURL)
			}
		}
	}
}

// perchDeviceKey is the key an account's identity is stored under: its Perch
// user id, so two config rows for one Perch account share one device.
func (s *Server) perchDeviceKey(a config.Account) string {
	if u := strings.TrimSpace(a.PerchUserID); u != "" {
		return u
	}
	return a.ID
}

// ---- the console's read-only fingerprint view ---------------------------------

// perchFingerprintAccount is one account's identity as the console shows it:
// whether it has a stable device, when it was first seen, and the thread id it
// presents. The full machine id is shown because it is the pool's own random
// value, not a secret — but it is per account, so it identifies the account.
type perchFingerprintAccount struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// MachineID is the account's device id ("" when the store is unavailable).
	MachineID string `json:"machineId"`
	// Shared is true when another account reports the same id, which is the
	// condition this whole layer exists to prevent.
	Shared        bool      `json:"shared,omitempty"`
	CreatedAt     time.Time `json:"createdAt,omitzero"`
	FirstPromptAt time.Time `json:"firstPromptAt,omitzero"`
	ThreadID      string    `json:"threadId"`
	ProxySource   string    `json:"proxySource"`

	// Turn-ticket state. A ticket is per account and per turn; these fields say
	// whether one is held, how much of its budget is gone, and when it lapses.
	// They are observations of the client's cache, not of the server.
	HasTicket        bool      `json:"hasTicket"`
	TicketRunID      string    `json:"ticketRunId,omitempty"`
	TicketProfile    string    `json:"ticketProfile,omitempty"`
	TicketUses       int       `json:"ticketUses,omitempty"`
	TicketRemaining  int       `json:"ticketRemaining,omitempty"`
	TicketExpiresAt  time.Time `json:"ticketExpiresAt,omitzero"`
	TicketTicketID   string    `json:"ticketId,omitempty"`
	TicketFrozenNote string    `json:"ticketNote,omitempty"`
}

// apiPerchFingerprint reports what the pool presents on each account's behalf,
// so an operator can see identity reuse instead of inferring it.
//
// It is read-only and never returns a credential. The thread id is the digest
// the pool would send, so a leak here reveals no path.
func (s *Server) apiPerchFingerprint(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	out := struct {
		StoreAvailable bool                      `json:"storeAvailable"`
		Settings       map[string]any            `json:"settings"`
		ProcessDevice  string                    `json:"processDeviceId,omitempty"`
		Accounts       []perchFingerprintAccount `json:"accounts"`
		// Delta is the held-context delta report (see delta.go / runid.go).
		Delta *perch.DeltaReport `json:"delta,omitempty"`
	}{
		StoreAvailable: s.perch != nil && s.perch.devices != nil,
		ProcessDevice:  s.perchProcessDeviceID(),
		Accounts:       []perchFingerprintAccount{},
	}
	// Held-context delta: is the pool actually sending only what is new. These
	// are process-wide counters, so they answer "is it working" rather than
	// "what did the last request do".
	delta := s.perchClient().DeltaReport()
	delta.TrackedConversations = s.perchClient().RunStats()
	out.Delta = &delta

	tel := s.telemetrySettings(cfg)
	out.Settings = map[string]any{
		"deviceFingerprint": cfg.Perch.DeviceFingerprint,
		"cwdThreadId":       cfg.Perch.CwdThreadID,
		"cliVersion":        cfg.Perch.CLIVersion,
		"userAgent":         perch.UserAgentFor(cfg.Perch.CLIVersion),
		"locale":            perch.Locale(cfg.Perch.Locale),
		"turnTicket":        cfg.Perch.TurnTicketOn(),
		"ticketProfile":     cfg.Perch.TicketProfile,
		"cliTurnReport":     cfg.Perch.CLITurnReport,
		"telemetryEgress":   tel.Egress,
		"posthogConfigured": tel.PostHogKey != "",
		"posthogOptedOut":   tel.OptedOut,
		"note":              "telemetryEgress=false 时遥测不上报；模型、刷新等业务请求不受此设置控制",
	}
	seen := map[string]int{}
	for _, a := range cfg.Accounts {
		if a.Provider != config.ProviderPerch {
			continue
		}
		id := s.perchIdentity(a.ID)
		seen[id.MachineID]++
		row := perchFingerprintAccount{
			ID: a.ID, Name: a.Name, MachineID: id.MachineID,
			CreatedAt: id.CreatedAt, FirstPromptAt: id.FirstPromptAt,
			ThreadID: s.perchThreadID(cfg, ""), ProxySource: perchFingerprintProxySource(a.ProxyURL),
		}
		if tk, ok := s.perchClient().TicketStatus(a.ID); ok {
			row.HasTicket = true
			row.TicketRunID = tk.RunID
			row.TicketTicketID = tk.TicketID
			row.TicketProfile = tk.Profile
			row.TicketUses = tk.Uses()
			row.TicketRemaining = tk.Remaining()
			row.TicketExpiresAt = tk.ExpiresAt
		} else if cfg.Perch.TurnTicketOn() {
			row.TicketFrozenNote = "no ticket held yet (issued before the account's next turn)"
		}
		out.Accounts = append(out.Accounts, row)
	}
	for i := range out.Accounts {
		if out.Accounts[i].MachineID != "" && seen[out.Accounts[i].MachineID] > 1 {
			out.Accounts[i].Shared = true
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// Proxy source is configuration provenance, not an observed network route.
func perchFingerprintProxySource(proxyURL string) string {
	if proxyURL != "" {
		return "account_proxy"
	}
	return "unconfigured_unknown"
}

// apiPerchDeltaReset zeroes the held-context delta counters, keeping the cache.
//
// An operator uses it after a change (a new client, a different lane) to see
// the new behaviour on its own rather than averaged with everything before it.
func (s *Server) apiPerchDeltaReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	s.perchClient().ResetDeltaCounters()
	rep := s.perchClient().DeltaReport()
	rep.TrackedConversations = s.perchClient().RunStats()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "delta": rep})
}
