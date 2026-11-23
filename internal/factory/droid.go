package factory

// Droid identity and request-body openings. The body rewriting follows the MIT
// magpie-community Factory plugin (packages/factory, index.mjs: droidBody,
// droidChat, anthropicBody); the wire identity follows the droid 1.14.0 capture
// in docs/factory-cli-endpoints.md (two User-Agents, request surface,
// traceparent, prem.factory.ai model plane). Factory serves a subscription's
// model requests only to droid, and droid's requests share two fixed traits
// besides the credential: the client headers below, and a system prompt that
// opens with DroidLine. A gateway that keeps its own User-Agent, or forwards
// another agent's first system line unchanged, is what the plugin reports as
// refused (magpie #242, #506). Task instructions and history are never
// rewritten; only the fixed opening is.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// DroidVersion is the droid release the requests claim to be when the operator
// sets nothing. droid's binary reads a server-side min_cli_version and Factory
// refuses an old release even when every other header matches, so the default
// follows the current droid release rather than the 1.14.0 capture the rest of
// this file is built from; the capture stays the reference for header values.
const DroidVersion = "0.242.0"

// DroidVersionOf returns the release to claim: the configured one when it is
// non-empty, otherwise DroidVersion.
func DroidVersionOf(configured string) string {
	if v := strings.TrimSpace(configured); v != "" {
		return v
	}
	return DroidVersion
}

// BunVersion is the runtime string droid's non-inference HTTP uses. The
// capture shows two User-Agents and mixing them is the visible mismatch:
// factory-cli/<DroidVersion> only on /api/llm/*, Bun/<BunVersion> everywhere
// else (whoami, billing, settings, telemetry, tools, session sync).
const BunVersion = "1.4.4"

// Request surfaces droid reports in X-Factory-Request-Surface.
const (
	SurfaceTUI  = "cli_tui"
	SurfaceExec = "cli_exec"
)

// RoutingSourceConfiguredOrder is how the gateway picks x-api-provider: the
// model's configured route, not a session-selected provider. The capture's
// other value, session_lock, describes droid reusing a conversation's locked
// provider; it is not claimed here because the mapping is model-based.
const RoutingSourceConfiguredOrder = "configured_order"

// DroidLine opens every droid system prompt.
const DroidLine = "You are Droid, an AI software engineering agent built by Factory."

// ClientIdentityHeaders are the identifiers a downstream client may echo back;
// the gateway replaces its downstream twin with droid's own value. X-Factory-Org-Id
// is deliberately absent: it belongs to the leased credential, and droid omits
// it when the account has no active organization.
var ClientIdentityHeaders = []string{
	"User-Agent",
	"X-Factory-Client",
	"X-Client-Version",
	"X-Session-Id",
	"X-Assistant-Message-Id",
	"X-Provider-Routing-Source",
	"X-Factory-Request-Surface",
	"X-Factory-Exec-Output-Format",
	"OpenAI-Platform",
	"Traceparent",
	"X-Api-Key",
	"X-Goog-Api-Key",
	"X-Anthropic-Billing-Header",
}

// ApplyClientHeaders writes droid's model-plane client headers.
func ApplyClientHeaders(h http.Header) {
	h.Set("User-Agent", "factory-cli/"+DroidVersion)
	h.Set("X-Factory-Client", "cli")
	h.Set("X-Client-Version", DroidVersion)
}

// ApplyManagementHeaders writes droid's non-inference client identity. whoami
// is the exception (see ApplyWhoamiHeaders): the capture sends the token, its
// active organization and the extended flag, and no client/version pair.
func ApplyManagementHeaders(h http.Header, surface string) {
	h.Set("User-Agent", "Bun/"+BunVersion)
	h.Set("X-Factory-Client", "cli")
	h.Set("X-Client-Version", DroidVersion)
	h.Set("X-Factory-Request-Surface", Surface(surface))
	if Surface(surface) == SurfaceExec {
		h.Set("X-Factory-Exec-Output-Format", "json")
	}
}

// ApplyWhoamiHeaders writes exactly what droid's whoami sends.
func ApplyWhoamiHeaders(h http.Header) {
	h.Set("User-Agent", "Bun/"+BunVersion)
	h.Set("X-Factory-Whoami-Extended", "true")
}

// Surface maps a configured value to one of droid's two surfaces.
func Surface(v string) string {
	if v == SurfaceExec {
		return SurfaceExec
	}
	return SurfaceTUI
}

