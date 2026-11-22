package factory

// ApplyGeminiIncludeThoughts asks the gateway to stream the model's thinking
// content back as "thought" parts before the visible answer. Measured
// 2026-11-22: the first thought frame arrives about 2 s in, while without it
// the first visible text only arrives when thinking finishes (27 s for a long
// answer), so a client that renders thoughts sees progress immediately.
func ApplyGeminiIncludeThoughts(body []byte) ([]byte, bool) {
	var m object
	if decode(body, &m) != nil || m == nil {
		return body, false
	}
	gen := obj(m["generationConfig"])
	if gen == nil {
		gen = object{}
	}
	tc := obj(gen["thinkingConfig"])
	if tc == nil {
		tc = object{}
	}
	if v, ok := tc["includeThoughts"].(bool); ok && v {
		return body, false
	}
	if _, present := tc["includeThoughts"]; present {
		return body, false // the caller already chose, keep its value
	}
	tc["includeThoughts"] = true
	gen["thinkingConfig"] = tc
	m["generationConfig"] = gen
	out, err := marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}
