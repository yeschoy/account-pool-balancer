package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perch"
	"account-pool-balancer/internal/perchstore"
)

// perchModelFake is a Perch model-call proxy: it records the envelope it was
// sent and answers with Perch's own event stream.
type perchModelFake struct {
	srv          *httptest.Server
	calls        atomic.Int32
	threads      atomic.Int32
	lastEnvelope atomic.Value
	// script is the stream it answers with (default: a short answer).
	script string
	delay  time.Duration
	status int
	body   string
	// lastHeaders are the headers of the last model-call request.
	lastHeaders atomic.Value
	// turns collects the cli-turn records the pool reported.
	turnsMu sync.Mutex
	turns   []map[string]any
	// turnStatus answers cli-turn with this status when set.
	turnStatus int

	// tickets counts turn-ticket requests, and modelCalls counts model-call
	// requests. They are tracked apart from `calls` because a CLI turn now
	// makes both, and a test that asserts "the request was sent upstream once"
	// means the model call, not the ticket beside it.
	tickets    atomic.Int32
	modelCalls atomic.Int32
	// ticketStatus / ticketBody answer the ticket request with a refusal when
	// set (default: a usable ticket).
	ticketStatus int
	ticketBody   string
	// lastTicket is the body of the last ticket request.
	lastTicket atomic.Value
	// lastTicketHdr is its headers.
	lastTicketHdr atomic.Value
}

