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

func TestPrepareGeminiGenerateBodyPreservesRawFields(t *testing.T) {
	const history = `[ {"parts": [ {"functionCall":{"args":{"id":9007199254740993}},"thoughtSignature":"\\u003copaque"} ]} ]`
	const opaque = ` { "tag": "<literal>", "opaque": [ 1, 2 ] }`
	input := []byte(`{"contents":` + history + `,"extra":` + opaque + `,"stream":true,"model":"old","m\u006fdel":"also-old"}`)
	out, err := PrepareGeminiGenerateBody("gemini-4-carbon", input, GeminiGenerateOptions{OpenSystem: true, SanitizeTools: true})
	if err != nil || !json.Valid(out) {
		t.Fatalf("unexpected result %s: %v", out, err)
	}
	for _, raw := range []string{history, strings.TrimSpace(opaque), `"model":"gemini-4-carbon"`} {
		if !strings.Contains(string(out), raw) {
			t.Fatalf("raw value %q lost or changed: %s", raw, out)
		}
	}
	if strings.Contains(string(out), `"stream"`) || strings.Contains(string(out), `"also-old"`) {
		t.Fatalf("stream or old model still present: %s", out)
	}

	unchanged := ` { "model": "gemini-4-carbon", "contents": [] } `
	if got, err := PrepareGeminiGenerateBody("gemini-4-carbon", []byte(unchanged), GeminiGenerateOptions{}); err != nil || string(got) != unchanged {
		t.Fatalf("unmodified body changed to %s: %v", got, err)
	}
	for _, malformed := range []string{`{"contents":null}`, `{"contents":[{"parts":[1,]}]}`, `{"contents":[]} {}`, `{"contents":[],"extra":{"x":tru}}`} {
		if _, err := PrepareGeminiGenerateBody("gemini-4-carbon", []byte(malformed), GeminiGenerateOptions{}); err == nil {
			t.Fatalf("accepted malformed request %s", malformed)
		}
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
