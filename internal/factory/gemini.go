package factory

import (
	"encoding/json"
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
	m, err := openGeminiTop(body)
	if err != nil {
		return nil, err
	}
	changed, err := placeGeminiModel(m, model)
	if err != nil {
		return nil, err
	}
	opened, err := updateGeminiSystem(m, false, true)
	if err != nil {
		return nil, err
	}
	if !changed && !opened {
		return body, nil
	}
	return m.marshal()
}

// GeminiGenerateOptions selects the existing wire adapters for a generate
// request. Keep mode leaves all three system switches off.
type GeminiGenerateOptions struct {
	FoldSystem, AdaptSystem, OpenSystem bool
	IncludeThoughts, SanitizeTools      bool
	ThinkingLevel                       string
}

// PrepareGeminiGenerateBody applies the top-level adapters to one validated
// object and renders it at most once on the common (non-fold) path. The
// adapters decode only the fields they inspect; untouched values keep their bytes.
// Aliasing (when enabled) runs before this step and may already trim tools.
func PrepareGeminiGenerateBody(model string, body []byte, opts GeminiGenerateOptions) ([]byte, error) {
	if opts.FoldSystem {
		// Folding moves text into the history and retains its existing full-body
		// renderer. The remaining top-level operations can still share a pass.
		var err error
		body, err = FoldGeminiSystem(model, DropGeminiStreamField(body))
		if err != nil || (!opts.IncludeThoughts && opts.ThinkingLevel == "" && !opts.SanitizeTools) {
			return body, err
		}
	}
	m, err := openGeminiTop(body)
	if err != nil {
		return nil, err
	}
	_, changed := m["stream"]
	delete(m, "stream")
	if !opts.FoldSystem {
		placed, err := placeGeminiModel(m, model)
		if err != nil {
			return nil, err
		}
		updated, err := updateGeminiSystem(m, opts.AdaptSystem, opts.OpenSystem)
		if err != nil {
			return nil, err
		}
		changed = changed || placed || updated
	}
	changed = updateGeminiGenerationConfig(m, opts.IncludeThoughts, opts.ThinkingLevel) || changed
	if opts.SanitizeTools {
		changed = sanitizeGeminiToolsTop(m) || changed
	}
	if !changed {
		return body, nil
	}
	return m.marshal()
}

// updateGeminiSystem shares the decoded system instruction between prompt
// adaptation and the opening. There is no work in keep mode.
func updateGeminiSystem(m topObject, adapt, open bool) (bool, error) {
	if !adapt && !open {
		return false, nil
	}
	si, present, err := m.field("systemInstruction")
	if err != nil {
		return false, errNotObject
	}
	holder := object{}
	if present {
		holder["systemInstruction"] = si
	}
	changed := adapt && adaptGeminiSystem(holder)
	if open {
		changed = openGeminiSystem(holder) || changed
	}
	if !changed {
		return false, nil
	}
	return true, m.set("systemInstruction", holder["systemInstruction"])
}

// openGeminiTop decodes a generateContent body's top level and requires the
// contents array.
func openGeminiTop(body []byte) (topObject, error) {
	m, err := decodeTop(body)
	if err != nil {
		return nil, errors.New("Factory Gemini request must be a JSON object")
	}
	if !m.isArray("contents") {
		return nil, errors.New("Factory Gemini request requires a contents array")
	}
	return m, nil
}

// placeGeminiModel writes the path's model into the body unless it is there.
func placeGeminiModel(m topObject, model string) (bool, error) {
	var existing string
	if raw, ok := m["model"]; ok && json.Unmarshal(raw, &existing) == nil && existing == model {
		return false, nil
	}
	return true, m.set("model", model)
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

// OpenGeminiBodyWith is OpenGeminiBody with the droid system-prompt opening
// made optional. With inject false the model is still placed in the body (the
// captured route needs it) but no systemInstruction is created or rewritten.
func OpenGeminiBodyWith(model string, body []byte, inject bool) ([]byte, error) {
	if inject {
		return OpenGeminiBody(model, body)
	}
	m, err := openGeminiTop(body)
	if err != nil {
		return nil, err
	}
	changed, err := placeGeminiModel(m, model)
	if err != nil || !changed {
		return body, err
	}
	return m.marshal()
}

// DropGeminiStreamField removes a body-level stream flag. Google's route takes
// the streaming variant from the method name (:streamGenerateContent), and
// droid sends no stream field; a client that sets one would otherwise reach the
// captured /generate route with a field Factory does not expect.
func DropGeminiStreamField(body []byte) []byte {
	m, err := decodeTop(body)
	if err != nil {
		return body
	}
	if _, ok := m["stream"]; !ok {
		return body
	}
	delete(m, "stream")
	out, err := m.marshal()
	if err != nil {
		return body
	}
	return out
}
