package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Google clients list models before generating. The list must only advertise
// calls this pool serves (generate/stream), never Veo, embeddings or batch.
func TestFactoryGeminiModelList(t *testing.T) {
	e := newFactoryEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	req.Header.Set("Authorization", "Bearer downstream-ms")
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("X-Catalog-Source") != "configured-reference-not-entitlement" {
		t.Fatal("missing reference caveat")
	}
	var doc struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if json.Unmarshal(w.Body.Bytes(), &doc) != nil || len(doc.Models) == 0 {
		t.Fatal(w.Body.String())
	}
	seen := map[string]bool{}
	for _, m := range doc.Models {
		if !strings.HasPrefix(m.Name, "models/gemini-") {
			t.Fatalf("non-gemini model advertised: %s", m.Name)
		}
		seen[m.Name] = true
		for _, method := range m.SupportedGenerationMethods {
			if method != "generateContent" && method != "streamGenerateContent" {
				t.Fatalf("%s advertises %s, which this pool does not serve", m.Name, method)
			}
		}
	}
	if !seen["models/gemini-4-carbon"] {
		t.Fatalf("configured generate route missing: %v", seen)
	}
	// Chat models are not Gemini routes and must not appear here.
	if seen["models/glm-5.3"] || seen["models/text-embedding-3-large"] {
		t.Fatal("non-generate model advertised on the Gemini surface")
	}
}

func TestFactoryGeminiModelListRequiresFactoryKey(t *testing.T) {
	e := newFactoryEnv(t)
	req := httptest.NewRequest(http.MethodGet, "/v1beta/models", nil)
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list status=%d", w.Code)
	}
}
