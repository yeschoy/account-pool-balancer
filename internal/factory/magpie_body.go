package factory

import (
	"bytes"
	"encoding/json"
	"strings"
)

// MagpieAdapt applies magpie's prompt adapters to a Factory request body and
// reports whether anything changed. The protocol decides where a prompt lives:
//
//	chat       messages[role=system] as the prompt, later system/developer
//	           messages as system messages, user turns and tool output
//	responses  instructions as the prompt, later system items, input turns,
//	           function_call_output
//	messages   the top-level system blocks as the prompt, then the same roles
//
// Tool descriptions and their schema descriptions are adapted too, because
// Factory reads a client's self-description there as well.
//
// A body that needs no change comes back byte for byte.
func MagpieAdapt(protocol string, body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return body, false
	}
	var m object
	if decode(body, &m) != nil || m == nil {
		return body, false
	}
	changed := false
	switch protocol {
	case "chat":
		changed = adaptChat(m)
	case "responses":
		changed = adaptResponses(m)
	case "messages":
		changed = adaptMessages(m)
	default:
		return body, false
	}
	if !changed {
		return body, false
	}
	out, err := marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}

// adaptChat walks a chat completions body.
func adaptChat(m object) bool {
	changed := false
	if msgs, ok := m["messages"].([]any); ok {
		changed = adaptOpenAIMessages(msgs) || changed
		if droidChatOpens(msgs) {
			changed = true
		}
	}
	if tools, ok := m["tools"].([]any); ok && ToolDescriptions(tools) {
		changed = true
	}
	return changed
}

// adaptResponses walks an OpenAI Responses body.
func adaptResponses(m object) bool {
	changed := false
	if v, ok := m["instructions"]; ok {
		if s, ok := v.(string); ok {
			adapted := PromptText(s)
			if !strings.HasPrefix(adapted, DroidLine) {
				adapted = openWith(adapted)
			}
			if adapted != s {
				m["instructions"] = adapted
				changed = true
			}
		}
	}
	if input, ok := m["input"].([]any); ok {
		if adaptOpenAIMessages(input) {
			changed = true
		}
	}
	if tools, ok := m["tools"].([]any); ok && ToolDescriptions(tools) {
		changed = true
	}
	return changed
}

// adaptOpenAIMessages adapts chat messages or Responses input items, and
// returns whether anything changed.
func adaptOpenAIMessages(items []any) bool {
	changed := false
	for i, raw := range items {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch text(msg["role"]) {
		case "system", "developer":
			adapt := SystemMessageText
			if i == 0 && text(msg["role"]) == "system" {
				adapt = PromptText
			}
			if adapted, c := adaptContent(msg, "content", adapt); c {
				items[i] = adapted
				changed = true
			}
		case "user":
			if adapted, c := adaptContent(msg, "content", UserBlockAdapt, UserTextAdapt); c {
				items[i] = adapted
				changed = true
			}
		case "":
			// Responses input items carry no role; only a function output
			// reaches a tool result.
		}
		if text(msg["type"]) == "function_call_output" {
			if s, ok := msg["output"].(string); ok {
				if out := quotedToolText(s); out != s {
					msg["output"] = out
					changed = true
				}
			}
		}
		if text(msg["role"]) == "tool" {
			if s, ok := msg["content"].(string); ok {
				if out := quotedToolText(s); out != s {
					msg["content"] = out
					changed = true
				}
			}
		}
	}
	return changed
}

// adaptContent applies stringAdapt to a string field, or blockAdapt to each
// text part of a block list.
func adaptContent(msg map[string]any, field string, stringAdapt func(string) string, blockAdapt ...func(string) string) (map[string]any, bool) {
	perBlock := stringAdapt
	if len(blockAdapt) > 0 {
		perBlock = blockAdapt[0]
	}
	switch c := msg[field].(type) {
	case string:
		if out := stringAdapt(c); out != c {
			msg[field] = out
			return msg, true
		}
	case []any:
		changed := false
		for _, raw := range c {
			part, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			s, ok := part["text"].(string)
			if !ok {
				continue
			}
			if out := perBlock(s); out != s {
				part["text"] = out
				changed = true
			}
		}
		return msg, changed
	}
	return msg, false
}

// UserBlockAdapt adapts one user text part; a part is a block of its own, so
// no joined reminder bundle has to be split.
func UserBlockAdapt(s string) string { return UserTextBlock(s) }

// UserTextAdapt is the adapter for a joined user string.
func UserTextAdapt(s string) string { return UserText(s) }

// droidChatOpens reports whether the first system message now opens with
// droid's line, adding it when the body has no system message at all.
func droidChatOpens(msgs []any) bool {
	if len(msgs) == 0 {
		return false
	}
	first, ok := msgs[0].(map[string]any)
	if !ok || text(first["role"]) != "system" {
		return false
	}
	switch c := first["content"].(type) {
	case string:
		if strings.HasPrefix(c, DroidLine) {
			return false
		}
		first["content"] = openWith(c)
		return true
	}
	return false
}

