package server

import (
	"context"
	"encoding/json"
	"fmt"
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
	"account-pool-balancer/internal/opencode"
)

func newGoGatewayEnv(t *testing.T) *ocEnv {
	t.Helper()
	e := newOCEnv(t, false)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.OpenCode.Compatibility = false
		r := c.Routing
		r.Sticky = true
		r.ConcurrencyWaitMs = 0
		c.OpenCode.GoRouting = &r
		for i := range c.Accounts {
			if c.Accounts[i].ID == "go1" {
				c.Accounts[i].Priority = 1
				c.Accounts[i].MaxConc = 1
			}
		}
		c.Accounts = append(c.Accounts, config.Account{ID: "go2", Provider: config.ProviderOpenCodeGo, Group: config.ProviderOpenCodeGo, RuntimeKey: "second-go-secret-key", Enabled: true, MaxConc: 1, Priority: 2})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.s.cfg.Get().Account("go2")
	if err = e.s.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return e
}

func goLogRows(t *testing.T, s *Server, kind string, filters map[string]string, want int) []map[string]any {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		page, err := s.logs.Query(kind, filters, time.Time{}, time.Time{}, 100, 0)
		if err == nil && len(page.Items) >= want {
			out := []map[string]any{}
			for _, raw := range page.Items {
				var m map[string]any
				if json.Unmarshal(raw, &m) != nil {
					t.Fatal("invalid log JSON")
				}
				out = append(out, m)
			}
			return out
		}
		select {
		case <-deadline.C:
			t.Fatalf("%s logs not available: %v", kind, err)
			return nil
		case <-tick.C:
		}
	}
}

func TestGoConversationOutranksCacheAndHintsAreCallerScoped(t *testing.T) {
	s := &Server{secret: []byte("test-routing-secret")}
	cfg := config.Default()
	h := http.Header{"Session_id": []string{"codex-conversation"}}
	body := map[string]any{"model": "chat", "prompt_cache_key": "cache-a", "messages": []any{map[string]any{"role": "user", "content": "hello"}}}
	a, ca, _ := s.goIdentity(cfg, "go\x00client-a", h, body)
	body["prompt_cache_key"] = "cache-b"
	b, cb, _ := s.goIdentity(cfg, "go\x00client-a", h, body)
	if a.KeyType != "session_id" || a.Fingerprint != b.Fingerprint || ca == cb {
		t.Fatal("cache hint changed explicit conversation")
	}
	c, cc, _ := s.goIdentity(cfg, "go\x00client-b", h, body)
	if c.Fingerprint == a.Fingerprint || cc == cb {
		t.Fatal("cross-caller affinity collision")
	}
	parent, _, _ := s.goIdentity(cfg, "go\x00client-a", http.Header{"X-Session-Id": []string{"root"}}, body)
	child, _, _ := s.goIdentity(cfg, "go\x00client-a", http.Header{"X-Parent-Session-Id": []string{"root"}, "X-Session-Id": []string{"child"}}, body)
	if parent.Fingerprint != child.Fingerprint {
		t.Fatal("parent child affinity differs")
	}
	cfg.Routing.FallbackHash = false
	noSession, cache, _ := s.goIdentity(cfg, "scope", http.Header{"X-Request-Id": []string{"changing-request"}}, body)
	if noSession.Fingerprint != "" || cache == "" {
		t.Fatal("cache/request ID became conversation state")
	}
	legacy := s.identity(cfg, "scope", h, body)
	if legacy.KeyType != "prompt_cache_key" {
		t.Fatal("other-provider identity semantics changed")
	}
}

