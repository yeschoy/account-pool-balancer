package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func enableCacheAuditForTest(t *testing.T, e *mistralEnv) {
	t.Helper()
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		off := false
		c.Logs.Detail = false
		c.Mistral.DetailLog = &off
		c.Routing.MaxBodyMB = 16
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var err error
	e.s.mistralAudit, err = newMistralCacheAudit(e.s.cfg.Get(), "client1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
}
func cacheAuditRecordsForTest(t *testing.T, e *mistralEnv) []requestLog {
	t.Helper()
	records, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 100, 0)
	var out []requestLog
	for _, raw := range records.Items {
		var r requestLog
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TS.Before(out[j].TS) })
	return out
}

func TestMistralCacheAuditHTTPFullBodyAndConnections(t *testing.T) {
	e := newMistralEnv(t)
	enableCacheAuditForTest(t, e)
	text := strings.Repeat("private-unlogged-long-content-", 240000)
	body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":%q}],"tools":[{"function":{"parameters":{"properties":{"z":{},"a":{}}}}}],"prompt_cache_key":"private-cache"}`, text)
	for i := 0; i < 3; i++ {
		if i == 2 {
			e.s.mistralHTTPClient(e.s.cfg.Get(), "").CloseIdleConnections()
		}
		if status, _ := e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "private-session"}); status != 200 {
			t.Fatal(status)
		}
	}
	records := cacheAuditRecordsForTest(t, e)
	if len(records) != 3 {
		t.Fatal("missing request logs")
	}
	var firstConnection string
	for i, r := range records {
		if r.CacheAudit == nil || len(r.CacheAudit.Attempts) != 1 {
			t.Fatal("detail-off audit missing")
		}
		a := r.CacheAudit.Attempts[0]
		if a.Expected.Bytes != len(e.call(i).body) || a.Expected.Tag != e.s.mistralAudit.bodyTag([]byte(e.call(i).body)) || len(a.BodyReads) != 1 || !a.BodyReads[0].MatchesExpected || !a.BodyReads[0].Complete || a.WriteSuccesses != 1 || !a.Sent || a.Status != 200 {
			t.Fatalf("actual full request not observed: %+v", a)
		}
		if len(a.Connections) != 1 || a.Connections[0].ID == "" || a.TLSHandshakes != 0 {
			t.Fatal("plain HTTP connection observation missing/fabricated TLS")
		}
		if i == 0 {
			firstConnection = a.Connections[0].ID
			if a.Connections[0].Reused {
				t.Fatal("first mock connection reused")
			}
		}
		if i == 1 && (a.Connections[0].ID != firstConnection || !a.Connections[0].Reused || r.CacheAudit.PreviousPrepared == nil || !*r.CacheAudit.PreviousPrepared.Comparison.PreviousSequenceRetained) {
			t.Fatal("keep-alive/full previous history not identified")
		}
		if i == 2 && (a.Connections[0].ID == firstConnection || a.Connections[0].Reused) {
			t.Fatal("fresh connection not identified")
		}
		encoded, _ := json.Marshal(r.CacheAudit)
		for _, private := range []string{"private-unlogged", "private-session", "private-cache", "mstrl_mock_secret", e.up.URL, "127.0.0.1"} {
			if strings.Contains(string(encoded), private) {
				t.Fatalf("audit leaked %q", private)
			}
		}
		if len(encoded) > 24<<10 {
			t.Fatal("fingerprint record unexpectedly large")
		}
	}
	details, _ := e.s.logs.Query("details", nil, time.Time{}, time.Time{}, 10, 0)
	if len(details.Items) != 0 {
		t.Fatal("fingerprints implicitly enabled body details")
	}
	// A different authenticated mock key cannot opt itself into the target.
	e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, map[string]string{"Authorization": "Bearer mistral-other-client"})
	all := cacheAuditRecordsForTest(t, e)
	if all[len(all)-1].CacheAudit != nil {
		t.Fatal("other client key captured")
	}
	req, _ := http.NewRequest(http.MethodGet, e.front.URL+"/api/logs/requests", nil)
	req.Header.Set("Authorization", "Bearer mistral-client")
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("runtime key could read administrator fingerprints")
	}
}

