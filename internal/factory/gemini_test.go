package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseGeminiPath(t *testing.T) {
	for _, c := range []struct {
		path   string
		model  string
		stream bool
		ok     bool
	}{
		{"/v1beta/models/gemini-4-carbon:generateContent", "gemini-4-carbon", false, true},
		{"/v1beta/models/gemini-3.8-flash:streamGenerateContent", "gemini-3.8-flash", true, true},
		{"/v1/models/gemini-4-argon:generateContent", "gemini-4-argon", false, true},
		{"/v1beta/models/gemini-4-carbon:countTokens", "", false, false},
		{"/v1/chat/completions", "", false, false},
		{"/v1beta/models/:generateContent", "", false, false},
	} {
		model, stream, ok := ParseGeminiPath(c.path)
		if model != c.model || stream != c.stream || ok != c.ok {
			t.Fatalf("%s => %q %v %v, want %q %v %v", c.path, model, stream, ok, c.model, c.stream, c.ok)
		}
	}
}

func TestOpenGeminiBody(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"Read"}]}],"generationConfig":{"maxOutputTokens":65536,"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"LOW"}},"seed":9007199254740993}`
	out, err := OpenGeminiBody("gemini-4-carbon", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Model             string         `json:"model"`
		SystemInstruction map[string]any `json:"systemInstruction"`
		GenerationConfig  map[string]any `json:"generationConfig"`
		Seed              json.Number    `json:"seed"`
	}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	if dec.Decode(&doc) != nil || doc.Model != "gemini-4-carbon" || doc.Seed.String() != "9007199254740993" {
		t.Fatalf("model/numbers lost: %s", out)
	}
	parts, _ := doc.SystemInstruction["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != DroidLine {
		t.Fatalf("system instruction not opened: %s", out)
	}
	if doc.GenerationConfig["thinkingConfig"].(map[string]any)["thinkingLevel"] != "LOW" {
		t.Fatal("generationConfig changed")
	}
}

func TestOpenGeminiBodySystemPartsAndIdempotence(t *testing.T) {
	// Text parts are joined under the line; non-text parts keep their order.
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"role":"system","parts":[{"text":"Be brief."},{"text":"Use tools."},{"inlineData":{"mimeType":"image/png","data":"aW1n"}}]}}`
	out, err := OpenGeminiBody("gemini-4-carbon", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		SystemInstruction struct {
			Parts []map[string]any `json:"parts"`
		} `json:"systemInstruction"`
	}
	if json.Unmarshal(out, &doc) != nil || len(doc.SystemInstruction.Parts) != 2 {
		t.Fatalf("parts not preserved: %s", out)
	}
	if doc.SystemInstruction.Parts[0]["text"] != DroidLine+"\nBe brief.\nUse tools." || doc.SystemInstruction.Parts[1]["inlineData"] == nil {
		t.Fatalf("wrong opening/order: %s", out)
	}
	// An instruction already opened by droid is left byte-for-byte apart from
	// the model field.
	own := `{"model":"gemini-4-carbon","contents":[],"systemInstruction":{"parts":[{"text":"` + DroidLine + `\nmine"}]}}`
	if got, err := OpenGeminiBody("gemini-4-carbon", []byte(own)); err != nil || string(got) != own {
		t.Fatalf("opened body rewritten: %v %s", err, got)
	}
}

func TestOpenGeminiBodyRejectsNonContents(t *testing.T) {
	if _, err := OpenGeminiBody("gemini-4-carbon", []byte(`{"messages":[]}`)); err == nil {
		t.Fatal("body without contents accepted")
	}
	if _, err := OpenGeminiBody("gemini-4-carbon", []byte(`not json`)); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}
