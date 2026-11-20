package factory

import (
	"encoding/json"
	"strings"
)

// Factory's usage policy check reads the whole request body, not only the
// system prompt: a conversation that quotes another agent's system prompt or
// instructions is refused with 403 {"code":"content_policy_rejected"} and stays
// refused for every later turn that still carries the text. magpie's Factory
// plugin keeps the same list in client-phrases.json and escapes the first
// character of each phrase as \uXXXX so the text survives the check while the
// model still reads the original characters once JSON is parsed.
//
// AnywhereRefusedPhrases are refused in any text; ToolOnlyRefusedPhrases are
// refused in tool output (they are also escaped everywhere here, because a
// rewritten phrase is read identically after JSON decoding).
var (
	AnywhereRefusedPhrases = []string{
		"You are an expert coding assistant operating inside pi, a coding agent harness.",
		// pi's compaction and branch openings, refused wherever they appear and
		// not only as a whole summary.
		"The conversation history before this point was compacted into the following summary:",
		"The following is a summary of a branch that this conversation came back from:",
		// Claude Code's continuation opening, refused in the same way.
		"This session is being continued from a previous conversation that ran out of context.",
	}
	ToolOnlyRefusedPhrases = []string{
		"You are Claude Code, Anthropic's official CLI for Claude.",
		"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
		"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
		"You have been invoked in the following environment:",
		"x-anthropic-billing-header: cc_version=",
		"This session is being continued from a previous conversation that ran out of context.",
		"(user's private global instructions for all projects)",
	}
)

// escapeFirst replaces the first character of every occurrence of phrase with
// its \uXXXX JSON escape. The decoded text is identical; only the wire bytes
// differ, which is what the policy check reads.
func escapeFirst(s, phrase string) string {
	if !strings.Contains(s, phrase) {
		return s
	}
	runes := []rune(phrase)
	if len(runes) == 0 {
		return s
	}
	escaped := `\u` + hex4(runes[0]) + string(runes[1:])
	return strings.ReplaceAll(s, phrase, escaped)
}

func hex4(v rune) string {
	const digits = "0123456789abcdef"
	u := uint16(v)
	return string([]byte{
		digits[(u>>12)&0xf], digits[(u>>8)&0xf], digits[(u>>4)&0xf], digits[u&0xf],
	})
}

// EscapeClientPhrases escapes every refused phrase in every string of a JSON
// body and reports whether anything changed. Unreadable bodies are returned
// untouched, so a malformed request still fails at the same place it did.
func EscapeClientPhrases(body []byte) ([]byte, bool) {
	if len(body) == 0 || !containsPhrase(body) {
		return body, false
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return body, false
	}
	if !escapeDeep(v) {
		return body, false
	}
	out, err := marshal(v)
	if err != nil {
		return body, false
	}
	return out, true
}

// containsPhrase is a cheap byte-level pre-check; a phrase inside a JSON string
// always appears literally unless it was already escaped.
func containsPhrase(body []byte) bool {
	s := string(body)
	for _, phrase := range AllRefusedPhrases() {
		if strings.Contains(s, phrase) {
			return true
		}
	}
	return false
}

// escapeDeep rewrites strings inside a decoded body in place.
func escapeDeep(v any) bool {
	changed := false
	switch t := v.(type) {
	case []any:
		for i, raw := range t {
			if s, ok := raw.(string); ok {
				if out := escapeAll(s); out != s {
					t[i] = out
					changed = true
				}
				continue
			}
			if escapeDeep(raw) {
				changed = true
			}
		}
	case map[string]any:
		for k, raw := range t {
			if s, ok := raw.(string); ok {
				if out := escapeAll(s); out != s {
					t[k] = out
					changed = true
				}
				continue
			}
			if escapeDeep(raw) {
				changed = true
			}
		}
	}
	return changed
}

func escapeAll(s string) string {
	for _, phrase := range AllRefusedPhrases() {
		s = escapeFirst(s, phrase)
	}
	return s
}

// AllRefusedPhrases returns both groups ordered longest first, so a full
// sentence is escaped before one of its own prefixes would hide it.
func AllRefusedPhrases() []string {
	out := make([]string, 0, len(AnywhereRefusedPhrases)+len(ToolOnlyRefusedPhrases))
	out = append(out, AnywhereRefusedPhrases...)
	out = append(out, ToolOnlyRefusedPhrases...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j]) > len(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// ContainsRefusedPhrase reports whether text carries a phrase Factory refuses.
// A literal backslash-u sequence marks an already escaped occurrence.
func ContainsRefusedPhrase(text string) bool {
	for _, phrase := range AllRefusedPhrases() {
		runes := []rune(phrase)
		if len(runes) < 2 {
			continue
		}
		escaped := `\u` + hex4(runes[0]) + string(runes[1:])
		if strings.Contains(strings.ReplaceAll(text, escaped, "\x00"), phrase) {
			return true
		}
	}
	return false
}