func newPerchModelFake(t *testing.T) *perchModelFake {
	f := &perchModelFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path == perch.TurnTicketPath {
			f.tickets.Add(1)
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.lastTicket.Store(body)
			f.lastTicketHdr.Store(r.Header.Clone())
			w.Header().Set("Content-Type", "application/json")
			if f.ticketStatus >= 300 {
				w.WriteHeader(f.ticketStatus)
				_, _ = io.WriteString(w, f.ticketBody)
				return
			}
			// A usable ticket. runId is stable per fake so a test can assert the
			// model call reused it.
			_, _ = io.WriteString(w, `{"ok":true,"ticket":"tk_fixture","ticketId":"tt_1","runId":"cli-turn-1700000000000-abc123","expiresAt":"`+
				time.Now().Add(10*time.Minute).UTC().Format(time.RFC3339)+`","profile":"standard","enforced":false}`)
			return
		}
		if r.URL.Path == perch.WebThreadsPath {
			// the web surface opens a thread before it posts a message
			f.threads.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true,"thread":{"id":"thread-1","title":"New chat"}}`)
			return
		}
		if r.URL.Path == perch.CLITurnPath {
			var turn map[string]any
			_ = json.NewDecoder(r.Body).Decode(&turn)
			f.turnsMu.Lock()
			f.turns = append(f.turns, turn)
			f.turnsMu.Unlock()
			if f.turnStatus >= 300 {
				w.WriteHeader(f.turnStatus)
				return
			}
			_, _ = io.WriteString(w, `{"ok":true}`)
			return
		}
		if r.URL.Path != perch.ModelCallPath && r.URL.Path != perch.WebChatPath {
			http.NotFound(w, r)
			return
		}
		f.modelCalls.Add(1)
		f.lastHeaders.Store(r.Header.Clone())
		var env map[string]any
		_ = json.NewDecoder(r.Body).Decode(&env)
		f.lastEnvelope.Store(env)
		if f.status >= 300 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.body))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		time.Sleep(f.delay)
		script := f.script
		if script == "" {
			script = `data: {"type":"answer_delta","text":"hi from perch"}` + "\n\n" +
				`data: {"type":"done","ok":true,"text":"hi from perch","finishReason":"stop",` +
				`"model":"deepseek-ai/DeepSeek-V4.1-Flash","usage":{"inputTokens":11,"outputTokens":3}}` + "\n\n"
		}
		_, _ = io.WriteString(w, script)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// withPerchPool turns the shared test env into one holding a Perch account
// whose model-call points at fake, plus a client key for the perch group.
func withPerchPool(t *testing.T, fake *perchModelFake) *env {
	t.Helper()
	e := newEnv(t, []config.Account{{
		ID: "p1", Provider: config.ProviderPerch, Name: "p1@example.com", Email: "p1@example.com",
		PerchUserID: "supa-p1", Group: "perch", Enabled: true,
		RuntimeKey: config.PerchRuntimeKey("supa-p1"),
	}}, func(c *config.Config) {
		c.Upstream.PerchBase = fake.srv.URL
		c.Upstream.PerchWebBase = fake.srv.URL
		// the fake speaks the CLI envelope; the web surface has its own tests
		c.Perch.Surface = "cli"
		// a prefix short enough to keep the test body small but still valid
		c.Perch.GatePrefix = perch.GatePrefixAnchor + " — " + strings.Repeat("gate ", 60)
		c.Perch.GateNotice = "GATEWAY NOTICE: inert."
		c.Perch.IdentityGuard = "GATEWAY NOTICE: you are not Saffron."
		c.ClientKeys = append(c.ClientKeys, config.ClientKey{
			ID: "ck-perch", Group: "perch", KeyHash: config.HashKey("sk-odb-perch-key-0001"), KeyPrefix: "x", Enabled: true,
		})
	})
	// the account's session, bound through the keeper
	sec := perchstore.Secrets{
		UserID: "supa-p1", Email: "p1@example.com", AccessToken: "access-p1",
		RefreshToken: "refresh-p1", ExpiresAt: time.Now().Add(2 * time.Hour).Unix(),
	}
	if _, err := e.srv.perch.keeper.Put(sec, perchstore.PutOptions{BindAs: "p1", FetchedAt: time.Now().UTC(), AllowCreate: true}); err != nil {
		t.Fatal(err)
	}
	return e
}

// postPerch sends an OpenAI request through the perch-group client key.
func postPerch(t *testing.T, e *env, path string, body map[string]any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, e.front.URL+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer sk-odb-perch-key-0001")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A non-streaming OpenAI request is answered as a chat.completion.
func TestPerchProxyNonStream(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model":    "gpt-6-sol",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens int64 `json:"prompt_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "chat.completion" {
		t.Fatalf("object = %q", out.Object)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "hi from perch" {
		t.Fatalf("choices = %+v", out.Choices)
	}
	if out.Choices[0].FinishReason != "stop" {
		t.Fatalf("finish = %q", out.Choices[0].FinishReason)
	}
	if out.Usage.PromptTokens != 11 {
		t.Fatalf("usage = %+v", out.Usage)
	}

	// what Perch was sent: the envelope, with the gate prefix and a model pin
	env := fake.lastEnvelope.Load().(map[string]any)
	if env["clientSurface"] != "cli" {
		t.Fatalf("clientSurface = %v", env["clientSurface"])
	}
	if env["strictManual"] != false {
		t.Fatal("strictManual must be false")
	}
	if env["manualModelOptionId"] != "gpt-6-sol" || env["preferredModelId"] != "gpt-6-sol" {
		t.Fatalf("pin fields = %v / %v", env["preferredModelId"], env["manualModelOptionId"])
	}
	req := env["request"].(map[string]any)
	msgs := req["messages"].([]any)
	sys := msgs[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(sys, perch.GatePrefixAnchor) {
		t.Fatalf("the system turn must open with the gate prefix, got %q", sys[:40])
	}
}

// A streaming request is answered as OpenAI SSE, including usage.
func TestPerchProxyStream(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.script = `data: {"type":"reasoning_delta","text":"thinking"}` + "\n\n" +
		`data: {"type":"answer_delta","text":"Hel"}` + "\n\n" +
		`data: {"type":"answer_delta","text":"lo"}` + "\n\n" +
		`data: {"type":"done","ok":true,"text":"Hello","finishReason":"stop","usage":{"inputTokens":9,"outputTokens":2}}` + "\n\n"
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	var chunks []map[string]any
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			chunks = append(chunks, map[string]any{"done": true})
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(payload), &m) == nil {
			chunks = append(chunks, m)
		}
	}
	if len(chunks) == 0 {
		t.Fatal("no SSE chunks")
	}
	// the last real frame carries usage and no choices
	var text strings.Builder
	var sawReasoning, sawUsage, sawDone bool
	for _, c := range chunks {
		if c["done"] == true {
			sawDone = true
			continue
		}
		if u, ok := c["usage"]; ok && u != nil {
			sawUsage = true
		}
		choices, _ := c["choices"].([]any)
		for _, raw := range choices {
			ch := raw.(map[string]any)
			delta, _ := ch["delta"].(map[string]any)
			if s, ok := delta["content"].(string); ok {
				text.WriteString(s)
			}
			if _, ok := delta["reasoning_content"]; ok {
				sawReasoning = true
			}
		}
	}
	if text.String() != "Hello" {
		t.Fatalf("streamed text = %q", text.String())
	}
	if !sawReasoning {
		t.Fatal("reasoning_content was not passed through")
	}
	if !sawUsage {
		t.Fatal("no usage chunk")
	}
	if !sawDone {
		t.Fatal("the stream did not end with [DONE]")
	}
}

// A plan refusal keeps Perch's status so the pool's error rule can act.
func TestPerchProxyPlanRefusal(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.status = 403
	fake.body = `{"ok":false,"error":"plan does not include direct API access","errorCode":"perch_surface_required"}`
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	// The pool's own rule decides the downstream status: a plan refusal is a
	// 403 with Perch's error code, not a bare upstream passthrough.
	if resp.StatusCode != 403 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "perch_surface_required") {
		t.Fatalf("body = %s", b)
	}
}

// Without a gate prefix every call would be refused upstream, so the pool says
// so locally instead.
func TestPerchProxyRefusesWithoutGatePrefix(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)
	// clear the prefix and make derivation impossible
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		c.Perch.GatePrefix = ""
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.srv.perchPrefixMu.Lock()
	e.srv.perchPrefix, e.srv.perchPrefixKnown = "", true
	e.srv.perchPrefixMu.Unlock()

	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("a request without a gate prefix must not be forwarded")
	}
	if fake.calls.Load() != 0 {
		t.Fatal("nothing should reach Perch without a prefix")
	}
}

