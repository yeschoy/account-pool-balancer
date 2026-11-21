package factory

// ApplyGeminiThinkingLevel writes generationConfig.thinkingConfig.thinkingLevel
// when the request carries no thinking configuration of its own. A caller that
// already set thinkingConfig or thinkingBudget keeps its choice.
func ApplyGeminiThinkingLevel(body []byte, level string) ([]byte, bool) {
	if level == "" {
		return body, false
	}
	var m object
	if decode(body, &m) != nil || m == nil {
		return body, false
	}
	gen := obj(m["generationConfig"])
	if gen == nil {
		gen = object{}
	}
	if gen["thinkingConfig"] != nil || gen["thinkingBudget"] != nil {
		return body, false
	}
	gen["thinkingConfig"] = object{"thinkingLevel": level}
	m["generationConfig"] = gen
	out, err := marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}
