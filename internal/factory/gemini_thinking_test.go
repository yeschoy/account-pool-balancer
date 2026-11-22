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

func TestApplyGeminiIncludeThoughts(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"hi"}]}]}`)
	out, changed := ApplyGeminiIncludeThoughts(body)
	if !changed {
		t.Fatal("includeThoughts not applied")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	tc := m["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if tc["includeThoughts"] != true {
		t.Fatalf("thinkingConfig = %v", tc)
	}
	// An explicit caller choice wins, including false.
	if _, ch := ApplyGeminiIncludeThoughts([]byte(`{"generationConfig":{"thinkingConfig":{"includeThoughts":false}}}`)); ch {
		t.Fatal("overwrote an explicit includeThoughts=false")
	}
	// A caller that only chose a level keeps it while thoughts are added.
	levelOnly := []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"low"}}}`)
	got, ch := ApplyGeminiIncludeThoughts(levelOnly)
	if !ch {
		t.Fatal("level-only request got no includeThoughts")
	}
	var lm map[string]any
	if err := json.Unmarshal(got, &lm); err != nil {
		t.Fatal(err)
	}
	ltc := lm["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
	if ltc["thinkingLevel"] != "low" || ltc["includeThoughts"] != true {
		t.Fatalf("level lost: %v", ltc)
	}
	// A level already present keeps it while adding thoughts.
	both := []byte(`{"generationConfig":{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"low"}}}`)
	if _, ch := ApplyGeminiIncludeThoughts(both); ch {
		t.Fatal("reapplied includeThoughts")
	}
}
