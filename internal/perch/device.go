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
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strconv"
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

	// TicketHeader carries the turn ticket the CLI obtained before its first
	// model call (the CLI's `hqr`).
	TicketHeader = "x-perch-turn-ticket"
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
	return CwdThreadIDFor(cwd, "")
}

// CwdThreadIDFor is CwdThreadID scoped to one account.
//
// The published CLI hashes the working directory, so every account served from
// this process would present the SAME thread id: one "cli-cwd-<hex>" shared by
// the whole pool. The real client never does that — a thread id belongs to one
// machine, and one machine runs one account. Sending one thread id from
// seventeen identities is a cross-account correlation signal that needs no
// inference to read: group the traffic by thread id and the pool is visible.
//
// Scoping the digest by account gives each account a thread id of its own,
// derived the same way and still stable across calls. An empty account keeps
// the published value, which is what a single-account deployment should send.
func CwdThreadIDFor(cwd, account string) string {
	return CwdThreadIDForSlot(cwd, account, "")
}

// ThreadPoolSize is how many thread ids one account presents.
//
// A developer running the CLI works in several project directories at once, and
// the published client derives one thread id per directory (Znt), so one
// account legitimately presents a handful of them. The pool has no caller cwd
// — that never travels over HTTP — so it can only synthesise the equivalent: a
// fixed pool of directory-shaped thread ids that conversations are spread
// across. Ten is the size that keeps the picture ordinary: enough that one
// account is not pinned to a single "project" forever, few enough that it reads
// as one developer rather than a fleet.
const ThreadPoolSize = 10

// CwdThreadIDForSlot derives the account's thread id for one pool slot.
//
// slot is the project-shaped discriminator: the pool maps each conversation to
// one slot, so a conversation keeps its thread (like a project directory that
// keeps being used) while a different conversation lands elsewhere. An empty
// slot keeps the single-thread behaviour of CwdThreadIDFor.
//
// The digest is account + slot scoped, so two accounts never share a thread id
// even when they occupy the same slot — the cross-account signal the plain
// published formula would create.
func CwdThreadIDForSlot(cwd, account, slot string) string {
	p := strings.TrimSpace(cwd)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	material := abs
	if a := strings.TrimSpace(account); a != "" {
		// NUL separates the two so no account name can shift the boundary and
		// collide with another (cwd, account) pair.
		material = abs + "\x00" + a
	}
	if sl := strings.TrimSpace(slot); sl != "" {
		material += "\x00" + sl
	}
	sum := sha256.Sum256([]byte(material))
	return ThreadIDPrefix + hex.EncodeToString(sum[:])[:threadDigestLen]
}

// MachineIDName is the CLI's file name for the machine id, kept so an operator
// who exports a CLI home can find the same value.
const MachineIDName = "machine-id"

// ThreadSlot maps a conversation to one slot of the account's thread pool.
//
// The mapping is a stable hash rather than round-robin on purpose: a
// conversation must keep the same thread id across requests, which is what makes
// it look like "this project's directory" instead of a thread that changes
// underfoot. Different conversations land on different slots, so the account
// reads as one developer with several projects rather than one session.
//
// An empty fingerprint or a non-positive size returns "" — the caller then keeps
// a single thread id, which is the behaviour before the pool existed.
func ThreadSlot(fingerprint string, size int) string {
	fp := strings.TrimSpace(fingerprint)
	if fp == "" || size <= 0 {
		return ""
	}
	sum := sha256.Sum256([]byte("perch-thread-slot\x00" + fp))
	// A full 8-byte window keeps the distribution even for small pools; using
	// the first byte would bias nothing either way but reads as arbitrary.
	n := binary.BigEndian.Uint64(sum[:8]) % uint64(size)
	return "p" + strconv.FormatUint(n, 10)
}

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
