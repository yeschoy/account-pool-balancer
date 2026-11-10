package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestMistralCacheAuditHTTPHeadersAndFullUsage(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			e := newMistralEnv(t)
			enableCacheAuditForTest(t, e)
			// Both diagnostic systems must compose their WroteHeaderField hooks.
			if !stream {
				enableMistralWireDetails(t, e)
			}
			usage := `{"prompt_tokens":10,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":0,"future_counter":9007199254740993},"service_tier":"standard","new_flag":true}`
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Mistral-Correlation-Id", "audit-native-trace")
				w.Header().Set("X-Region", "eu-west")
				w.Header().Set("X-Affinity", r.Header.Get("X-Affinity"))
				w.Header().Add("Set-Cookie", "route=cookie-private-value; Secure; HttpOnly")
				w.Header().Set("X-Unknown-Router", "unknown-private-node")
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[],\"usage\":%s}\n\ndata: [DONE]\n\n", usage)
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"choices":[{"message":{"content":"ok"}}],"usage":%s}`, usage)
				}
			})
			body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"unlogged-private-prompt"}],"prompt_cache_key":"private-cache","stream":%v}`, stream)
			status, reply := e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "private-session", "X-Affinity": "private-cache", "X-Unrecognized": "private-header"})
			if status != 200 || !strings.Contains(reply, "9007199254740993") {
				t.Fatal("diagnostic changed reply/numeric precision", status)
			}
			r := cacheAuditRecordsForTest(t, e)[0]
			a := r.CacheAudit.Attempts[0]
			if !a.RequestHeadersObserved || a.RequestHTTP == nil || a.ResponseHTTP == nil || a.UpstreamProtocol != "HTTP/1.1" || a.UsageSanitized || a.UsageTruncated {
				t.Fatalf("header/usage capture unavailable: %+v", a)
			}
			if a.RequestHTTP.Headers["Authorization"].Values[0] != "[REDACTED]" || len(a.RequestHTTP.Headers["X-Affinity"].Tags) != 1 || a.ResponseHTTP.Headers["Set-Cookie"].Values[0] != "[REDACTED]" || a.ResponseHTTP.Headers["X-Region"].Values[0] != "eu-west" || len(a.ResponseHTTP.Headers["X-Unknown-Router"].Tags) != 1 {
				t.Fatal("header projection missing/private value readable")
			}
			var got, want any
			json.Unmarshal(a.Usage, &got)
			json.Unmarshal([]byte(usage), &want)
			if string(a.Usage) == "" || !strings.Contains(string(a.Usage), "9007199254740993") || fmt.Sprint(got) != fmt.Sprint(want) {
				t.Fatal("usage unknown fields/precision lost", string(a.Usage))
			}
			encoded, _ := json.Marshal(r.CacheAudit)
			for _, secret := range []string{"cookie-private-value", "unknown-private-node", "unlogged-private-prompt", "private-cache", "private-session", "private-header", "mstrl_mock_secret", "mistral-client"} {
				if strings.Contains(string(encoded), secret) {
					t.Fatalf("private value leaked: %s", secret)
				}
			}
		})
	}
}

func TestMistralCacheAuditUsageBoundsAndRedaction(t *testing.T) {
	a := cacheAuditForTest(t)
	r := a.begin(cacheAuditRecordForTest("http"), Identity{}, cacheAuditBodyForTest(`[]`, `[]`), cacheAuditBodyForTest(`[]`, `[]`), time.Now())
	req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid/v1/chat/completions", strings.NewReader("{}"))
	cfg := config.Default()
	cfg.Accounts = []config.Account{{ID: "m1", RuntimeKey: "private-runtime-key"}}
	c := r.attempt(req, &httptrace.ClientTrace{}, cfg, cfg.Accounts[0], []byte("{}"), 0, []string{"private-session"})
	c.captureUsage([]byte(`{"response":{"usage":{"input_tokens":10,"service_tier":"priority","private-session":"private-runtime-key","unrecognized":"private text","array":[2,null,false]}}}`))
	if !c.log.UsageSanitized || c.log.UsageTruncated || !strings.Contains(string(c.log.Usage), `"service_tier":"priority"`) || strings.Contains(string(c.log.Usage), "private-session") || strings.Contains(string(c.log.Usage), "private-runtime-key") || strings.Contains(string(c.log.Usage), "private text") {
		t.Fatal("nested usage text/key redaction failed", string(c.log.Usage))
	}
	c.captureUsage([]byte(`{"usage":{"large":"` + strings.Repeat("x", mistralAuditUsageLimit) + `"}}`))
	if !c.log.UsageTruncated || c.log.Usage != nil || c.log.UsageBytes <= mistralAuditUsageLimit {
		t.Fatal("oversized usage silently partial or retained")
	}
	c.captureUsage([]byte(`{"usage":{"deep":` + strings.Repeat(`[`, 10) + `1` + strings.Repeat(`]`, 10) + `}}`))
	if !c.log.UsageTruncated || c.log.Usage != nil {
		t.Fatal("deep usage not bounded")
	}
	var off *cacheAuditAttempt
	data := []byte(`{"usage":{"prompt_tokens":1}}`)
	if n := testing.AllocsPerRun(100, func() { off.captureUsage(data); off.response(nil) }); n != 0 {
		t.Fatal("disabled HTTP diagnostics allocated", n)
	}
}

func TestMistralCacheAuditHTTPBoundsSnapshotsAndSecrets(t *testing.T) {
	a := cacheAuditForTest(t)
	body := cacheAuditBodyForTest(`[]`, `[]`)
	r := a.begin(cacheAuditRecordForTest("bounded"), Identity{}, body, body, time.Now())
	req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid", strings.NewReader(string(body)))
	trace := &httptrace.ClientTrace{}
	c := r.attempt(req, trace, config.Default(), config.Account{}, body, 0, []string{"private-session"})
	for i := 0; i < 90; i++ {
		trace.WroteHeaderField(fmt.Sprintf("X-Unknown-%03d", i), []string{strings.Repeat("z", 700), "2", "3", "4", "5"})
	}
	c.finish(&attemptResult{})
	out := r.log.Attempts[0]
	if out.RequestHTTP == nil || !out.RequestHTTP.Truncated || len(out.RequestHTTP.Headers) != mistralAuditHeaderLimit {
		t.Fatal("header count not bounded")
	}
	before, _ := json.Marshal(out.RequestHTTP)
	trace.WroteHeaderField("X-Late-Header", []string{"late"})
	after, _ := json.Marshal(out.RequestHTTP)
	if string(before) != string(after) {
		t.Fatal("late transport callback mutated stored header snapshot")
	}
	h := c.auditHeaders(http.Header{"Server": []string{"private-session"}, "Set-Cookie": []string{"secret=value"}, "X-Unknown": []string{"private text"}})
	if len(h.Headers["Server"].Tags) != 1 || h.Headers["Set-Cookie"].Values[0] != "[REDACTED]" || len(h.Headers["X-Unknown"].Tags) != 1 {
		t.Fatal("allowlisted secret or unknown value leaked")
	}
}
