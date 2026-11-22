package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/logstore"
)

// ampGeminiBody is shaped like an Amp conversation on the Gemini route:
// ~1 MB of user/model/tool turns and a long systemInstruction.
func ampGeminiBody() []byte {
	var contents []any
	for i := 0; i < 160; i++ {
		contents = append(contents,
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": strings.Repeat("please check the repository files and run tests. ", 40)}}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": "Bash", "args": map[string]any{"cmd": "go test ./..."}}}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": "Bash", "response": map[string]any{"output": strings.Repeat("ok  \taccount-pool-balancer/internal/server\t1.2s\n", 80)}}}}},
		)
	}
	b, _ := json.Marshal(map[string]any{
		"contents":          contents,
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "You are a powerful AI coding agent. " + strings.Repeat("Follow the user's instructions carefully and use the tools. ", 600)}}},
		"tools":             []any{map[string]any{"functionDeclarations": []any{map[string]any{"name": "Bash", "description": "run", "parameters": map[string]any{"type": "object", "properties": map[string]any{"cmd": map[string]any{"type": "string"}}}}}}},
		"generationConfig":  map[string]any{"temperature": 1},
	})
	return b
}

func benchFactoryPrepare(b *testing.B, trusted bool) {
	cfg := config.Default()
	cfg.Factory.SystemMode = config.SystemMagpie
	body := ampGeminiBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := prepareFactoryFor(cfg, "/v1beta/models/gemini-4-argon:streamGenerateContent", body, trusted); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFactoryPrepareGeminiDefault(b *testing.B) { benchFactoryPrepare(b, false) }
func BenchmarkFactoryPrepareGeminiAmp(b *testing.B)     { benchFactoryPrepare(b, true) }

// BenchmarkFactoryDetailBuild is the work finish used to do inline per
// request with Factory detail logging on; it now runs in the detail workers.
func BenchmarkFactoryDetailBuild(b *testing.B) {
	cfg := config.Default()
	for i := 0; i < 600; i++ {
		cfg.Accounts = append(cfg.Accounts, config.Account{ID: "a", RuntimeKey: "fk-" + strings.Repeat("k", 30) + string(rune('a'+i%26)) + string(rune('a'+i/26))})
	}
	body := ampGeminiBody()
	resp := []byte(strings.Repeat(`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`+"\n\n", 200))
	h := http.Header{"User-Agent": {"google-genai-sdk/2.23.0"}, "X-Amp-Thread-Id": {"T-1"}, "X-Goog-Api-Key": {"secret"}}
	s := &Server{logs: nil}
	s.logs = openBenchLogs(b)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		d := &detailLog{TS: time.Now(), Provider: config.ProviderFactory}
		s.buildDetail(detailInput{cfg: cfg, d: d, max: 64 << 10, reqHeader: h, respHeader: http.Header{}, reqBody: body, respBody: resp})
	}
}

func openBenchLogs(b *testing.B) *logstore.Store {
	st, err := logstore.Open(b.TempDir(), func() logstore.Limits {
		return logstore.Limits{RetentionDays: 7, MaxTotalBytes: 1 << 30, MaxRecords: map[string]int{"details": 500}}
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(st.Close)
	return st
}