// Paths Perch has no counterpart for are refused locally.
func TestPerchPoolServesOnlyChatAndResponses(t *testing.T) {
	for _, p := range []string{"/v1/chat/completions", "/v1/responses"} {
		if !poolPathOK(config.ProviderPerch, p) {
			t.Fatalf("%s should be served", p)
		}
	}
	for _, p := range []string{"/v1/embeddings", "/v1/completions", "/v1/messages"} {
		if poolPathOK(config.ProviderPerch, p) {
			t.Fatalf("%s should be refused locally", p)
		}
	}
}

// A stream_restart voids what came before: the client must not see the
// abandoned run and the new one concatenated.
func TestPerchProxyHandlesStreamRestart(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.script = `data: {"type":"answer_delta","text":"abandoned"}` + "\n\n" +
		`data: {"type":"stream_restart","reason":"mid_tool_call_failover"}` + "\n\n" +
		`data: {"type":"answer_delta","text":"final answer"}` + "\n\n" +
		`data: {"type":"done","ok":true,"text":"final answer","finishReason":"stop"}` + "\n\n"
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Choices) == 0 || out.Choices[0].Message.Content != "final answer" {
		t.Fatalf("content = %+v", out.Choices)
	}
}

// An account whose session needs signing in again is taken out of service and
// the client is told, without the request ever reaching Perch.
func TestPerchProxyExpiredSession(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)
	// point the session at a refresh token the fake refuses, then mark it
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == "p1" {
				c.Accounts[i].PerchRefreshFailed = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("a failed account must not answer 200")
	}
}

// A plan refusal quarantines the account under Perch's own rule, and the
// console names that state "restricted" — not "login-expired", which is
// reserved for a refused refresh token.
func TestPerchPlanRefusalIsRestrictedNotExpired(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.status = 403
	fake.body = `{"ok":false,"error":"plan does not include direct API access","errorCode":"perch_surface_required","limit":{"plan":"starter"}}`
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_, rows := perchAccountsOf(e, "")
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	if got := rows[0]["status"]; got != "restricted" {
		t.Fatalf("status = %v, want restricted (a plan refusal is not an expired login)", got)
	}
	// and the account is still a real session: it was not marked as needing a
	// fresh sign-in
	if rows[0]["refreshFailed"] == true {
		t.Fatal("a plan refusal must not set the refresh-failed flag")
	}
	// the filter finds it
	if _, found := perchAccountsOf(e, "status=restricted"); len(found) != 1 {
		t.Fatal("the restricted filter missed the account")
	}
}

