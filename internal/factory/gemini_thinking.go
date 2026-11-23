package factory

// ApplyGeminiThinkingLevel writes generationConfig.thinkingConfig.thinkingLevel
// when the request carries no thinking configuration of its own. A caller that
// already set thinkingConfig or thinkingBudget keeps its choice.
func ApplyGeminiThinkingLevel(body []byte, level string) ([]byte, bool) {
	return applyGeminiGenerationConfig(body, false, level)
}

func applyGeminiGenerationConfig(body []byte, includeThoughts bool, level string) ([]byte, bool) {
	if !includeThoughts && level == "" {
		return body, false
	}
	m, err := decodeTop(body)
	if err != nil || !updateGeminiGenerationConfig(m, includeThoughts, level) {
		return body, false
	}
	out, err := m.marshal()
	if err != nil {
		return body, false
	}
	return out, true
}

func updateGeminiGenerationConfig(m topObject, includeThoughts bool, level string) bool {
	if !includeThoughts && level == "" {
		return false
	}
	raw, _, err := m.field("generationConfig")
	if err != nil {
		return false
	}
	gen := obj(raw)
	if gen == nil {
		gen = object{}
	}
	changed := false
	// Preserve the old order: if includeThoughts creates thinkingConfig,
	// the level pass sees an existing config and does not insert a level.
	if includeThoughts {
		tc := obj(gen["thinkingConfig"])
		if _, present := tc["includeThoughts"]; !present {
			if tc == nil {
				tc = object{}
			}
			tc["includeThoughts"] = true
			gen["thinkingConfig"] = tc
			changed = true
		}
	}
	if level != "" && gen["thinkingConfig"] == nil && gen["thinkingBudget"] == nil {
		gen["thinkingConfig"] = object{"thinkingLevel": level}
		changed = true
	}
	return changed && m.set("generationConfig", gen) == nil
}
