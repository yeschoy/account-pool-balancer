package perch

// Device identity, as the published CLI has it.
//
// The pool used to hand Perch a different truth on every request: run ids were
// random, thread ids were caller-supplied or random, and nothing carried the
// CLI's own notion of "this machine". The CLI is not like that. Measured
// against perchai-cli 2.4.121 (see .analysis/perchai-cli-2.4.121/analysis.md),
// the identity surface is small and mostly deterministic:
//
//   - A machine id: `b2i` reads $PERCH_CLI_TELEMETRY_DIR/machine-id (default
//     ~/.perch/machine-id), trims it, and keeps it for good. Only when the file
//     is missing or empty does it mint one and write it back. It is a random
//     UUID: NOT the operating system's machine-id, no hardware serial, no MAC,
//     no hostname. That distinction matters — this package must not start
//     inventing hardware fingerprints the real client never sends.
//   - Two one-shot markers beside it, `cli-first-run` and `cli-first-prompt`,
//     each created with an ISO timestamp the first time and never updated, so
//     "first run" and "first message" events fire exactly once per install.
//   - A thread id derived from the working directory: `Znt(cwd)` is
//     "cli-cwd-" + the first 16 hex characters of SHA-256 over the lexically
//     resolved cwd. `resolve` does not follow symlinks. It is a digest, so the
//     raw path is never on the wire, but a low-entropy path can still be
//     guessed by enumeration — it is pseudonymity, not secrecy.
//   - A locale header `x-perch-locale` and the `perchai-cli/<version>` UA.
//
// The pool serves many accounts from one process, so the files above cannot be
// global: one machine id across every account is exactly the linkage signal this
// work is meant to control. Instead each account gets its own identity, sealed
// in the credential store next to its session (see internal/perchstore).

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	// ThreadIDPrefix is the CLI's own prefix for a cwd-derived thread.
	ThreadIDPrefix = "cli-cwd-"
	// threadDigestLen is how much of the SHA-256 hex the CLI keeps: 16 chars.
	threadDigestLen = 16

	// LocaleHeader carries the CLI's locale.
	LocaleHeader = "x-perch-locale"
	// DefaultLocale is what the pool has always sent, and what the CLI sends
	// with the default settings.
	DefaultLocale = "en"
)

// DeviceIdentity is one account's stable, pseudonymous device state. Every
// field is derived once and then reused, because a value that changes per
// request is the opposite of an identity.
type DeviceIdentity struct {
	// MachineID is this account's machine id: a random UUID minted on first
	// use. The CLI stores one per install; the pool stores one per account so
	// two accounts never look like one machine.
	MachineID string `json:"machineId"`
	// CreatedAt is when the machine id was minted (the CLI's cli-first-run
	// marker).
	CreatedAt time.Time `json:"createdAt,omitzero"`
	// FirstPromptAt is when this account's first message was sent (the CLI's
	// cli-first-prompt marker).
	FirstPromptAt time.Time `json:"firstPromptAt,omitzero"`
}

// NewMachineID mints a machine id. The CLI's `h2i()` yields a random UUID v4;
// this produces the same shape from crypto/rand.
func NewMachineID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on the platforms this runs on; if it ever
		// does, a time-based value would be worse than no identity at all.
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// EnsureMachineID returns the identity's machine id, minting and stamping one
// when it is missing. changed reports whether the caller has to persist it.
func (d DeviceIdentity) EnsureMachineID(now time.Time) (DeviceIdentity, bool) {
	if strings.TrimSpace(d.MachineID) != "" {
		return d, false
	}
	id := NewMachineID()
	if id == "" {
		return d, false
	}
	d.MachineID = id
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	return d, true
}

// MarkFirstPrompt stamps the first-message time once. changed reports whether
// this was the first message, which is what the CLI's cli-first-prompt marker
// decides.
func (d DeviceIdentity) MarkFirstPrompt(now time.Time) (DeviceIdentity, bool) {
	if !d.FirstPromptAt.IsZero() {
		return d, false
	}
	d.FirstPromptAt = now
	return d, true
}

// CwdThreadID is the CLI's `Znt`: "cli-cwd-" plus the first 16 hex characters
// of SHA-256 over the lexically resolved cwd.
//
// resolve is lexical (filepath.Abs, not EvalSymlinks) exactly as the CLI's
// `resolve` is, so the same string yields the same id on every call. An empty
// cwd yields "" and callers must then keep their own thread id rather than
// sending a digest of nothing.
func CwdThreadID(cwd string) string {
	p := strings.TrimSpace(cwd)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256([]byte(abs))
	return ThreadIDPrefix + hex.EncodeToString(sum[:])[:threadDigestLen]
}

// MachineIDName is the CLI's file name for the machine id, kept so an operator
// who exports a CLI home can find the same value.
const MachineIDName = "machine-id"

// Locale validates a locale header value. The CLI sends its own language code;
// the pool accepts only what fits a header safely, and falls back to DefaultLocale.
func Locale(v string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return DefaultLocale
	}
	if len(s) > 35 {
		return DefaultLocale
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return DefaultLocale
		}
	}
	return s
}

// UserAgentFor is the UA string for a claimed version. An empty version yields
// "unknown", which is what the CLI itself sends when it has none (`v6`).
func UserAgentFor(version string) string {
	v := strings.TrimSpace(version)
	if v == "" {
		return "perchai-cli/unknown"
	}
	return "perchai-cli/" + v
}

// VerifyUserAgent refuses a version that cannot be a User-Agent token, so a
// misconfigured setting cannot inject a header.
func VerifyUserAgent(version string) error {
	for _, r := range version {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '+', r == '_':
		default:
			return fmt.Errorf("版本号只能包含字母、数字和 . - + _")
		}
	}
	return nil
}

// DeviceEcho is the shape Snowflake's Cortex and the Anthropic message format
// put in metadata.user_id (CLI `hVt`). It is included because the pool speaks
// that format to some providers; it is not sent to Perch's own envelope, which
// has no such field.
type DeviceEcho struct {
	DeviceID    string `json:"device_id"`
	AccountUUID string `json:"account_uuid"`
	SessionID   string `json:"session_id"`
}

// NewDeviceEcho builds the echo for one call: the account's stable device id
// and the session id for this conversation.
func NewDeviceEcho(deviceID, accountUUID, sessionID string) DeviceEcho {
	return DeviceEcho{DeviceID: deviceID, AccountUUID: accountUUID, SessionID: sessionID}
}

// JSON renders the echo the way `hVt` does: a JSON string inside the field.
func (e DeviceEcho) JSON() string {
	return fmt.Sprintf(`{"device_id":%q,"account_uuid":%q,"session_id":%q}`, e.DeviceID, e.AccountUUID, e.SessionID)
}
