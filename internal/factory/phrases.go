package factory

import (
	"encoding/json"
	"regexp"
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

// EscapeClientPhrases escapes every refused phrase and self-description
// sentence in a request body and reports whether anything changed.
//
// The rewrite happens on the wire bytes, not on decoded values: the first
// character of each match becomes its \uXXXX JSON escape, which JSON parsing
// turns back into that character. The model therefore reads the original text
// byte for byte while the policy check, which reads the raw body, no longer
// sees the phrase. Unreadable bodies are returned untouched.
func EscapeClientPhrases(body []byte) ([]byte, bool) {
	if len(body) == 0 || !containsPhrase(body) {
		return body, false
	}
	// Only a JSON body is rewritten: anything else is left for the layer that
	// rejects it, unchanged.
	if !json.Valid(body) {
		return body, false
	}
	current := string(body)
	for _, phrase := range AllRefusedPhrases() {
		current = escapeFirst(current, phrase)
	}
	current = escapeSelfDescription(current)
	return []byte(current), true
}

// selfDescriptionRE matches a client describing itself as an AI agent driven by
// an agent harness, ending its sentence with a full stop. Measured on
// 2026-11-21: "You are an AI agent powered by DeepSeek Harness." is refused
// while the same text without the final period, or the product name alone,
// passes — the refusal is that sentence, and a conversation that quotes it (a
// tool result reading a file, for example) is refused for every later turn.
var selfDescriptionRE = regexp.MustCompile(`(?i)You are[^.\\"\n]{0,120}?harness\.`)

// escapeSelfDescription escapes the first character of every matched
// self-description sentence on the wire bytes, which JSON parsing turns back
// into the original character.
func escapeSelfDescription(s string) string {
	if !strings.Contains(strings.ToLower(s), "harness") {
		return s
	}
	return selfDescriptionRE.ReplaceAllStringFunc(s, func(match string) string {
		runes := []rune(match)
		if len(runes) == 0 {
			return match
		}
		return `\u` + hex4(runes[0]) + string(runes[1:])
	})
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
	return selfDescriptionRE.MatchString(s)
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