// ApplyForwardHeaders writes the per-request headers droid puts on model
// calls: the provider routing label, the client surface, a W3C trace context
// and the conversation's ids. upstream is Factory's x-api-provider.
func ApplyForwardHeaders(h http.Header, upstream, session, messageID, surface, traceparent string) {
	h.Set("X-Api-Provider", upstream)
	h.Set("X-Provider-Routing-Source", RoutingSourceConfiguredOrder)
	h.Set("X-Factory-Request-Surface", Surface(surface))
	if Surface(surface) == SurfaceExec {
		h.Set("X-Factory-Exec-Output-Format", "json")
	}
	if traceparent != "" {
		h.Set("Traceparent", traceparent)
	}
	if session != "" {
		h.Set("X-Session-Id", session)
	}
	if messageID != "" {
		h.Set("X-Assistant-Message-Id", messageID)
	}
}

// ApplyProtocolForwardHeaders writes the subset of droid's model headers that
// the 1.14.0 capture shows for this wire protocol. Not every call carries the
// same set: droid sends traceparent for Responses and Gemini but not for Chat
// Completions or Messages, and the exec output format only appears when the
// request ran through exec. Sending a header the capture does not carry is a
// different client, not a closer copy of this one.
//
// protocol is the wire protocol after any conversion: chat, responses,
// messages or generate.
func ApplyProtocolForwardHeaders(h http.Header, protocol, upstream, session, messageID, surface, traceparent string) {
	h.Set("X-Api-Provider", upstream)
	h.Set("X-Provider-Routing-Source", RoutingSourceConfiguredOrder)
	h.Set("X-Factory-Request-Surface", Surface(surface))
	if Surface(surface) == SurfaceExec && (protocol == "responses" || protocol == "generate") {
		h.Set("X-Factory-Exec-Output-Format", "json")
	}
	if (protocol == "responses" || protocol == "generate") && traceparent != "" {
		h.Set("Traceparent", traceparent)
	}
	if session != "" {
		h.Set("X-Session-Id", session)
	}
	if messageID != "" {
		h.Set("X-Assistant-Message-Id", messageID)
	}
}

// ApplyAPIEndpointHeaders adds what Factory's Anthropic route expects beside
// the bearer token: droid's Anthropic client is made with the literal key
// "placeholder", which Anthropic's SDK sends as x-api-key.
func ApplyAPIEndpointHeaders(h http.Header) {
	h.Set("X-Api-Key", "placeholder")
}

// OpenDroidBody returns body, a request on the given Factory protocol, as droid
// would open it. On chat and Responses (Factory's /llm/o route) the system
// prompt starts with DroidLine; on Messages (/llm/a) a leading billing block is
// dropped and DroidLine is placed first, as anthropicBody does. Unreadable
// bodies, bodies already opened and bodies a JSON round-trip would change are
// returned byte for byte.
//
// The protocol, not a path suffix, decides: the adapter calls this after a
// cross-protocol conversion, where the local path and the wire path differ.
func OpenDroidBody(protocol string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	anthropic := protocol == "messages"
	if !anthropic && protocol != "chat" && protocol != "responses" {
		return body
	}
	path := "/responses"
	if protocol == "chat" {
		path = "/chat/completions"
	}
	var m object
	if decode(body, &m) != nil || m == nil {
		return body
	}
	changed := false
	if anthropic {
		changed = openAnthropic(m)
	} else {
		changed = openOpenAI(path, m)
	}
	if !changed {
		return body
	}
	out, err := marshal(m)
	if err != nil {
		return body
	}
	return out
}

// openOpenAI opens a Responses body's instructions or a chat body's first
// system message the way droidBody does.
func openOpenAI(path string, m object) bool {
	switch {
	case strings.HasSuffix(path, "/responses"):
		v, present := m["instructions"]
		if present && v != nil {
			s, ok := v.(string)
			if !ok {
				return false // not something droid sends
			}
			if strings.HasPrefix(s, DroidLine) {
				return false
			}
			m["instructions"] = openWith(s)
			return true
		}
		m["instructions"] = DroidLine
		return true
	case strings.HasSuffix(path, "/chat/completions"):
		return openChatRequest(m)
	}
	return false
}

func openChatRequest(m object) bool {
	msgs, ok := m["messages"].([]any)
	if !ok {
		return false
	}
	opened, changed := openChat(msgs)
	if changed {
		m["messages"] = opened
	}
	return changed
}

