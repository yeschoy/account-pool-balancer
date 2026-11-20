package server

import (
	"os"
	"path/filepath"
	"testing"

	"account-pool-balancer/internal/config"
)

// The estimate prices recorded Factory tokens with Google's rates; models
// without a rate are counted as unpriced rather than silently free.
func TestScanFactoryUsage(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	rows := `{"ts":"2026-11-20T10:00:00Z","provider":"factory","model":"gemini-4-carbon","inputTokens":1000000,"outputTokens":100000}
{"ts":"2026-11-20T10:01:00Z","provider":"factory","model":"gemini-4-hydrogen","inputTokens":2000000,"outputTokens":200000}
{"ts":"2026-11-20T10:02:00Z","provider":"factory","model":"unknown-model","inputTokens":10,"outputTokens":5}
{"ts":"2026-11-20T10:03:00Z","provider":"factory","model":"gemini-4-carbon"}
{"ts":"2026-11-20T10:04:00Z","provider":"perch","model":"claude-sonnet-5-5","inputTokens":999,"outputTokens":999}
`
	if err := os.WriteFile(filepath.Join(logs, "requests-20261120-100000-1.jsonl"), []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Server{dataDir: dir}
	cfg := config.Default()
	got := s.scanFactoryUsage(cfg)
	if got.Requests != 4 {
		t.Fatalf("requests = %d", got.Requests)
	}
	if got.InputTokens != 3000010 || got.OutputTokens != 300005 {
		t.Fatalf("tokens = %d / %d", got.InputTokens, got.OutputTokens)
	}
	// carbon: 1M * 2.00 + 0.1M * 12.00 = 3.20
	// hydrogen: 2M * 0.75 + 0.2M * 3.75 = 2.25
	want := 5.45
	if diff := got.CostUSD - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost = %v, want %v", got.CostUSD, want)
	}
	if got.Unpriced != 2 { // unknown model and the request without usage
		t.Fatalf("unpriced = %d", got.Unpriced)
	}
	if len(got.Models) != 3 || got.Models[0].Model != "gemini-4-carbon" {
		t.Fatalf("models = %+v", got.Models)
	}
	if got.Models[0].CostUSD < 3.199 || got.Models[0].CostUSD > 3.201 {
		t.Fatalf("carbon cost = %v", got.Models[0].CostUSD)
	}
}
