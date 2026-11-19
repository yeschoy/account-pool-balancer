package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// aggregateGeminiSSE turns a Google streamGenerateContent SSE stream into the
// single GenerateContentResponse a non-streaming caller expects. Factory's one
// /generate endpoint is always relayed as SSE by droid, but a NewAPI Gemini
// channel may ask for :generateContent; merging deltas here keeps that path
// usable without inventing a second upstream route.
func aggregateGeminiSSE(r io.Reader, max int64) ([]byte, error) {
	rd := bufio.NewReaderSize(r, 64<<10)
	out := map[string]any{}
	var candidates []any
	var total int64
	for {
		ev, err := readEvent(rd)
		if len(ev) > 0 {
			total += int64(len(ev))
			if total > max {
				return nil, errEventTooLarge
			}
			_, data, _ := sseFields(ev)
			if len(data) > 0 && !bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
				var m map[string]any
				dec := json.NewDecoder(bytes.NewReader(data))
				dec.UseNumber()
				if dec.Decode(&m) == nil && m != nil {
					if _, isErr := m["error"]; isErr {
						return json.Marshal(m)
					}
					for k, v := range m {
						switch k {
						case "candidates":
							candidates = mergeGeminiCandidates(candidates, v)
						case "usageMetadata", "promptFeedback":
							out[k] = v
						default:
							if _, exists := out[k]; !exists {
								out[k] = v
							}
						}
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
	}
	if len(candidates) == 0 {
		if len(out) == 0 {
			return nil, errors.New("Gemini stream ended without candidates")
		}
		return json.Marshal(out) // a blocked prompt may carry only promptFeedback
	}
	out["candidates"] = candidates
	return json.Marshal(out)
}

func mergeGeminiCandidates(dst []any, v any) []any {
	src, _ := v.([]any)
	for i, raw := range src {
		sm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		for len(dst) <= i {
			dst = append(dst, map[string]any{})
		}
		dm, ok := dst[i].(map[string]any)
		if !ok {
			dm = map[string]any{}
			dst[i] = dm
		}
		for k, val := range sm {
			if k == "content" {
				mergeGeminiContent(dm, val)
				continue
			}
			dm[k] = val
		}
	}
	return dst
}

func mergeGeminiContent(dm map[string]any, v any) {
	sm, ok := v.(map[string]any)
	if !ok {
		return
	}
	cm, _ := dm["content"].(map[string]any)
	if cm == nil {
		cm = map[string]any{}
		dm["content"] = cm
	}
	if role, _ := sm["role"].(string); role != "" {
		cm["role"] = role
	}
	parts, _ := cm["parts"].([]any)
	if next, ok := sm["parts"].([]any); ok {
		cm["parts"] = mergeGeminiParts(parts, next)
	}
}

func mergeGeminiParts(dst []any, src []any) []any {
	for _, raw := range src {
		pm, ok := raw.(map[string]any)
		if !ok {
			dst = append(dst, raw)
			continue
		}
		txt, hasText := pm["text"].(string)
		if !hasText {
			dst = append(dst, pm)
			continue
		}
		thought, _ := pm["thought"].(bool)
		// Only a plain text part is a delta. A part carrying per-chunk
		// metadata (thoughtSignature and the like) stays separate.
		simple := len(pm) == 1 || len(pm) == 2 && pm["thought"] != nil
		if simple {
			merged := false
			for j := len(dst) - 1; j >= 0; j-- {
				last, ok := dst[j].(map[string]any)
				if !ok {
					continue
				}
				lt, okText := last["text"].(string)
				if !okText || len(last) > 2 {
					continue
				}
				lthought, _ := last["thought"].(bool)
				if lthought != thought {
					continue
				}
				last["text"] = lt + txt
				merged = true
				break
			}
			if merged {
				continue
			}
		}
		dst = append(dst, pm)
	}
	return dst
}
