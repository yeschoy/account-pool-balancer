package factory

// ApplyGeminiThinkingLevel writes generationConfig.thinkingConfig.thinkingLevel
// when the request carries no thinking configuration of its own. A caller that
// already set thinkingConfig or thinkingBudget keeps its choice.
func ApplyGeminiThinkingLevel(body []byte, level string) ([]byte, bool) {
	if level == "" {
		return body, false
	}
	m, err := decodeTop(body)
	if err != nil {
		return body, false
	}
	raw, _, err := m.field("generationConfig")
	if err != nil {
		return body, false
	}
	gen := obj(raw)
	if gen == nil {
		gen = object{}
	}
	if gen["thinkingConfig"] != nil || gen["thinkingBudget"] != nil {
		return body, false
	}
	gen["thinkingConfig"] = object{"thinkingLevel": level}
	if m.set("generationConfig", gen) != nil {
		return body, false
	}
	out, err := m.marshal()
	if err != nil {
		return body, false
	}
	return out, true
}