func TestMistralCacheAuditHTTP2TLSAndAdaptation(t *testing.T) {
	e := newMistralEnv(t)
	enableCacheAuditForTest(t, e)
	tlsUp := httptest.NewUnstartedServer(e.up.Config.Handler)
	tlsUp.EnableHTTP2 = true
	tlsUp.StartTLS()
	defer tlsUp.Close()
	transport := tlsUp.Client().Transport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	defer transport.CloseIdleConnections()
	// Trust the mock certificate only on Mistral's independent transport.
	mistralTransport := e.s.mistralHTTPClient(e.s.cfg.Get(), "").Transport.(*http.Transport)
	mistralTransport.TLSClientConfig = transport.TLSClientConfig.Clone()
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Mistral.BaseURL = tlsUp.URL + "/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		extra := ""
		if i == 1 {
			extra = `,{"role":"user","content":"next"}`
		}
		body := `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"developer","content":"private instruction"},{"role":"assistant","reasoning":"private thought","content":"answer"}` + extra + `],"tools":[{"type":"function","function":{"name":"x","parameters":{"properties":{"z":{},"a":{}}}}}],"thinking":{"type":"enabled"}}`
		if status, _ := e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "private-tls-session"}); status != 200 {
			t.Fatal(status)
		}
	}
	rs := cacheAuditRecordsForTest(t, e)
	for i, r := range rs {
		a := r.CacheAudit.Attempts[0]
		if r.CacheAudit.Preparation.BodyEqual || !*r.CacheAudit.Preparation.ToolsEqual || !a.Preparation.BodyEqual || !a.BodyReads[0].MatchesExpected {
			t.Fatal("native preparation mistaken for wire corruption")
		}
		if i == 0 && (a.TLSHandshakes != 1 || a.TLSResumed || a.Connections[0].Reused) {
			t.Fatal("new TLS handshake missing")
		}
		if i == 1 && (a.TLSHandshakes != 0 || !a.Connections[0].Reused || a.Connections[0].ID != rs[0].CacheAudit.Attempts[0].Connections[0].ID || !*r.CacheAudit.PreviousPrepared.Comparison.PreviousSequenceRetained) {
			t.Fatal("HTTP/2 reuse/growing adapted prefix missing")
		}
	}
}

func TestMistralCacheAuditStreamingFlush(t *testing.T) {
	e := newMistralEnv(t)
	enableCacheAuditForTest(t, e)
	release := make(chan struct{})
	var released atomic.Bool
	unblock := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	defer unblock()
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"early\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\n\ndata: [DONE]\n\n")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.front.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true}`))
	req.Header.Set("Authorization", "Bearer mistral-client")
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, "early") {
		t.Fatal("audit buffered the first SSE event", err)
	}
	unblock()
	tail, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(tail), "[DONE]") {
		t.Fatal("audit changed SSE completion", err)
	}
	records := cacheAuditRecordsForTest(t, e)
	if len(records) != 1 || records[0].Result != "success" || !records[0].CacheAudit.Attempts[0].BodyReads[0].MatchesExpected {
		t.Fatal("streaming audit/usage not finalized")
	}
}

func TestMistralCacheAuditRetriesRemainCorrelated(t *testing.T) {
	e := newMistralEnv(t)
	enableCacheAuditForTest(t, e)
	enableMistralWireDetails(t, e)
	var calls atomic.Int32
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"message":"limited"}`)
			return
		}
		w.Header().Set("Mistral-Correlation-Id", "native-audit-trace")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":20,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":0}}}`)
	})
	if status, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"x"}]}`, map[string]string{"Session-Id": "retry-session"}); status != 200 {
		t.Fatal(status)
	}
	r := cacheAuditRecordsForTest(t, e)[0]
	as := r.CacheAudit.Attempts
	if len(as) != 2 || as[0].AttemptIndex != 0 || as[0].Status != 429 || as[1].AttemptIndex != 1 || as[1].Status != 200 || as[0].AccountID == as[1].AccountID || as[0].SourceTag == as[1].SourceTag || as[1].UpstreamRequestID != "native-audit-trace" || r.CachedTokens == nil || *r.CachedTokens != 0 {
		t.Fatal("retry/account/source/trace/usage changed")
	}
	for _, a := range as {
		if len(a.BodyReads) != 1 || !a.BodyReads[0].MatchesExpected || a.WriteSuccesses != 1 {
			t.Fatal("retried full native body not observed")
		}
	}
	if as[1].Previous.RequestID != r.RequestID || as[1].Previous.AttemptIndex != 0 || !as[1].Previous.Comparison.BodyEqual {
		t.Fatal("wire predecessor lost")
	}
	detail := mistralWireRecord(t, e)
	if len(detail.UpstreamAttempts) != 2 || http.Header(detail.UpstreamAttempts[0].Request.Headers).Get("Content-Length") == "" {
		t.Fatal("audit replaced native detail/header hooks")
	}
}

