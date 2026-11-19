package factory

import (
	"errors"
	"strings"
)

var (
	errNotObject  = errors.New("Factory Gemini request must be a JSON object")
	errNoContents = errors.New("Factory Gemini request requires a contents array")
)

// FoldSystemIntoUser moves a request's system channel into the first user turn
// and removes the system field. Factory's usage policy check reads the whole
// body: a system field carrying anything but droid's own prompt is refused, and
// so is an empty one (pi-provider-factory measured 403 for both "stripped" and
// "generic content" on all three routes). Leaving the field out entirely is
// accepted, and keeping the caller's instructions as ordinary user text is what
// magpie's adapters do.
//
// The protocol decides where the text lives:
//
//	chat       messages[role=system]
//	responses  instructions
//	messages   system (string or blocks)
//
// A body without system content is returned unchanged, so a caller that already
// sends none pays nothing. Unreadable bodies are returned byte for byte.
func FoldSystemIntoUser(protocol string, body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var m object
	if decode(body, &m) != nil || m == nil {
		return body
	}
	var changed bool
	switch protocol {
	case "chat":
		changed = foldChatSystem(m)
	case "responses":
		changed = foldResponsesInstructions(m)
	case "messages":
		changed = foldAnthropicSystem(m)
	default:
		return body
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

// systemPreamble wraps folded text so the model still sees it as an instruction
// block rather than the user's opening sentence.
func systemPreamble(s string) string {
	return "<system>\n" + s + "\n</system>\n\n"
}

// foldChatSystem pulls system messages out of messages[]. Factory accepts a
// system role only when it carries droid's own prompt, so every system message
// is joined into the first user turn and removed from the array.
func foldChatSystem(m object) bool {
	msgs, ok := m["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return false
	}
	var texts []string
	kept := make([]any, 0, len(msgs))
	for _, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok || text(msg["role"]) != "system" {
			kept = append(kept, raw)
			continue
		}
		if t := messageText(msg["content"]); strings.TrimSpace(t) != "" {
			texts = append(texts, t)
		}
	}
	if len(texts) == 0 {
		return false
	}
	m["messages"] = prependToFirstUser(kept, strings.Join(texts, "\n\n"))
	return true
}

// foldResponsesInstructions moves the instructions field into the first user
// input item. Instructions travel as a user turn because Factory refuses the
// field's ordinary content.
func foldResponsesInstructions(m object) bool {
	v, present := m["instructions"]
	if !present || v == nil {
		return false
	}
	joined := messageText(v)
	delete(m, "instructions")
	if joined == "" {
		return true // it carried nothing: removing it is the whole change
	}
	m["input"] = prependToResponsesInput(m["input"], joined)
	return true
}

func prependToResponsesInput(input any, systemText string) any {
	switch in := input.(type) {
	case string:
		return systemPreamble(systemText) + in
	case []any:
		for i, raw := range in {
			item, ok := raw.(map[string]any)
			if !ok || text(item["role"]) != "user" {
				continue
			}
			item["content"] = prependText(item["content"], "input_text", systemText)
			in[i] = item
			return in
		}
		return append([]any{map[string]any{"role": "user", "content": systemPreamble(systemText)}}, in...)
	}
	return input
}

// foldAnthropicSystem moves the top-level system (string or text blocks) into
// the first user turn. Non-text blocks are kept verbatim after the joined text.
func foldAnthropicSystem(m object) bool {
	v, present := m["system"]
	if !present || v == nil {
		return false
	}
	joined, extra := splitSystemParts(v)
	delete(m, "system")
	if strings.TrimSpace(joined) == "" && len(extra) == 0 {
		return true
	}
	msgs, ok := m["messages"].([]any)
	if !ok {
		// Nothing to attach to: put the text back rather than dropping it.
		m["system"] = v
		return false
	}
	preamble := ""
	if strings.TrimSpace(joined) != "" {
		preamble = systemPreamble(joined)
	}
	for i, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok || text(msg["role"]) != "user" {
			continue
		}
		msg["content"] = prependText(msg["content"], "text", strings.TrimSuffix(preamble, "\n\n"))
		if len(extra) > 0 {
			if blocks, ok := msg["content"].([]any); ok {
				msg["content"] = append(blocks, extra...)
			}
		}
		msgs[i] = msg
		return true
	}
	content := append([]any{map[string]any{"type": "text", "text": strings.TrimSuffix(preamble, "\n\n")}}, extra...)
	m["messages"] = append([]any{map[string]any{"role": "user", "content": content}}, msgs...)
	return true
}