// PrepareChatBody runs the existing Chat prompt, tool-alias and opening
// adapters on one decoded object, in their original order. No-op bodies keep
// their original bytes. The caller still owns final phrase handling and the
// choice to use keep or fold mode instead.
func PrepareChatBody(body []byte, adaptPrompt, renameAll bool) (out []byte, aliases map[string]string, adapted bool) {
	var m object
	if decode(body, &m) != nil || m == nil {
		return body, nil, false
	}
	adapted = adaptPrompt && adaptChat(m)
	aliases = renameToolNames(m, renameAll)
	opened := openChatRequest(m)
	if !opened && !adapted && len(aliases) == 0 {
		return body, nil, false
	}
	out, err := marshal(m)
	if err != nil {
		return body, nil, false
	}
	return out, aliases, adapted
}

func openWith(s string) string {
	if strings.TrimSpace(s) == "" {
		return DroidLine
	}
	return DroidLine + "\n" + s
}

// openChat opens msgs' first system message with DroidLine, or puts one before
// them carrying the line alone.
func openChat(msgs []any) ([]any, bool) {
	if len(msgs) > 0 {
		if first, ok := msgs[0].(map[string]any); ok {
			if text(first["role"]) == "system" {
				switch c := first["content"].(type) {
				case string:
					if strings.HasPrefix(c, DroidLine) {
						return msgs, false
					}
					first["content"] = openWith(c)
					return msgs, true
				case []any:
					// droid sends one string: text parts alone are joined.
					texts := make([]string, 0, len(c)+1)
					texts = append(texts, DroidLine)
					joined := true
					for i, raw := range c {
						p, ok := raw.(map[string]any)
						if !ok || text(p["type"]) != "text" {
							joined = false
							break
						}
						s, ok := p["text"].(string)
						if !ok {
							joined = false
							break
						}
						if i == 0 && strings.HasPrefix(s, DroidLine) {
							return msgs, false
						}
						texts = append(texts, s)
					}
					if joined {
						first["content"] = strings.Join(texts, "\n")
					} else {
						first["content"] = append([]any{map[string]any{"type": "text", "text": DroidLine}}, c...)
					}
					return msgs, true
				}
			}
		}
	}
	opened := make([]any, 0, len(msgs)+1)
	opened = append(opened, map[string]any{"role": "system", "content": DroidLine})
	opened = append(opened, msgs...)
	return opened, true
}

// openAnthropic consumes any leading billing header block (droid's client
// sends none) and puts DroidLine first, as anthropicBody does.
func openAnthropic(m object) bool {
	changed := false
	switch s := m["system"].(type) {
	case string:
		if strings.HasPrefix(s, "x-anthropic-billing-header: cc_version=") {
			changed = true
			if rest := afterFirstLine(s); strings.TrimSpace(rest) != "" {
				m["system"] = rest
			} else {
				delete(m, "system")
			}
			s = text(m["system"])
		}
		if s != "" && !strings.HasPrefix(s, DroidLine) {
			m["system"] = openWith(s)
			changed = true
		}
	case []any:
		kept := make([]any, 0, len(s)+1)
		for _, raw := range s {
			b, ok := raw.(map[string]any)
			if !ok || text(b["type"]) != "text" {
				kept = append(kept, raw)
				continue
			}
			t := text(b["text"])
			if strings.TrimSpace(t) == "" {
				changed = true
				continue
			}
			if strings.HasPrefix(t, "x-anthropic-billing-header: cc_version=") {
				changed = true
				// Only the header's own line goes: instructions after it stay.
				if rest := afterFirstLine(t); strings.TrimSpace(rest) != "" {
					b["text"] = rest
				} else {
					continue
				}
			}
			kept = append(kept, raw)
		}
		identity := -1
		for i, raw := range kept {
			b, _ := raw.(map[string]any)
			if text(b["type"]) == "text" && strings.HasPrefix(text(b["text"]), DroidLine) {
				identity = i
				break
			}
		}
		switch {
		case identity < 0:
			kept = append([]any{map[string]any{"type": "text", "text": DroidLine}}, kept...)
			changed = true
		case identity > 0:
			head := kept[identity]
			moved := append([]any{head}, kept[:identity]...)
			kept = append(moved, kept[identity+1:]...)
			changed = true
		}
		// Duplicate identity-only blocks are noise; a block carrying
		// instructions after the line stays intact.
		for i := len(kept) - 1; i > 0; i-- {
			if b, _ := kept[i].(map[string]any); text(b["type"]) == "text" && text(b["text"]) == DroidLine {
				kept = append(kept[:i], kept[i+1:]...)
				changed = true
			}
		}
		if changed {
			m["system"] = kept
		}
	}
	if m["system"] == nil && !changed {
		m["system"] = DroidLine
		changed = true
	}
	return changed
}

// marshal re-encodes a decoded body. Numbers stay json.Number, so an integer
// beyond 2^53 is written back exactly instead of as a rounded float.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func afterFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return ""
}
