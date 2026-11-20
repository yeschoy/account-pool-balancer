package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

// Benchmarks for the per-request adaptation path. A request from an agent
// client carries a large body (hundreds of KB of tool declarations), so every
// pass over it matters.

func realisticToolBody(withHarness bool) []byte {
	decls := make([]any, 0, 265)
	for i := 0; i < 265; i++ {
		decls = append(decls, map[string]any{
			"name":        "mcp__rea__tool" + string(rune('a'+i%26)) + "abcd",
			"description": strings.Repeat("Analyze the local artifact and report evidence. ", 16),
			"parameters": map[string]any{
				"$schema": "http://json-schema.org/draft-07/schema#", "type": "object",
				"additionalProperties": false,
				"properties":           map[string]any{"path": map[string]any{"type": []any{"string", "null"}}},
			},
		})
	}
	system := "You are Droid, an AI software engineer built by Factory."
	if withHarness {
		system += "\nNote: You are an AI agent powered by DeepSeek Harness."
	}
	body, _ := json.Marshal(map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": system}}},
		"contents":          []any{map[string]any{"role": "user", "parts": []any{map[string]any{"text": "hi"}}}},
		"tools":             []any{map[string]any{"functionDeclarations": decls}},
	})
	return body
}

func BenchmarkContainsPhrase(b *testing.B) {
	body := realisticToolBody(false)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		containsPhrase(body)
	}
}

func BenchmarkEscapeClientPhrasesNoMatch(b *testing.B) {
	body := realisticToolBody(false)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EscapeClientPhrases(body)
	}
}

func BenchmarkSelfDescriptionScan(b *testing.B) {
	body := realisticToolBody(false)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		selfDescriptionSpans(string(body))
	}
}

func benchmarkPipeline(b *testing.B, withHarness bool) {
	body := realisticToolBody(withHarness)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		aliased, _ := AliasToolNamesRenamingAll(body)
		out, err := OpenGeminiBodyWith("gemini-4-carbon", MagpieAdaptGemini(aliased), true)
		if err != nil {
			b.Fatal(err)
		}
		if trimmed, changed := SanitizeGeminiTools(out); changed {
			out = trimmed
		}
		EscapeClientPhrases(out)
	}
}

func BenchmarkPipelineClean(b *testing.B)   { benchmarkPipeline(b, false) }
func BenchmarkPipelineHarness(b *testing.B) { benchmarkPipeline(b, true) }
