package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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
	lastEnvelope atomic.Value
	// script is the stream it answers with (default: a short answer).
	script string
	status int
	body   string
}

func newPerchModelFake(t *testing.T) *perchModelFake {
	f := &perchModelFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != perch.ModelCallPath {
			http.NotFound(w, r)
			return
		}
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
	if got := fake.calls.Load(); got != 1 {
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
