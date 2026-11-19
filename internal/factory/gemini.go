package factory

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// geminiPathRE is the Google generateContent surface NewAPI's Gemini channel
// and the Google SDK use. Factory takes the same body at one path,
// /api/llm/g/v1/generate, with the model in the body instead of the path.
var geminiPathRE = regexp.MustCompile(`^/(?:v1beta|v1)/models/([^/:]+):(stream)?[Gg]enerateContent$`)
var geminiCountRE = regexp.MustCompile(`^/(?:v1beta|v1)/models/([^/:]+):countTokens$`)

// ParseGeminiPath returns the model and whether the caller asked for the
// streaming variant. countTokens has its own parser; embeddings has its own
// OpenAI surface.
func ParseGeminiPath(path string) (model string, stream, ok bool) {
	m := geminiPathRE.FindStringSubmatch(path)
	if m == nil {
		return "", false, false
	}
	model, err := url.PathUnescape(m[1])
	if err != nil || model == "" {
		return "", false, false
	}
	return model, m[2] == "stream", true
}

// ParseGeminiCountTokens recognizes the Google countTokens surface. Factory's
// generate route is unified at /generate, so countTokens is mapped to the
// matching /countTokens path with the model in the body.
func ParseGeminiCountTokens(path string) (model string, ok bool) {
	m := geminiCountRE.FindStringSubmatch(path)
	if m == nil {
		return "", false
	}
	model, err := url.PathUnescape(m[1])
	if err != nil || model == "" {
		return "", false
	}
	return model, true
}

// OpenGeminiBody turns a Google generateContent body into what droid sends to
// Factory's /generate: the path's model in the body and a systemInstruction
// that opens with DroidLine. Other fields (contents, tools, generationConfig,
// safety settings) are kept as they arrived. A malformed body is rejected
// before any account is leased, rather than forwarded to a wrong path.
func OpenGeminiBody(model string, body []byte) ([]byte, error) {
	var m object
	if decode(body, &m) != nil || m == nil {
		return nil, errors.New("Factory Gemini request must be a JSON object")
	}
	contents, ok := m["contents"].([]any)
	if !ok || contents == nil {
		return nil, errors.New("Factory Gemini request requires a contents array")
	}
	changed := false
	if existing, ok := m["model"].(string); !ok || existing != model {
		m["model"] = model
		changed = true
	}
	if openGeminiSystem(m) {
		changed = true
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

// openGeminiSystem mirrors the plugin's geminiBody: the system instruction's
// role is dropped, its leading text parts are joined with "\n" under DroidLine,
// and non-text parts keep their order after the opening. An instruction that
// already opens with DroidLine is left untouched.
func openGeminiSystem(m object) bool {
	var parts []any
	changed := false
	switch si := m["systemInstruction"].(type) {
	case string:
		parts = []any{map[string]any{"text": si}}
		changed = true
	case object:
		if _, hasRole := si["role"]; hasRole {
			delete(si, "role")
			changed = true
		}
		if p, ok := si["parts"].([]any); ok {
			parts = p
		}
	}
	if len(parts) > 0 {
		if first, ok := parts[0].(object); ok {
			if t, ok := first["text"].(string); ok && strings.HasPrefix(t, DroidLine) {
				if changed {
					m["systemInstruction"] = map[string]any{"parts": parts}
				}
				return changed
			}
		}
	}
	texts := make([]string, 0, len(parts)+1)
	rest := make([]any, 0, len(parts))
	for _, raw := range parts {
		p, ok := raw.(object)
		if ok && len(p) == 1 {
			if t, ok := p["text"].(string); ok {
				texts = append(texts, t)
				continue
			}
		}
		rest = append(rest, raw)
	}
	joined := strings.Join(texts, "\n")
	opening := DroidLine
	if strings.TrimSpace(joined) != "" {
		opening = DroidLine + "\n" + joined
	}
	parts = append([]any{map[string]any{"text": opening}}, rest...)
	m["systemInstruction"] = map[string]any{"parts": parts}
	return true
}