type auditCloseReader struct {
	io.Reader
	closed *int
}

func (r *auditCloseReader) Close() error { (*r.closed)++; return nil }

func TestMistralCacheAuditAffinityIncludesAllValues(t *testing.T) {
	a := cacheAuditForTest(t)
	body := cacheAuditBodyForTest(`[]`, `[]`)
	seen := map[string]bool{}
	for _, values := range [][]string{{"first", "second"}, {"first", "different"}, {"second", "first"}, {"", "second"}} {
		r := a.begin(cacheAuditRecordForTest("affinity"), Identity{}, body, body, time.Now())
		req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid", bytes.NewReader(body))
		req.Header["X-Affinity"] = values
		c := r.attempt(req, &httptrace.ClientTrace{}, nil, config.Account{}, body, 0, nil)
		c.finish(&attemptResult{})
		tag := r.log.Attempts[0].AffinityTag
		if tag == "" || seen[tag] {
			t.Fatal("multi-value affinity was collapsed to the first value")
		}
		seen[tag] = true
	}
}

func TestMistralCacheAuditDetectsActualReaderMismatch(t *testing.T) {
	a := cacheAuditForTest(t)
	expected := cacheAuditBodyForTest(`[{"role":"user","content":"x"}]`, `[]`)
	actual := bytes.Replace(expected, []byte(`"x"`), []byte(`"y"`), 1)
	for _, observeActual := range []bool{false, true} {
		r := a.begin(cacheAuditRecordForTest("mismatch"), Identity{}, expected, expected, time.Now())
		req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid", bytes.NewReader(actual))
		announced := expected
		if observeActual {
			announced = actual
		}
		c := r.attempt(req, &httptrace.ClientTrace{}, nil, config.Account{ID: "m1"}, announced, 0, nil)
		got, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil || !bytes.Equal(got, actual) {
			t.Fatal("diagnostic changed actual reader")
		}
		c.finish(&attemptResult{})
		out := r.log.Attempts[0]
		if !out.BodyReads[0].Complete || out.BodyReads[0].MatchesExpected != observeActual || out.BodyReads[0].Tag != a.bodyTag(actual) || out.Preparation.BodyEqual == observeActual {
			t.Fatal("different same-length bytes hidden by a fabricated read hash")
		}
	}
}

func TestMistralCacheAuditTracePrivacy(t *testing.T) {
	a := cacheAuditForTest(t)
	body := cacheAuditBodyForTest(`[]`, `[]`)
	cfg := config.Default()
	cfg.Accounts = []config.Account{{ID: "other", RuntimeKey: "other-account-secret"}}
	for _, value := range []string{"other-account-secret", "selected-runtime-secret", "private-session-value"} {
		r := a.begin(cacheAuditRecordForTest("trace"), Identity{}, body, body, time.Now())
		req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid", bytes.NewReader(body))
		c := r.attempt(req, &httptrace.ClientTrace{}, cfg, config.Account{RuntimeKey: "selected-runtime-secret"}, body, 0, []string{"private-session-value"})
		header := http.Header{"Mistral-Correlation-Id": []string{"trace-" + value}}
		c.finish(&attemptResult{header: header, upstreamStatus: 200})
		if !r.log.Attempts[0].TraceRedacted || r.log.Attempts[0].UpstreamRequestID != "" || header.Get("Mistral-Correlation-Id") != "trace-"+value {
			t.Fatal("private trace leaked or actual response was changed")
		}
	}
}