func TestGoGatewayUsesSessionThenCachePreferenceAndPreservesBody(t *testing.T) {
	e := newGoGatewayEnv(t)
	var mu sync.Mutex
	var sessions, uas []string
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sessions = append(sessions, r.Header.Get("X-Opencode-Session"))
		uas = append(uas, r.Header.Get("User-Agent"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ocReply(opencode.Chat))
	}
	body := `{ "model":"chat", "prompt_cache_key":"cache-one", "messages":[{"role":"user","content":"first"}], "max_tokens":20 }`
	headers := map[string]string{"Session_id": "conversation-a", "User-Agent": "codex_cli_rs/test"}
	if st, b, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, headers); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == "go2" {
				c.Accounts[i].Priority = 0
			}
			if c.Accounts[i].ID == "go1" {
				c.Accounts[i].Priority = 90
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	second := strings.Replace(body, "cache-one", "cache-two", 1)
	if st, b, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", second, headers); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	headers["Session_id"] = "conversation-b"
	if st, b, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, headers); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	e.mu.Lock()
	keys := append([]string(nil), e.keys...)
	sent := append([]byte(nil), e.bodies[0]...)
	e.mu.Unlock()
	if len(keys) != 3 || keys[0] != keys[1] || keys[0] != keys[2] {
		t.Fatalf("account affinity not preserved: %v", keys)
	}
	if string(sent) != body {
		t.Fatal("native prompt/cache bytes rewritten")
	}
	mu.Lock()
	defer mu.Unlock()
	if sessions[0] == "" || sessions[0] != sessions[1] || sessions[0] == sessions[2] {
		t.Fatal("upstream sessions merged or changed")
	}
	if uas[0] != "codex_cli_rs/test" {
		t.Fatal("real client UA replaced")
	}
	logs := goLogRows(t, e.s, "requests", map[string]string{"provider": config.ProviderOpenCodeGo}, 3)
	found := false
	for _, r := range logs {
		if r["cacheAffinityResult"] == "hit" {
			found = true
		}
		raw, _ := json.Marshal(r)
		if strings.Contains(string(raw), "conversation-a") || strings.Contains(string(raw), "cache-one") {
			t.Fatal("raw identity leaked")
		}
	}
	if !found {
		t.Fatal("cache affinity result not logged")
	}
}

func TestGoBusySessionDoesNotOverflowAndLocalErrorsAreLogged(t *testing.T) {
	e := newGoGatewayEnv(t)
	h := map[string]string{"X-Claude-Code-Session-Id": "busy-session", "User-Agent": "claude-code/test"}
	body := ocRequest(opencode.Messages, "messages", false)
	if st, b, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/messages", body, h); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ocReply(opencode.Messages))
	}
	e.mu.Unlock()
	done := make(chan int, 1)
	go func() { st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/messages", body, h); done <- st }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request not started")
	}
	st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/messages", body, h)
	close(release)
	if <-done != 200 || st != 429 {
		t.Fatalf("busy session status %d", st)
	}
	if e.count() != 2 {
		t.Fatal("busy request borrowed another account")
	}
	rows := goLogRows(t, e.s, "errors", map[string]string{"provider": config.ProviderOpenCodeGo, "origin": "local"}, 1)
	if rows[0]["category"] != "capacity" {
		t.Fatal("local capacity not separated")
	}
	for _, v := range e.s.pool.Views(e.s.cfg.Get()) {
		if v.Active != 0 {
			t.Fatal("lease leaked")
		}
	}
}

