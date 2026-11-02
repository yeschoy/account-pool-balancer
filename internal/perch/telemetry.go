package perch

// Telemetry, reconstructed from the published CLI.
//
// The CLI has two independent reporters, and conflating them is the mistake to
// avoid (analysis.md §设备、指纹及遥测):
//
//  1. PostHog anonymous events (`wq` and friends) at
//     https://us.i.posthog.com/i/v0/e/: a JSON body of api_key, event,
//     distinct_id and properties, with distinct_id taken from machine-id. The
//     event set is fixed — cli_first_run, cli_session_started, message_sent,
//     first_message, upgrade_prompt_shown — and the properties are a handful of
//     scalars (version, platform, surface, acp_client, placement, target,
//     trigger, limitKind). None of them carry the prompt.
//     Three opt-outs are honoured: CI, PERCH_NO_TELEMETRY and DO_NOT_TRACK.
//     In 2.4.121 the api key compiled in is empty and the wrappers
//     short-circuit on it, so the shipped client sends nothing at all; the
//     implementation is still there and still switchable on.
//
//  2. A conversation record, POST /api/perch-terminal/cli-turn, which is not
//     anonymous: prompt, assistantText, threadId, runId, events and so on. It
//     requires an access token, has its own budget (1 attempt for a streaming
//     or run-less status, 3 for a final status with a runId, 45 s each, 1 s
//     exponential backoff between them), and — unlike PostHog — ignores all
//     three opt-out variables. That asymmetry is reproduced here deliberately,
//     and the pool widens it: its own switch governs both.
//
// What this file does NOT do is send. Both reporters are constructed as data
// and handed to a Sink; the caller decides whether anything leaves the process.
// That is the conservative default the pool ships with, because the analysis
// found no evidence that the original pool ever emitted these calls.

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// ---- PostHog --------------------------------------------------------------------

const (
	// PostHogEndpoint is the CLI's ingest host; `A2i` trims a trailing slash
	// and appends /i/v0/e/.
	PostHogEndpoint = "https://us.i.posthog.com"
	// PostHogPath is the capture path.
	PostHogPath = "/i/v0/e/"
	// PostHogLib is the literal $lib property the CLI sets.
	PostHogLib = "perch-cli"
	// PostHogTimeout is `y2i`: 2500 ms, then the request is abandoned silently.
	PostHogTimeout = 2500 * time.Millisecond
)

// PostHog event names, verbatim from the bundle.
const (
	EventFirstRun       = "cli_first_run"
	EventSessionStarted = "cli_session_started"
	EventMessageSent    = "message_sent"
	EventFirstMessage   = "first_message"
	EventUpgradePrompt  = "upgrade_prompt_shown"
)

// OptOutEnv are the CLI's opt-outs (`_ee`): any truthy value disables PostHog.
var OptOutEnv = []string{"CI", "PERCH_NO_TELEMETRY", "DO_NOT_TRACK"}

// OptedOut reports whether any opt-out environment variable is non-empty.
// Even values such as "0" and "false" disable reporting, as in the CLI.
func OptedOut(getenv func(string) string) bool {
	if getenv == nil {
		return false
	}
	for _, k := range OptOutEnv {
		if getenv(k) != "" {
			return true
		}
	}
	return false
}

// PostHogEvent is the capture body. Property order is irrelevant; the field
// set is the CLI's.
type PostHogEvent struct {
	APIKey     string         `json:"api_key"`
	Event      string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Properties map[string]any `json:"properties"`
}

// BuildPostHogEvent assembles one event. properties may be nil; $lib is added
// last and wins, as it does in the CLI where it is spread after the caller's.
func BuildPostHogEvent(apiKey, event, distinctID string, properties map[string]any) PostHogEvent {
	p := make(map[string]any, len(properties)+1)
	for k, v := range properties {
		p[k] = v
	}
	p["$lib"] = PostHogLib
	return PostHogEvent{APIKey: apiKey, Event: event, DistinctID: distinctID, Properties: p}
}

// PostHogURL is the full capture URL for a configured host.
func PostHogURL(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		b = PostHogEndpoint
	}
	return b + PostHogPath
}

// ---- cli-turn -------------------------------------------------------------------

// CLITurnPath is appended to the app URL the way `WMo` does it: the base is
// trimmed of trailing slashes, and a base that already ends in the path is
// accepted as-is rather than doubled.
const CLITurnPath = "/api/perch-terminal/cli-turn"