// perchAccountsOf lists the pool through the API on a proxy test env.
func perchAccountsOf(e *env, query string) (int, []map[string]any) {
	req := httptest.NewRequest(http.MethodGet, "/api/perch/accounts?"+query, nil)
	rec := httptest.NewRecorder()
	e.srv.apiPerchAccounts(rec, req)
	var out struct {
		Total    int              `json:"total"`
		Accounts []map[string]any `json:"accounts"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Total, out.Accounts
}

// Perch's own client treats provider_limit as "the allowance ran out"; the
// pool must cool the account rather than quarantine it.
func TestPerchProviderLimitCoolsAccount(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.status = 429
	fake.body = `{"ok":false,"error":"provider refused: insufficient credits","errorCode":"provider_limit","limit":{"limitKind":"provider_limit"}}`
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	resp.Body.Close()
	_, rows := perchAccountsOf(e, "")
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	// a cooldown, not a quarantine: the session is still fine
	if rows[0]["refreshFailed"] == true {
		t.Fatal("a provider limit must not mark the session as failed")
	}
	if got := rows[0]["status"]; got == "login-expired" || got == "restricted" {
		t.Fatalf("status = %v, want a cooldown state", got)
	}
}

// A banned account is quarantined for good — no automatic recovery.
func TestPerchBannedQuarantines(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.status = 403
	fake.body = `{"ok":false,"error":"this account has been banned","errorCode":"banned"}`
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	resp.Body.Close()
	page, _ := e.srv.logs.Query("errors", nil, timeZero(), timeZero(), 10, 0)
	var rule string
	for _, raw := range page.Items {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if r, ok := m["ruleId"].(string); ok && strings.HasPrefix(r, "perch-banned") {
			rule = r
		}
	}
	if rule != "perch-banned" {
		t.Fatalf("the banned code did not match its rule; saw %v", rule)
	}
}

func TestPerchBannedPathsAndRefresh(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body, script string
		stream       bool
		banned       bool
	}{
		{"401 banned", 401, `{"errorCode":"banned","error":"blocked"}`, "", false, true},
		{"403 banned", 403, `{"errorCode":"banned","error":"blocked"}`, "", false, true},
		{"stream banned", 0, "", "data: {\"type\":\"done\",\"ok\":false,\"errorCode\":\"banned\",\"error\":\"blocked\"}\n\n", true, true},
		{"401 expired", 401, `{"error":"session expired"}`, "", false, false},
		{"429", 429, `{"errorCode":"turn_rate_limited"}`, "", false, false},
		{"503", 503, `{"errorCode":"banned"}`, "", false, false},
		{"403 text", 403, `{"error":"not banned"}`, "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newPerchModelFake(t)
			fake.status, fake.body, fake.script = tc.status, tc.body, tc.script
			e := withPerchPool(t, fake)
			hc := fake.srv.Client()
			hc.Transport = &rewriteTransport{target: fake.srv.Listener.Addr().String()}
			e.srv.perch.keeper.SetClient(perchClientFor(hc))
			resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
				"model": "auto", "stream": tc.stream, "messages": []any{map[string]any{"role": "user", "content": "hi"}},
			})
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			v := e.srv.pool.Views(e.srv.cfg.Get())["p1"]
			got := v.State != nil && v.State.HardQuarantined && v.State.RuleID == "perch-banned"
			if got != tc.banned {
				t.Fatalf("banned=%v, state=%+v", got, v.State)
			}
			if tc.banned {
				e.srv.markPerchHealthy("p1")
				v = e.srv.pool.Views(e.srv.cfg.Get())["p1"]
				if v.State == nil || !v.State.HardQuarantined || v.State.RuleID != "perch-banned" {
					t.Fatalf("refresh recovered banned account: %+v", v.State)
				}
			}
		})
	}
}

func TestPerchBannedPersistsSynchronouslyAndRetriesFailure(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("write-fails-%v", failWrite), func(t *testing.T) {
			fake := newPerchModelFake(t)
			fake.status, fake.body = 403, `{"errorCode":"banned","error":"blocked"}`
			e := withPerchPool(t, fake)
			dir := e.srv.dataDir
			if failWrite {
				bad := filepath.Join(t.TempDir(), "not-a-directory")
				if err := os.WriteFile(bad, []byte("x"), 0600); err != nil {
					t.Fatal(err)
				}
				e.srv.dataDir = bad
			}
			resp := postPerch(t, e, "/v1/chat/completions", map[string]any{"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			v := e.srv.pool.Views(e.srv.cfg.Get())["p1"]
			if v.State == nil || !v.State.HardQuarantined || v.State.RuleID != "perch-banned" {
				t.Fatalf("quarantine lost: %+v", v.State)
			}
			if failWrite {
				if !e.srv.metaDirty.Load() {
					t.Fatal("failed write not marked for retry")
				}
				e.srv.dataDir = dir
				if err := e.srv.saveMetadata(false); err != nil {
					t.Fatal(err)
				}
			}
			var md metadata
			data, err := os.ReadFile(filepath.Join(dir, "metadata.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &md); err != nil {
				t.Fatal(err)
			}
			if st := md.Pool.States["p1"]; st == nil || !st.HardQuarantined || st.RuleID != "perch-banned" {
				t.Fatalf("not persisted: %+v", st)
			}
			var wg sync.WaitGroup
			for i := 0; i < 10; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if err := e.srv.saveMetadata(true); err != nil {
						t.Error(err)
					}
				}()
			}
			wg.Wait()
		})
	}
}

func TestPerchBannedRefreshPersistenceAndManualRecovery(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.status, fake.body = 403, `{"errorCode":"banned","error":"blocked"}`
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	e.srv.onPerchRefresh("p1", perchstore.RefreshResult{NeedLogin: true})
	check := func(s *Server) {
		t.Helper()
		v := s.pool.Views(s.cfg.Get())["p1"]
		if v.State == nil || !v.State.HardQuarantined || v.State.RuleID != "perch-banned" {
			t.Fatalf("banned quarantine lost: %+v", v.State)
		}
	}
	check(e.srv)
	e.srv.onPerchRefresh("p1", perchstore.RefreshResult{Renewed: true})
	check(e.srv)
	if err := e.srv.saveMetadata(true); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(e.srv.dataDir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	check(restarted)
	req := httptest.NewRequest(http.MethodPost, "/api/accounts/p1/recover", nil)
	req.SetPathValue("id", "p1")
	w := httptest.NewRecorder()
	restarted.apiAccountAction(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("recover status %d", w.Code)
	}
	v := restarted.pool.Views(restarted.cfg.Get())["p1"]
	if v.State != nil && v.State.HardQuarantined {
		t.Fatalf("manual recovery failed: %+v", v.State)
	}
}

func TestPerchLegacyBannedRulePath(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.status, fake.body = 401, `{"errorCode":"banned","error":"blocked"}`
	e := withPerchPool(t, fake)
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		rs := c.PerchErrorRules
		rs[0].When.BodyCodes = nil
		rs[0].When.BodyContains = []string{"banned"}
		rs[0], rs[1] = rs[1], rs[0]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	v := e.srv.pool.Views(e.srv.cfg.Get())["p1"]
	if v.State == nil || v.State.RuleID != "perch-banned" || !v.State.HardQuarantined {
		t.Fatalf("state = %+v", v.State)
	}
}

// Perch being full is not this account's fault: the account must not be
// cooled, and the request stops rather than spending the pool's other
// accounts on the same wall.
func TestPerchCapacityReachedDoesNotPunishAccount(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.status = 503
	fake.body = `{"ok":false,"error":"capacity reached, try again later","errorCode":"capacity_reached"}`
	e := withPerchPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	resp.Body.Close()
	// It must not be retried either: Perch is full for everyone, so a retry
	// (here or on another account) only spends the pool on a doomed request.
	if got := fake.modelCalls.Load(); got != 1 {
		t.Fatalf("a capacity limit was sent upstream %d times, want 1", got)
	}
	// the pool's own view: the account itself is not in a cooldown
	views := e.srv.pool.Views(e.srv.cfg.Get())
	for id, v := range views {
		if v.State != nil && !v.State.CooldownUntil.IsZero() {
			t.Fatalf("account %s was cooled for a Perch-side capacity limit", id)
		}
	}
}

// timeZero is a zero time for log queries.
func timeZero() time.Time { return time.Time{} }

// withPerchWebPool is withPerchPool for the web surface: the default. The fake
// answers both the thread endpoint and the chat endpoint, so the pool must open
// a thread and then post the message there.
func withPerchWebPool(t *testing.T, fake *perchModelFake) *env {
	t.Helper()
	e := withPerchPool(t, fake)
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		c.Perch.Surface = "web"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The web surface is what the pool serves by default: no persona preamble goes
// upstream, a thread is opened first, and the caller's system turn is folded
// into the one message the lane accepts.
func TestPerchProxyWebSurface(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.script = `data: {"type":"answer_delta","text":"pong"}` + "\n\n" +
		`data: {"type":"done","ok":true,"text":"pong","modelLabel":"MiniMax M3","tier":"standard","durationMs":900}` + "\n\n"
	e := withPerchWebPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "gpt-6-sol",
		"messages": []any{
			map[string]any{"role": "system", "content": "answer in one word"},
			map[string]any{"role": "user", "content": "ping"},
		},
	})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "pong" {
		t.Fatalf("choices = %+v", out.Choices)
	}
	if fake.threads.Load() != 1 {
		t.Fatalf("threads opened = %d, want 1", fake.threads.Load())
	}
	sent, _ := fake.lastEnvelope.Load().(map[string]any)
	msg, _ := sent["message"].(string)
	if !strings.HasPrefix(msg, "answer in one word\n\n") || !strings.HasSuffix(msg, "ping") {
		t.Fatalf("message = %q", msg)
	}
	if sent["threadId"] != "thread-1" {
		t.Fatalf("threadId = %v", sent["threadId"])
	}
	if _, has := sent["prompt"]; has {
		t.Fatal("the web lane takes `message`, not `prompt`")
	}
}

// A request the web lane cannot express (no user turn) is refused locally with
// a readable reason instead of being posted upstream to fail there.
func TestPerchProxyWebSurfaceRefusesSystemOnly(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchWebPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model":    "gpt-6-sol",
		"messages": []any{map[string]any{"role": "system", "content": "only a system turn"}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	if fake.calls.Load() != 0 {
		t.Fatalf("a request with no message must not reach Perch (%d calls)", fake.calls.Load())
	}
}

// withPerchCLIPool serves the CLI surface: the caller's system prompt and its
// tools go upstream in Perch's envelope, and nothing is folded or dropped.
func withPerchCLIPool(t *testing.T, fake *perchModelFake) *env {
	t.Helper()
	e := withPerchPool(t, fake)
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		c.Perch.Surface = "cli"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The CLI surface is the one that carries tools: a request declaring one must
// reach Perch with the tool definition and the caller's system turn intact.
func TestPerchTrackUsagePresence(t *testing.T) {
	for _, tc := range []struct {
		name, raw     string
		input, output *int64
	}{
		{"missing", `{}`, nil, nil},
		{"null", `{"inputTokens":null,"outputTokens":null}`, nil, nil},
		{"zero", `{"inputTokens":0,"outputTokens":0}`, new(int64), new(int64)},
		{"positive", `{"inputTokens":10,"outputTokens":2}`, int64Ptr(10), int64Ptr(2)},
		{"partial", `{"outputTokens":2}`, nil, int64Ptr(2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var u perch.Usage
			if err := json.Unmarshal([]byte(tc.raw), &u); err != nil {
				t.Fatal(err)
			}
			var result attemptResult
			result.trackUsage(&u)
			for _, pair := range []struct{ got, want *int64 }{{result.usage.in, tc.input}, {result.usage.out, tc.output}} {
				if (pair.got == nil) != (pair.want == nil) || pair.got != nil && *pair.got != *pair.want {
					t.Fatalf("got %v, want %v", pair.got, pair.want)
				}
			}
		})
	}
	var result attemptResult
	result.trackUsage(&perch.Usage{})
	if result.usage.in == nil || result.usage.out == nil || *result.usage.in != 0 || *result.usage.out != 0 {
		t.Fatalf("constructed usage = %+v", result.usage)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func TestPerchStreamTTFT(t *testing.T) {
	for _, tc := range []struct {
		name, script string
		wantTTFT     bool
	}{
		{"answer", `data: {"type":"answer_delta","text":"ok"}` + "\n\n", true},
		{"reasoning", `data: {"type":"reasoning_delta","text":"think"}` + "\n\n", true},
		{"tool", `data: {"type":"tool_call_delta","toolCalls":[{"id":"a","name":"run"}]}` + "\n\n", true},
		{"empty", `data: {"type":"answer_delta","text":""}` + "\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := newPerchModelFake(t)
			fake.delay = 80 * time.Millisecond
			fake.script = tc.script + `data: {"type":"done","ok":true}` + "\n\n"
			e := withPerchPool(t, fake)
			resp := postPerch(t, e, "/v1/chat/completions", map[string]any{"model": "auto", "stream": true, "messages": []any{map[string]any{"role": "user", "content": "hi"}}})
			_, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			var logs []map[string]any
			for i := 0; i < 100; i++ {
				logs = e.requestLogs()
				if len(logs) > 0 {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if len(logs) != 1 {
				t.Fatalf("logs = %v", logs)
			}
			ttft := logs[0]["ttftMs"]
			if tc.wantTTFT {
				if n, ok := ttft.(float64); !ok || n < 70 {
					t.Fatalf("ttft = %v, want >= 70ms", ttft)
				}
			} else if ttft != nil {
				t.Fatalf("ttft = %v, want nil", ttft)
			}
		})
	}
}

func TestPerchUsageLogging(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, field       string
			cached, hit, rate any
		}{
			{"missing", "", nil, nil, nil},
			{"null", `,"cacheReadInputTokens":null`, nil, nil, nil},
			{"zero", `,"cacheReadInputTokens":0`, float64(0), false, float64(0)},
			{"positive", `,"cacheReadInputTokens":4`, float64(4), true, float64(0.4)},
		} {
			t.Run(fmt.Sprintf("stream=%t/%s", stream, tc.name), func(t *testing.T) {
				fake := newPerchModelFake(t)
				fake.script = `data: {"type":"answer_delta","text":"ok"}` + "\n\n" +
					`data: {"type":"done","ok":true,"usage":{"inputTokens":10,"outputTokens":2` + tc.field + `}}` + "\n\n"
				e := withPerchPool(t, fake)
				resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
					"model": "auto", "stream": stream, "messages": []any{map[string]any{"role": "user", "content": "hi"}},
				})
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("status=%d error=%v body=%s", resp.StatusCode, err, body)
				}
				var usage map[string]any
				if stream {
					for _, line := range strings.Split(string(body), "\n") {
						var frame map[string]any
						if strings.HasPrefix(line, "data: ") && json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame) == nil {
							if u, ok := frame["usage"].(map[string]any); ok {
								usage = u
							}
						}
					}
				} else {
					var frame map[string]any
					if err := json.Unmarshal(body, &frame); err != nil {
						t.Fatal(err)
					}
					usage, _ = frame["usage"].(map[string]any)
				}
				if usage["prompt_tokens"] != float64(10) || usage["completion_tokens"] != float64(2) || usage["total_tokens"] != float64(12) {
					t.Fatalf("OpenAI usage = %v", usage)
				}
				if tc.name == "positive" {
					if details, ok := usage["prompt_tokens_details"].(map[string]any); !ok || details["cached_tokens"] != float64(4) {
						t.Fatalf("OpenAI usage = %v", usage)
					}
				} else if _, ok := usage["prompt_tokens_details"]; ok {
					t.Fatalf("unexpected OpenAI cache details: %v", usage)
				}
				var logs []map[string]any
				for i := 0; i < 100; i++ {
					logs = e.requestLogs()
					if len(logs) > 0 {
						break
					}
					time.Sleep(time.Millisecond)
				}
				if len(logs) != 1 {
					t.Fatalf("logs = %v", logs)
				}
				log := logs[0]
				if log["cachedTokens"] != tc.cached || log["cacheHit"] != tc.hit || log["cacheRate"] != tc.rate {
					t.Fatalf("cache log = %v", log)
				}
			})
		}
	}
}

func TestPerchProxyCLISurfaceKeepsToolsAndSystem(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchCLIPool(t, fake)
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model":  "gpt-6-sol",
		"stream": false,
		"messages": []any{
			map[string]any{"role": "system", "content": "always answer with a tool call"},
			map[string]any{"role": "user", "content": "weather in Oslo?"},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "get_weather", "description": "weather",
			"parameters": map[string]any{"type": "object", "properties": map[string]any{
				"city": map[string]any{"type": "string"},
			}},
		}}},
	})
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	env, _ := fake.lastEnvelope.Load().(map[string]any)
	req, _ := env["request"].(map[string]any)
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d", len(msgs))
	}
	sys, _ := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "always answer with a tool call") {
		t.Fatalf("the caller's system turn was lost: %.120q", sys)
	}
	tools, _ := req["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want the declared tool to reach Perch", len(tools))
	}
	if env["clientSurface"] != "cli" {
		t.Fatalf("clientSurface = %v", env["clientSurface"])
	}
	if env["lane"] != "chat" {
		t.Fatalf("lane = %v", env["lane"])
	}
}

// ---- device identity, fingerprint and telemetry --------------------------------

// The pool's model call presents the CLI's identity: the UA names the configured
// version, and the locale header follows the setting.
func TestPerchModelCallCarriesTheCLIIdentity(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)
	e.srv.cfg.Update(-1, func(c *config.Config) error {
		c.Perch.CLIVersion = "2.4.121"
		c.Perch.Locale = "zh-CN"
		return nil
	})
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model":    "gpt-6-sol",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	})
	resp.Body.Close()
	h, _ := fake.lastHeaders.Load().(http.Header)
	if got := h.Get("User-Agent"); got != "perchai-cli/2.4.121" {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := h.Get("x-perch-locale"); got != "zh-CN" {
		t.Fatalf("x-perch-locale = %q", got)
	}
	// the envelope names the CLI's cwd-derived thread, not a random one
	env := fake.lastEnvelope.Load().(map[string]any)
	thread, _ := env["threadId"].(string)
	if !strings.HasPrefix(thread, perch.ThreadIDPrefix) {
		t.Fatalf("envelope threadId = %q, want a %q id", thread, perch.ThreadIDPrefix)
	}
	// and it is the same on the next call: an identity, not a request id
	postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model":    "gpt-6-sol",
		"messages": []any{map[string]any{"role": "user", "content": "again"}},
	}).Body.Close()
	env2 := fake.lastEnvelope.Load().(map[string]any)
	if env2["threadId"] != thread {
		t.Fatalf("threadId changed between calls: %q → %v", thread, env2["threadId"])
	}
	// The runId is the turn ticket's, not a per-request random one. The
	// published client takes its runId from the ticket response and reuses it,
	// so a fresh id per call beside a ticket is a pairing it never makes. The
	// fake hands out one stable ticket, so both calls name the same run.
	if env2["runId"] != env["runId"] {
		t.Fatalf("runId must come from the turn ticket, not be minted per call: %v → %v", env["runId"], env2["runId"])
	}
	if got := env["runId"]; got != "cli-turn-1700000000000-abc123" {
		t.Fatalf("envelope runId = %v, want the ticket's runId", got)
	}
	// and the ticket is actually presented
	if got := h.Get(perch.TicketHeader); got != "tk_fixture" {
		t.Fatalf("%s = %q, want the issued ticket", perch.TicketHeader, got)
	}
}

// With telemetry egress off (the default) the pool reports nothing to Perch's
// cli-turn endpoint, but still records what it would have sent.
func TestPerchTurnIsAuditedNotSentByDefault(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)
	postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "gpt-6-sol",
		"messages": []any{
			map[string]any{"role": "system", "content": "be brief"},
			map[string]any{"role": "user", "content": "a secret question"},
		},
	}).Body.Close()

	fake.turnsMu.Lock()
	n := len(fake.turns)
	fake.turnsMu.Unlock()
	if n != 0 {
		t.Fatalf("audit-only mode posted %d cli-turn records", n)
	}

	var audited string
	page, err := e.srv.logs.Query("perch", map[string]string{"event": "cli_turn"}, time.Time{}, time.Time{}, 50, 0)
	if err != nil {
		t.Fatalf("query perch log: %v", err)
	}
	for _, r := range page.Items {
		audited += string(r)
	}
	if audited == "" {
		t.Fatal("no cli-turn audit record was written")
	}
	if !strings.Contains(audited, "未发送") {
		t.Fatalf("audit record should say it was not sent: %s", audited)
	}
	// the conversation itself must not reach the log
	if strings.Contains(audited, "a secret question") || strings.Contains(audited, "hi from perch") {
		t.Fatalf("audit record leaks the conversation: %s", audited)
	}
}

// With egress on, the record is posted with the account's own token and the
// CLI's field set, and the prompt and answer are exactly what was exchanged.
func TestPerchTurnPostsWhenEgressIsOn(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)
	e.srv.cfg.Update(-1, func(c *config.Config) error {
		c.Perch.TelemetryEgress = true
		c.Perch.CLITurnReport = true
		c.Upstream.PerchBase = fake.srv.URL
		c.Perch.AppURL = fake.srv.URL
		return nil
	})
	postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model":    "gpt-6-sol",
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
	}).Body.Close()

	fake.turnsMu.Lock()
	defer fake.turnsMu.Unlock()
	if len(fake.turns) != 1 {
		t.Fatalf("posted %d cli-turn records, want 1", len(fake.turns))
	}
	turn := fake.turns[0]
	if turn["status"] != perch.TurnStatusComplete {
		t.Fatalf("status = %v", turn["status"])
	}
	if !strings.Contains(turn["prompt"].(string), "hello") {
		t.Fatalf("prompt = %v", turn["prompt"])
	}
	if turn["assistantText"] != "hi from perch" {
		t.Fatalf("assistantText = %v", turn["assistantText"])
	}
	if !strings.HasPrefix(turn["threadId"].(string), perch.ThreadIDPrefix) {
		t.Fatalf("threadId = %v", turn["threadId"])
	}
	// the CLI's full field set, so the record is not obviously thinner
	for _, field := range []string{"runId", "chatMode", "personaId", "durationMs", "error", "events", "contextSnapshot", "systemInitiated", "attachments"} {
		if _, ok := turn[field]; !ok {
			t.Errorf("cli-turn record lacks %q", field)
		}
	}
}

// Two accounts must never present the same device. The pool keys identity on
// the Perch user id, so a second account gets its own.
func TestPerchDeviceIdentityIsPerAccount(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)
	a := e.srv.perchIdentity("p1")
	if a.MachineID == "" {
		t.Fatal("the account has no machine id")
	}
	b := e.srv.perchIdentity("p2")
	if b.MachineID == a.MachineID {
		t.Fatal("two accounts share a machine id")
	}
	if again := e.srv.perchIdentity("p1"); again.MachineID != a.MachineID {
		t.Fatal("the machine id is not stable across calls")
	}
}

// The console can see what the pool presents: stable per-account ids, no
// sharing, and the exact identity headers.
func TestPerchFingerprintView(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)
	rec := httptest.NewRecorder()
	e.srv.apiPerchFingerprint(rec, httptest.NewRequest(http.MethodGet, "/api/perch/fingerprint", nil))
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		StoreAvailable bool `json:"storeAvailable"`
		Settings       map[string]any
		Accounts       []struct {
			ID        string `json:"id"`
			MachineID string `json:"machineId"`
			ThreadID  string `json:"threadId"`
			CreatedAt string `json:"createdAt"`
			Shared    bool   `json:"shared"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.StoreAvailable {
		t.Fatal("the device store should be available")
	}
	if len(out.Accounts) != 1 {
		t.Fatalf("accounts = %+v", out.Accounts)
	}
	a := out.Accounts[0]
	if a.MachineID == "" || a.ThreadID == "" || a.CreatedAt == "" {
		t.Fatalf("identity incomplete: %+v", a)
	}
	if a.Shared {
		t.Fatal("a single account cannot share its id")
	}
	if out.Settings["userAgent"] != "perchai-cli/2.4.121" && out.Settings["userAgent"] == "" {
		t.Fatalf("settings = %+v", out.Settings)
	}
	// it reports the switch, so an operator knows nothing is being sent
	if out.Settings["telemetryEgress"] != false {
		t.Fatalf("egress should default to false: %+v", out.Settings)
	}
}

// A CLI turn obtains a turn ticket before its model call, presents it, and uses
// the ticket's own runId. This is the sequence perchai-cli's `perch run` makes;
// a pool call without it posts a request shape the published client never
// produces.
func TestPerchTurnTicketIssuedThenPresented(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)

	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	if got := fake.tickets.Load(); got != 1 {
		t.Fatalf("issued %d tickets, want 1", got)
	}
	tb, _ := fake.lastTicket.Load().(map[string]any)
	if tb["surface"] != "cli" || tb["profile"] != "standard" {
		t.Fatalf("ticket request body = %v", tb)
	}
	hdr, _ := fake.lastHeaders.Load().(http.Header)
	if got := hdr.Get(perch.TicketHeader); got != "tk_fixture" {
		t.Fatalf("%s = %q, want the issued ticket", perch.TicketHeader, got)
	}
	// The envelope names the ticket's run, not one minted per request.
	env, _ := fake.lastEnvelope.Load().(map[string]any)
	if got := env["runId"]; got != "cli-turn-1700000000000-abc123" {
		t.Fatalf("envelope runId = %v, want the ticket's", got)
	}
	// A complete envelope must not claim the CLI's delta mode.
	if got := hdr.Get("X-Perch-Call-Delta"); got != "" {
		t.Fatalf("x-perch-call-delta = %q, want none", got)
	}
}