// splitSystemParts joins a system value's text and returns the non-text blocks
// that must travel with it.
func splitSystemParts(v any) (string, []any) {
	switch s := v.(type) {
	case string:
		return s, nil
	case []any:
		var texts []string
		var extra []any
		for _, raw := range s {
			block, ok := raw.(map[string]any)
			if !ok || text(block["type"]) != "text" {
				extra = append(extra, raw)
				continue
			}
			if t := text(block["text"]); strings.TrimSpace(t) != "" {
				texts = append(texts, t)
			}
		}
		return strings.Join(texts, "\n\n"), extra
	}
	return "", nil
}

// messageText flattens a content value (string, blocks or parts) to text.
func messageText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var out []string
		for _, raw := range c {
			switch part := raw.(type) {
			case string:
				if strings.TrimSpace(part) != "" {
					out = append(out, part)
				}
			case map[string]any:
				if t := text(part["text"]); strings.TrimSpace(t) != "" {
					out = append(out, t)
				}
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// prependToFirstUser puts systemText in front of the first user message.
func prependToFirstUser(msgs []any, systemText string) []any {
	preamble := strings.TrimSuffix(systemPreamble(systemText), "\n\n")
	for i, raw := range msgs {
		msg, ok := raw.(map[string]any)
		if !ok || text(msg["role"]) != "user" {
			continue
		}
		msg["content"] = prependText(msg["content"], "text", preamble)
		msgs[i] = msg
		return msgs
	}
	return append([]any{map[string]any{"role": "user", "content": preamble}}, msgs...)
}

// prependText puts prefix in front of a content value, keeping its shape: a
// string stays a string, and a block list gains one text block with blockType.
func prependText(content any, blockType, prefix string) any {
	if prefix == "" {
		return content
	}
	switch c := content.(type) {
	case nil:
		return []any{map[string]any{"type": blockType, "text": prefix}}
	case string:
		if c == "" {
			return prefix
		}
		return prefix + "\n" + c
	case []any:
		return append([]any{map[string]any{"type": blockType, "text": prefix}}, c...)
	}
	return content
}

// FoldGeminiSystem does the same for a Google generateContent body: the system
// instruction becomes a leading text part of the first user content.
func FoldGeminiSystem(model string, body []byte) ([]byte, error) {
	var m object
	if decode(body, &m) != nil || m == nil {
		return nil, errNotObject
	}
	contents, ok := m["contents"].([]any)
	if !ok || contents == nil {
		return nil, errNoContents
	}
	changed := false
	if existing, ok := m["model"].(string); !ok || existing != model {
		m["model"] = model
		changed = true
	}
	if v, present := m["systemInstruction"]; present && v != nil {
		// The instruction's parts are Gemini text parts ({"text":…}), not
		// Anthropic blocks, so the text is read directly.
		joined := geminiInstructionText(v)
		delete(m, "systemInstruction")
		changed = true
		if strings.TrimSpace(joined) != "" {
			m["contents"] = prependToFirstGeminiUser(contents, systemPreamble(joined))
		}
	}
	if !changed {
		return body, nil
	}
	out, err := marshal(m)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// geminiInstructionText joins a systemInstruction's text parts.
func geminiInstructionText(v any) string {
	switch si := v.(type) {
	case string:
		return si
	case map[string]any:
		parts, _ := si["parts"].([]any)
		var texts []string
		for _, raw := range parts {
			if p, ok := raw.(map[string]any); ok {
				if t := text(p["text"]); strings.TrimSpace(t) != "" {
					texts = append(texts, t)
				}
			}
		}
		return strings.Join(texts, "\n")
	}
	return ""
}

func prependToFirstGeminiUser(contents []any, preamble string) []any {
	for i, raw := range contents {
		c, ok := raw.(map[string]any)
		if !ok || text(c["role"]) != "user" {
			continue
		}
		parts, _ := c["parts"].([]any)
		c["parts"] = append([]any{map[string]any{"text": strings.TrimSuffix(preamble, "\n\n")}}, parts...)
		contents[i] = c
		return contents
	}
	return append([]any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": strings.TrimSuffix(preamble, "\n\n")}}}}, contents...)
}
