package factory

import (
	"encoding/json"
	"testing"
)

// magpie's geminiBody deletes a body-level stream flag: the streaming variant
// comes from the method name, and droid sends no such field.
func TestDropGeminiStreamField(t *testing.T) {
	body := []byte(`{"stream":true,"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	out := DropGeminiStreamField(body)
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		t.Fatal("not JSON")
	}
	if _, ok := m["stream"]; ok {
		t.Fatalf("stream kept: %s", out)
	}
	// A body without the field is returned byte for byte.
	clean := []byte(`{"contents":[]}`)
	if got := DropGeminiStreamField(clean); string(got) != string(clean) {
		t.Fatalf("clean body changed: %s", got)
	}
}

// OpenAI reasoning models refuse temperature and top_p with 400; magpie drops
// them on the Responses route.
func TestStripOpenAISampling(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","temperature":0.7,"top_p":0.9,"input":[]}`)
	out, changed := StripOpenAISampling(body)
	if !changed {
		t.Fatal("nothing stripped")
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		t.Fatal("not JSON")
	}
	if _, ok := m["temperature"]; ok {
		t.Fatalf("temperature kept: %s", out)
	}
	if _, ok := m["top_p"]; ok {
		t.Fatalf("top_p kept: %s", out)
	}
	clean := []byte(`{"model":"gpt-5.4","input":[]}`)
	if got, changed := StripOpenAISampling(clean); changed || string(got) != string(clean) {
		t.Fatalf("clean body changed: %s", got)
	}
}
