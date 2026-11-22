package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTavilyDomainFilterContextAndUsage(t *testing.T) {
	var outside bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&q)
		if string(q["include_usage"]) != "true" || string(q["max_results"]) != "3" || string(q["include_domains"]) != `["example.com"]` {
			t.Errorf("unsupported search options: %s", q)
		}
		u := "https://docs.example.com/help"
		if outside {
			u = "https://example.com.evil.test/help"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []Source{{Title: "Docs", URL: u, Content: strings.Repeat("中", 2000)}}, "usage": map[string]int{"credits": 1}})
	}))
	defer srv.Close()
	backend := Tavily{Endpoint: srv.URL, APIKey: "fake-search-credential", MaxResults: 5}
	q := Request{Query: "public", AllowedDomains: []string{"Example.COM"}, ContextSize: "low"}
	result, e := backend.Search(context.Background(), q)
	if e != nil || len(result.Sources) != 1 || len(result.Sources[0].Content) != 3999 || result.Credits == nil {
		t.Fatalf("%+v %v", result, e)
	}
	outside = true
	_, e = backend.Search(context.Background(), q)
	if e == nil || e.Error() != "search_domain_violation" {
		t.Fatalf("domain restriction bypassed: %v", e)
	}
}
func TestDomainValidation(t *testing.T) {
	for _, d := range []string{"https://example.com", "example.com/a", "*.example.com", "127.0.0.1", " example.com", "-bad.example", "localhost", "example.com.", "user@example.com", "例子.com"} {
		if _, e := NormalizeDomains([]string{d}); e == nil {
			t.Fatalf("bad domain %s", d)
		}
	}
	domains, e := NormalizeDomains([]string{"EXAMPLE.COM", "example.com"})
	if e != nil || len(domains) != 1 || !DomainAllowed("sub.example.com", domains) || DomainAllowed("evil-example.com", domains) {
		t.Fatal("domain normalization/matching")
	}
}