func TestMistralCacheAuditReaderReplayPartialAndCaps(t *testing.T) {
	a := cacheAuditForTest(t)
	body := cacheAuditBodyForTest(`[{"role":"user","content":"x"}]`, `[]`)
	r := a.begin(cacheAuditRecordForTest("readers"), Identity{}, body, body, time.Now())
	req, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid/v1/chat/completions", bytes.NewReader(body))
	closed, getCalls := 0, 0
	sentinel := errors.New("reader unavailable")
	failGet := false
	req.Body = &auditCloseReader{Reader: bytes.NewReader(body), closed: &closed}
	req.GetBody = func() (io.ReadCloser, error) {
		getCalls++
		if failGet {
			return nil, sentinel
		}
		return &auditCloseReader{Reader: bytes.NewReader(body), closed: &closed}, nil
	}
	originalHooks := 0
	trace := &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { originalHooks++ }, GotConn: func(httptrace.GotConnInfo) { originalHooks++ }, TLSHandshakeDone: func(tls.ConnectionState, error) { originalHooks++ }}
	attempt := r.attempt(req, trace, nil, config.Account{ID: "m1", RuntimeKey: "private-runtime"}, body, 0, nil)
	prefix := make([]byte, 9)
	if _, err := io.ReadFull(req.Body, prefix); err != nil {
		t.Fatal(err)
	}
	req.Body.Close()
	recreated, err := req.GetBody()
	if err != nil {
		t.Fatal(err)
	}
	read, err := io.ReadAll(recreated)
	recreated.Close()
	if err != nil || !bytes.Equal(read, body) || closed != 2 {
		t.Fatal("GetBody/Close semantics changed")
	}
	failGet = true
	if _, err := req.GetBody(); err != sentinel {
		t.Fatal("GetBody failure swallowed")
	}
	failGet = false
	trace.GotConn(httptrace.GotConnInfo{})
	trace.TLSHandshakeDone(tls.ConnectionState{DidResume: true}, nil)
	trace.WroteRequest(httptrace.WroteRequestInfo{Err: sentinel})
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	for i := 0; i < mistralAuditEvents+1; i++ {
		b, _ := req.GetBody()
		b.Close()
		trace.GotConn(httptrace.GotConnInfo{})
	}
	attempt.finish(&attemptResult{terminal: "network"})
	out := r.log.Attempts[0]
	if originalHooks != mistralAuditEvents+5 || out.WriteSuccesses != 1 || out.WriteFailures != 1 || !out.TLSResumed || len(out.BodyReads) != mistralAuditEvents || !out.EventsTruncated || out.BodyReads[0].Complete || out.BodyReads[0].Bytes != 9 || !out.BodyReads[1].MatchesExpected || len(out.Connections) != mistralAuditEvents || getCalls != mistralAuditEvents+3 {
		t.Fatalf("partial/replay/cap/hooks incorrect: %+v hooks=%d calls=%d", out, originalHooks, getCalls)
	}
	io.ReadAll(req.Body) // Late asynchronous reads/callbacks cannot rewrite a persisted snapshot.
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	if r.log.Attempts[0].BodyReads[0].Bytes != 9 || r.log.Attempts[0].WriteSuccesses != 1 {
		t.Fatal("late callback changed snapshot")
	}
	for i := 1; i < mistralAuditAttempts; i++ {
		q, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid", bytes.NewReader(body))
		r.attempt(q, &httptrace.ClientTrace{}, nil, config.Account{ID: "m1"}, body, i, nil).finish(&attemptResult{})
	}
	q, _ := http.NewRequest(http.MethodPost, "https://upstream.invalid", bytes.NewReader(body))
	old := q.Body
	if r.attempt(q, &httptrace.ClientTrace{}, nil, config.Account{}, body, mistralAuditAttempts, nil) != nil || q.Body != old || !r.log.AttemptsTruncated {
		t.Fatal("audit cap altered unobserved attempts")
	}
	for i := 0; i < mistralAuditConnections+1; i++ {
		left, right := net.Pipe()
		id := a.connectionID(left)
		if i < mistralAuditConnections && (id == "" || a.connectionID(left) != id) || i == mistralAuditConnections && id != "" {
			t.Fatal("connection ID registry cap/stability failed")
		}
		left.Close()
		right.Close()
	}
	if len(a.connections) != mistralAuditConnections {
		t.Fatal("unbounded connection registry")
	}
	a.expire(a.expires)
	if len(a.connections) != 0 || len(a.wire) != 0 {
		t.Fatal("maintenance expiry kept references")
	}
}