func TestGoResponseSessionSurvivesContinuationAndCheckpoint(t *testing.T) {
	e := newGoGatewayEnv(t)
	var mu sync.Mutex
	var sessions []string
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sessions = append(sessions, r.Header.Get("X-Opencode-Session"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, ocReply(opencode.Responses))
	}
	if st, b, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/responses", ocRequest(opencode.Responses, "responses", false), map[string]string{"Session_id": "original-codex-session"}); st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	if err := e.s.saveMetadata(true); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"config.json", "metadata.json"} {
		b, err := os.ReadFile(filepath.Join(e.s.dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.stopPerch(); restored.logs.Close() })
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"responses","previous_response_id":"reply-1","input":"next"}`))
	req.Header.Set("Authorization", "Bearer downstream-opencode-go")
	w := httptest.NewRecorder()
	restored.Handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("restored chain %d %s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sessions) != 2 || sessions[0] == "" || sessions[0] != sessions[1] {
		t.Fatal("response continuation lost upstream session")
	}
}

func TestGoEgressPinCoalescesAndBlocksDriftBeforeCredentials(t *testing.T) {
	e := newGoGatewayEnv(t)
	var calls atomic.Int32
	var ip atomic.Value
	ip.Store("203.0.113.7")
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Api-Key") != "" {
			t.Error("IP probe leaked credentials")
		}
		fmt.Fprintf(w, `{"ip":%q}`, ip.Load().(string))
	}))
	defer probe.Close()
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.OpenCode.GoEgressCheckURL = probe.URL
		for i := range c.Accounts {
			if c.Accounts[i].Provider == config.ProviderOpenCodeGo {
				c.Accounts[i].EgressIP = "203.0.113.7"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.s.cfg.Get()
	a, _ := cfg.Account("go1")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.s.ensureGoEgress(context.Background(), cfg, a, false); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("checks not coalesced: %d", calls.Load())
	}
	if err = e.s.quota.RefreshNow(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	before := e.upCalls.Load()
	ip.Store("203.0.113.8")
	if e.s.ensureGoEgress(context.Background(), cfg, a, true) == nil {
		t.Fatal("IP drift accepted")
	}
	if e.s.quota.RefreshNow(context.Background(), a) == nil || e.upCalls.Load() != before {
		t.Fatal("metadata contacted upstream after failed pin")
	}
	// go2 has no fresh catalog after the pin change, so go1 is the selected account.
	st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), nil)
	if st != 503 || e.count() != 0 {
		t.Fatalf("inference passed failed pin: %d", st)
	}
	if v := e.s.pool.Views(cfg)[a.ID]; v.Active != 0 || v.RPM != 0 || v.Samples != 0 {
		t.Fatal("failed pin spent inference capacity or health")
	}
	if e.s.goEgressView(cfg, a).Status != "mismatch" {
		t.Fatal("drift not observable")
	}
	rows := goLogRows(t, e.s, "errors", map[string]string{"provider": config.ProviderOpenCodeGo, "origin": "local"}, 1)
	if rows[0]["category"] != "egress" {
		t.Fatal("egress error not classified locally")
	}
}

func TestGoPolicyRefusalPausesFollowingRequests(t *testing.T) {
	e := newGoGatewayEnv(t)
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"error":{"message":"abusive traffic from this IP address"}}`)
	}
	h := map[string]string{"X-Session-Id": "policy-session"}
	for i := 0; i < 2; i++ {
		st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), h)
		if i == 0 && st != 403 {
			t.Fatalf("policy status %d", st)
		}
	}
	// Explicit network abuse pauses peers sharing the known fixed route,
	// so a following request cannot walk the same exit through another key.
	v := e.s.pool.Views(e.s.cfg.Get())["go1"]
	if v.State == nil || v.State.RuleID != "opencode-go-policy-refusal" || v.Samples != 0 {
		t.Fatal("policy pause missing or blamed on health")
	}
	if e.count() != 1 {
		t.Fatal("policy refusal was retried through a shared egress")
	}
	before := e.upCalls.Load()
	a, _ := e.s.cfg.Get().Account("go1")
	if e.s.quota.RefreshNow(context.Background(), a) == nil || e.upCalls.Load() != before {
		t.Fatal("metadata queried a policy-paused account")
	}
}

func TestGoOverviewIsProviderScopedAndShowsUnknownCoverage(t *testing.T) {
	e := newGoGatewayEnv(t)
	cfg := e.s.cfg.Get()
	e.s.pool.Bind(cfg, "zen-response", "zen1")
	e.s.pool.Bind(cfg, "go-response", "go1")
	e.s.pool.RememberCache(cfg, "cache-test", "go1", "chat")
	r := httptest.NewRequest("GET", "/api/overview?provider=opencode-go", nil)
	w := httptest.NewRecorder()
	e.s.apiOverview(w, r)
	var got struct {
		Accounts, Bindings, ClientKeys int
		Go                             goOverview `json:"go"`
	}
	if json.Unmarshal(w.Body.Bytes(), &got) != nil {
		t.Fatal("invalid overview")
	}
	if got.Accounts != 2 || got.Bindings != 1 || got.ClientKeys != 1 || got.Go.Bindings.Cache != 1 || got.Go.QuotaFresh != 2 {
		t.Fatalf("wrong scope: %+v", got)
	}
	if got.Go.Egress["configured-only"] != 2 {
		t.Fatal("unverified routes falsely marked static")
	}
}

func TestGoDetailCaptureRedactsIdentityWithoutChangingNativeCache(t *testing.T) {
	e := newGoGatewayEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Logs.Detail = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"messages","metadata":{"user_id":"user_account_session_abcdef12-1234-1234-1234-123456789abc"},"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}],"max_tokens":5}`
	st, b, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/messages", body, map[string]string{"X-Session-Id": "private-header-session", "X-Prompt-Cache-Key": "private-cache-key"})
	if st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	e.mu.Lock()
	sent := string(e.bodies[0])
	e.mu.Unlock()
	if sent != body {
		t.Fatal("cache_control/native body mutated")
	}
	rows := goLogRows(t, e.s, "details", map[string]string{"provider": config.ProviderOpenCodeGo}, 1)
	raw, _ := json.Marshal(rows)
	for _, secret := range []string{"private-header-session", "private-cache-key", "abcdef12-1234-1234-1234-123456789abc"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("identity leaked in detail capture")
		}
	}
	if goUserAgent("Go-http-client/1.1") != "account-pool-balancer/go-gateway" || goUserAgent("claude-code/real") != "claude-code/real" {
		t.Fatal("UA normalization wrong")
	}
}
