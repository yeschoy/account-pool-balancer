package server

import "testing"

// Gemini reports usage in a top-level usageMetadata and carries no "usage"
// object; the scanner used to return early and drop it, so Factory requests
// logged no input/output tokens.
func TestScanUsageGeminiMetadata(t *testing.T) {
	var u usage
	scanUsage(map[string]any{"usageMetadata": map[string]any{
		"promptTokenCount":        float64(120),
		"candidatesTokenCount":    float64(34),
		"cachedContentTokenCount": float64(100),
		"thoughtsTokenCount":      float64(7),
	}}, &u)
	if u.in == nil || *u.in != 120 {
		t.Fatalf("input = %v", u.in)
	}
	if u.out == nil || *u.out != 34 {
		t.Fatalf("output = %v", u.out)
	}
	if u.cached == nil || *u.cached != 100 {
		t.Fatalf("cached = %v", u.cached)
	}
	if u.reasoning == nil || *u.reasoning != 7 {
		t.Fatalf("reasoning = %v", u.reasoning)
	}
	// OpenAI shape still works and is not affected.
	var u2 usage
	scanUsage(map[string]any{"usage": map[string]any{"prompt_tokens": float64(5), "completion_tokens": float64(6)}}, &u2)
	if u2.in == nil || *u2.in != 5 || u2.out == nil || *u2.out != 6 {
		t.Fatalf("openai usage = %+v", u2)
	}
}

// A Gemini cache miss carries no cachedContentTokenCount; it must still be
// measured as zero so the hit rate is not computed over hits alone.
func TestScanUsageGeminiCacheMissCountsAsZero(t *testing.T) {
	var u usage
	scanUsage(map[string]any{"usageMetadata": map[string]any{
		"promptTokenCount":     float64(8449),
		"candidatesTokenCount": float64(5),
	}}, &u)
	if u.cached == nil || *u.cached != 0 {
		t.Fatalf("cached = %v, want measured zero", u.cached)
	}
	// Usage without promptTokenCount stays unmeasured rather than claiming zero.
	var u2 usage
	scanUsage(map[string]any{"usageMetadata": map[string]any{"trafficType": "ON_DEMAND"}}, &u2)
	if u2.cached != nil {
		t.Fatalf("cached = %v, want nil", u2.cached)
	}
}