// An enforced turn-rate limit ends the request: the pool must not cool the
// account and try the same request elsewhere. Rotating after the quota gate is
// the allowance circumvention this gate exists to stop, and it is what turns a
// single refused turn into a fleet of them.
func TestPerchTurnRateLimitDoesNotRotate(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.ticketStatus = http.StatusTooManyRequests
	fake.ticketBody = `{"ok":false,"enforced":true,"errorCode":"turn_rate_limited","error":"your plan's turn rate limit reached"}`
	e := withPerchPool(t, fake)

	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "turn_rate_limited") {
		t.Fatalf("body = %s, want the turn_rate_limited code", body)
	}
	// The model was never called: the gate refused first.
	if got := fake.modelCalls.Load(); got != 0 {
		t.Fatalf("the model was called %d times, want 0", got)
	}
	if got := fake.tickets.Load(); got != 1 {
		t.Fatalf("ticket requests = %d, want 1 (no rotation)", got)
	}
	// and the account was not punished with a cooldown
	v := e.srv.pool.Views(e.srv.cfg.Get())["p1"]
	if v.State != nil && !v.State.CooldownUntil.IsZero() {
		t.Fatalf("account was cooled for a plan quota verdict: %+v", v.State)
	}
}

// An unenforced ticket failure degrades: the model call still happens, without
// a ticket. That is the published client's own behaviour.
func TestPerchTicketFailureUnenforcedStillCallsModel(t *testing.T) {
	fake := newPerchModelFake(t)
	fake.ticketStatus = http.StatusBadGateway
	fake.ticketBody = `{"ok":false,"error":"ticket service down"}`
	e := withPerchPool(t, fake)

	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := fake.modelCalls.Load(); got != 1 {
		t.Fatalf("model calls = %d, want 1", got)
	}
	hdr, _ := fake.lastHeaders.Load().(http.Header)
	if got := hdr.Get(perch.TicketHeader); got != "" {
		t.Fatalf("%s = %q, want none", perch.TicketHeader, got)
	}
}

// Attribution carries workspaceId, or is null. The published client never sends
// a userId-only block.
func TestPerchAttributionNeedsWorkspace(t *testing.T) {
	fake := newPerchModelFake(t)
	e := withPerchPool(t, fake)

	// Without a quota snapshot there is no workspace id: the block must be
	// dropped entirely, not sent half-filled.
	resp := postPerch(t, e, "/v1/chat/completions", map[string]any{
		"model": "auto", "messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	resp.Body.Close()
	env, _ := fake.lastEnvelope.Load().(map[string]any)
	if attrib, ok := env["attribution"]; ok && attrib != nil {
		t.Fatalf("attribution = %v, want null/absent without a workspace id", attrib)
	}
}
