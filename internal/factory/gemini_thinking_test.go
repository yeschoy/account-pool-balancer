package factory

import (
	"encoding/json"
	"testing"
)

func TestApplyGeminiThinkingLevel(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	out, changed := ApplyGeminiThinkingLevel(body, "minimal")
	if !changed {
		t.Fatal("level not applied")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	gen := m["generationConfig"].(map[string]any)
	tc := gen["thinkingConfig"].(map[string]any)
	if tc["thinkingLevel"] != "minimal" {
		t.Fatalf("thinkingConfig = %v", tc)
	}
	// A caller's own choice wins.
	own := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`)
	if got, changed := ApplyGeminiThinkingLevel(own, "minimal"); changed || string(got) != string(own) {
		t.Fatalf("client choice overwritten: %s", got)
	}
	budget := []byte(`{"generationConfig":{"thinkingBudget":0}}`)
	if _, changed := ApplyGeminiThinkingLevel(budget, "minimal"); changed {
		t.Fatal("thinkingBudget overridden")
	}
	// Empty level is a no-op.
	if _, changed := ApplyGeminiThinkingLevel(body, ""); changed {
		t.Fatal("empty level applied")
	}
}
