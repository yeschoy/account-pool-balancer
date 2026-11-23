package factory

// ApplyGeminiIncludeThoughts asks the gateway to stream the model's thinking
// content back as "thought" parts before the visible answer. Measured
// 2026-11-22: the first thought frame arrives about 2 s in, while without it
// the first visible text only arrives when thinking finishes (27 s for a long
// answer), so a client that renders thoughts sees progress immediately.
func ApplyGeminiIncludeThoughts(body []byte) ([]byte, bool) {
	return applyGeminiGenerationConfig(body, true, "")
}