// CLITurnURL resolves the cli-turn endpoint for an app URL.
func CLITurnURL(appURL string) string {
	b := strings.TrimRight(strings.TrimSpace(appURL), "/")
	if b == "" {
		return ""
	}
	if strings.HasSuffix(b, CLITurnPath) {
		return b
	}
	return b + CLITurnPath
}

// Statuses the CLI reports. streaming is a progress ping; the others are final.
const (
	TurnStatusStreaming  = "streaming"
	TurnStatusComplete   = "complete"
	TurnStatusError      = "error"
	TurnStatusAborted    = "aborted"
	TurnStatusSuperseded = "superseded"
)

// Retry budget (`Zue`): a streaming or run-less status gets 1 attempt, a final
// status with a runId gets qMo (3). Per-attempt timeout is 45 s, 12 s for the
// streaming ping, and the gap between attempts is 1 s, 2 s, 4 s…
const (
	TurnAttemptsFinal     = 3
	TurnAttemptsStreaming = 1
	TurnTimeoutFinal      = 45 * time.Second
	TurnTimeoutStreaming  = 12 * time.Second
)

// TurnDraft is what the pool knows about a turn before it is reported: the
// conversation, the answer, and how the turn ended. RedactDraft turns it into a
// stored form with no conversation text in it.
type TurnDraft struct {
	Thread  string
	Prompt  string
	Answer  string
	Text    string
	RunID   string
	Mode    string
	Model   string
	Finish  string
	Started time.Time
	Err     string
}

// CLITurn is the cli-turn body, field for field. Pointers are used where the
// CLI sends an explicit null (personaId, error, contextSnapshot) so an omitted
// value and a null stay distinguishable on the wire.
type CLITurn struct {
	Prompt          string         `json:"prompt"`
	AssistantText   string         `json:"assistantText"`
	ThreadID        string         `json:"threadId"`
	RunID           string         `json:"runId"`
	ChatMode        string         `json:"chatMode"`
	PersonaID       *string        `json:"personaId"`
	Status          string         `json:"status"`
	DurationMs      int64          `json:"durationMs"`
	Error           *string        `json:"error"`
	Events          []any          `json:"events"`
	ContextSnapshot map[string]any `json:"contextSnapshot"`
	SystemInitiated bool           `json:"systemInitiated"`
	Attachments     []any          `json:"attachments"`
	AgentChat       map[string]any `json:"agentChat,omitempty"`
}

// TurnAttempts is how many POSTs a status gets.
func TurnAttempts(status, runID string) int {
	if status == TurnStatusStreaming || strings.TrimSpace(runID) == "" {
		return TurnAttemptsStreaming
	}
	return TurnAttemptsFinal
}

// TurnTimeout is the per-attempt timeout for a status.
func TurnTimeout(status string) time.Duration {
	if status == TurnStatusStreaming {
		return TurnTimeoutStreaming
	}
	return TurnTimeoutFinal
}

// TurnBackoff is the wait before attempt n+1 given n (1-based): 1 s, 2 s, 4 s.
func TurnBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(1) * time.Second << (attempt - 1)
}

// RedactTurn returns only non-identifying fields with a recognized status.
func RedactTurn(t CLITurn) CLITurn {
	status := "unknown"
	switch t.Status {
	case TurnStatusStreaming, TurnStatusComplete, TurnStatusError, TurnStatusAborted, TurnStatusSuperseded:
		status = t.Status
	}
	return CLITurn{Status: status, DurationMs: t.DurationMs}
}

// ---- sink -----------------------------------------------------------------------

// HTTPClientForProxy exposes the same per-proxy transport used by account calls.
// An empty proxy retains the client's configured transport; its route is unknown.
func (c *Client) HTTPClientForProxy(proxyURL string) (*http.Client, error) {
	return c.clientFor(proxyURL)
}

// Sink receives what the pool would report. The production sink audits and, if
// enabled, forwards; tests use their own.
type Sink interface {
	// PostHog reports one anonymous event. err is advisory: the CLI swallows
	// every failure of this call.
	PostHog(ctx context.Context, ev PostHogEvent) error
	// CLITurn reports one conversation record. The implementation owns the
	// retry budget; the caller reports once.
	CLITurn(ctx context.Context, endpoint, token string, turn CLITurn) error
}

// httpPoster is the shared transport for both reporters.
func httpPostJSON(ctx context.Context, hc *http.Client, url string, body []byte, headers map[string]string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	buf := make([]byte, 0, 256)
	tmp := make([]byte, 256)
	for {
		n, rerr := res.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil || len(buf) > 1<<20 {
			break
		}
	}
	return res.StatusCode, buf, nil
}
