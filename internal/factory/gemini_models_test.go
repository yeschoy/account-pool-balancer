package factory

import "testing"

func TestGeminiModelListAdvertisesOnlyGenerate(t *testing.T) {
	got := GeminiModelList([]string{"gemini-4-carbon", "", "gemini-3.8-flash"})
	if len(got) != 2 {
		t.Fatalf("empty id kept: %+v", got)
	}
	if got[0].Name != "models/gemini-4-carbon" || got[0].DisplayName != "gemini-4-carbon" {
		t.Fatalf("naming: %+v", got[0])
	}
	// The pool has no Veo/video/embedding surface on this route; advertising
	// them would promise a call Factory does not serve.
	for _, m := range got {
		if len(m.SupportedGenerationMethods) != 2 || m.SupportedGenerationMethods[0] != "generateContent" {
			t.Fatalf("methods: %+v", m.SupportedGenerationMethods)
		}
	}
}
