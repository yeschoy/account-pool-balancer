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

func TestScanFactoryUsagePerAccount(t *testing.T) {
	dir := t.TempDir()
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logs, 0o700); err != nil {
		t.Fatal(err)
	}
	rows := `{"ts":"2026-11-20T10:00:00Z","provider":"factory","accountId":"f1","accountName":"OldName","model":"gemini-4-carbon","status":200,"inputTokens":1000000,"outputTokens":100000,"cachedTokens":50000}
{"ts":"2026-11-20T10:01:00Z","provider":"factory","accountId":"f2","accountName":"Acct2","model":"gemini-4-hydrogen","status":200,"inputTokens":2000000,"outputTokens":200000}
{"ts":"2026-11-20T10:02:00Z","provider":"factory","accountId":"f2","accountName":"Acct2","model":"unknown-model","status":500,"inputTokens":10,"outputTokens":5}
{"ts":"2026-11-20T10:03:00Z","provider":"factory","accountId":"deleted-acct","accountName":"Legacy","model":"gemini-4-carbon","status":200,"inputTokens":500000,"outputTokens":50000}
{"ts":"2026-11-20T10:04:00Z","provider":"perch","accountId":"p1","accountName":"Perch1","model":"claude-sonnet-5-5","status":200,"inputTokens":999,"outputTokens":999}
`
	if err := os.WriteFile(filepath.Join(logs, "requests-20261120-100000-1.jsonl"), []byte(rows), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Accounts = []config.Account{
		{ID: "f1", Provider: config.ProviderFactory, Name: "Factory-1", Email: "f1@example.com", Enabled: true},
		{ID: "f2", Provider: config.ProviderFactory, Name: "Factory-2", Enabled: false},
		{ID: "f3", Provider: config.ProviderFactory, Name: "Factory-Idle", Enabled: true},
		{ID: "p1", Provider: config.ProviderPerch, Name: "Perch-1", Enabled: true},
	}
	s := &Server{dataDir: dir}
	got := s.scanFactoryUsage(cfg)
	if len(got.Accounts) != 4 {
		t.Fatalf("accounts len = %d, want 4: %+v", len(got.Accounts), got.Accounts)
	}
	// Sorted by CostUSD descending:
	// f1: (1M-0.05M)*2.00 + 0.05M*0.20 + 0.1M*12.00 = 1.90 + 0.01 + 1.20 = 3.11
	// f2: 2M*0.75 + 0.2M*3.75 = 2.25 (plus 1 unpriced)
	// deleted-acct: 0.5M*2.00 + 0.05M*12.00 = 1.60
	// f3: 0
	wantOrder := []string{"f1", "f2", "deleted-acct", "f3"}
	for i, id := range wantOrder {
		if got.Accounts[i].AccountID != id {
			t.Fatalf("order[%d] = %s, want %s (%+v)", i, got.Accounts[i].AccountID, id, got.Accounts)
		}
	}
	f1 := got.Accounts[0]
	if f1.AccountName != "Factory-1" || f1.Email != "f1@example.com" || !f1.Configured || !f1.Enabled {
		t.Fatalf("f1 metadata = %+v", f1)
	}
	if f1.Requests != 1 || f1.Success != 1 || f1.CachedTokens != 50000 || f1.CostUSD < 3.109 || f1.CostUSD > 3.111 {
		t.Fatalf("f1 usage = %+v", f1)
	}
	f2 := got.Accounts[1]
	if f2.Requests != 2 || f2.Success != 1 || f2.Unpriced != 1 || f2.Enabled || !f2.Configured {
		t.Fatalf("f2 usage = %+v", f2)
	}
	del := got.Accounts[2]
	if del.Configured || del.AccountName != "Legacy" || del.CostUSD < 1.599 || del.CostUSD > 1.601 {
		t.Fatalf("deleted account = %+v", del)
	}
	idle := got.Accounts[3]
	if !idle.Configured || idle.Requests != 0 || idle.CostUSD != 0 {
		t.Fatalf("idle account = %+v", idle)
	}
}