// adaptMessages walks an Anthropic Messages body: the top-level system block
// list is the prompt, then the same role rules as the OpenAI routes.
func adaptMessages(m object) bool {
	changed := false
	switch s := m["system"].(type) {
	case string:
		if adapted := PromptText(s); adapted != s {
			m["system"] = adapted
			changed = true
		}
		if v := text(m["system"]); !strings.HasPrefix(v, DroidLine) {
			m["system"] = openWith(v)
			changed = true
		}
	case []any:
		if out, c := adaptSystemBlocks(s); c {
			m["system"] = out
			changed = true
		}
	}
	if m["system"] == nil && !changed {
		m["system"] = DroidLine
		changed = true
	}
	if msgs, ok := m["messages"].([]any); ok {
		for i, raw := range msgs {
			msg, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			switch text(msg["role"]) {
			case "user":
				if adapted, c := adaptContent(msg, "content", UserTextAdapt, UserBlockAdapt); c {
					msgs[i] = adapted
					changed = true
				}
			case "system":
				if adapted, c := adaptContent(msg, "content", SystemMessageText); c {
					msgs[i] = adapted
					changed = true
				}
			}
			if blocks, ok := msg["content"].([]any); ok {
				for _, raw := range blocks {
					block, ok := raw.(map[string]any)
					if !ok || text(block["type"]) != "tool_result" {
						continue
					}
					switch tc := block["content"].(type) {
					case string:
						if out := quotedToolText(tc); out != tc {
							block["content"] = out
							changed = true
						}
					case []any:
						for _, inner := range tc {
							ip, ok := inner.(map[string]any)
							if !ok {
								continue
							}
							if s, ok := ip["text"].(string); ok {
								if out := quotedToolText(s); out != s {
									ip["text"] = out
									changed = true
								}
							}
						}
					}
				}
			}
		}
	}
	if tools, ok := m["tools"].([]any); ok && ToolDescriptions(tools) {
		changed = true
	}
	return changed
}

// adaptSystemBlocks ports anthropicBody: drop empty and billing blocks, adapt
// each text block, put droid's line first and remove duplicate identity-only
// blocks. A block carrying instructions stays intact.
func adaptSystemBlocks(system []any) ([]any, bool) {
	changed := false
	kept := make([]any, 0, len(system)+1)
	for _, raw := range system {
		block, ok := raw.(map[string]any)
		if !ok || text(block["type"]) != "text" {
			kept = append(kept, raw)
			continue
		}
		t := text(block["text"])
		if strings.TrimSpace(t) == "" {
			changed = true
			continue
		}
		if strings.HasPrefix(t, "x-anthropic-billing-header: cc_version=") {
			changed = true
			if rest := afterFirstLine(t); strings.TrimSpace(rest) != "" {
				block["text"] = rest
			} else {
				continue
			}
		}
		if adapted := PromptText(text(block["text"])); adapted != text(block["text"]) {
			block["text"] = adapted
			changed = true
		}
		kept = append(kept, block)
	}
	identity := -1
	for i, raw := range kept {
		block, _ := raw.(map[string]any)
		if text(block["type"]) == "text" && strings.HasPrefix(text(block["text"]), DroidLine) {
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
	for i := len(kept) - 1; i > 0; i-- {
		if block, _ := kept[i].(map[string]any); text(block["type"]) == "text" && text(block["text"]) == DroidLine {
			kept = append(kept[:i], kept[i+1:]...)
			changed = true
		}
	}
	return kept, changed
}

// quotedToolText wraps tool output Factory refuses in a JSON string, escaping
// the refused phrases so the model can decode the exact original text.
func quotedToolText(s string) string {
	if !ContainsRefusedPhrase(s) {
		return s
	}
	encoded, err := marshalString(s)
	if err != nil {
		return s
	}
	for _, phrase := range AllRefusedPhrases() {
		if !strings.Contains(encoded, phrase) {
			continue
		}
		escaped := `\\u` + hex4(rune(phrase[0])) + phrase[1:]
		encoded = strings.ReplaceAll(encoded, phrase, escaped)
	}
	return QuotedToolPrefix + encoded
}

// marshalString quotes s as a JSON string without HTML escaping.
func marshalString(s string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return "", err
	}
	return strings.TrimRight(buf.String(), "\n"), nil
}

// QuotedToolPrefix explains the wrapper to the model.
const QuotedToolPrefix = "Tool output encoded as a JSON string. Decode the JSON string to recover the exact original text before using it:\n"

// MagpieAdaptGemini applies the prompt adapters to a Google generateContent
// body's systemInstruction. The generated route keeps the instruction in
// systemInstruction.parts, so only those text parts are rewritten; the user's
// own contents are left alone and the opening is added by OpenGeminiBodyWith.
func MagpieAdaptGemini(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	// Only the instruction is decoded; contents keep their original bytes.
	top, err := decodeTop(body)
	if err != nil {
		return body
	}
	raw, present, err := top.field("systemInstruction")
	if err != nil || !present {
		return body
	}
	m := object{"systemInstruction": raw}
	changed := false
	switch si := m["systemInstruction"].(type) {
	case string:
		if adapted := PromptText(si); adapted != si {
			m["systemInstruction"] = adapted
			changed = true
		}
	case map[string]any:
		if parts, ok := si["parts"].([]any); ok {
			for _, raw := range parts {
				part, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				s, ok := part["text"].(string)
				if !ok {
					continue
				}
				if adapted := PromptText(s); adapted != s {
					part["text"] = adapted
					changed = true
				}
			}
		}
	}
	if !changed || top.set("systemInstruction", m["systemInstruction"]) != nil {
		return body
	}
	out, err := top.marshal()
	if err != nil {
		return body
	}
	return out
}
