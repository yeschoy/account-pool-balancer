package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/pool"
	"account-pool-balancer/internal/quota"
	"account-pool-balancer/internal/thregister"
)

type thCall struct {
	key, path, body string
	header          http.Header
}
type thEnv struct {
	s            *Server
	front, up    *httptest.Server
	mu           sync.Mutex
	calls        []thCall
	handler      http.HandlerFunc
	discardCalls bool
}

func newTHEnv(t testing.TB) *thEnv {
	t.Helper()
	e := &thEnv{}
	e.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		e.mu.Lock()
		if !e.discardCalls {
			e.calls = append(e.calls, thCall{key: r.Header.Get("Authorization"), path: r.URL.Path, body: string(b), header: r.Header.Clone()})
		}
		h := e.handler
		e.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/messages" {
			fmt.Fprint(w, `{"type":"message","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":2}}`)
		} else if r.URL.Path == "/v1/responses" {
			fmt.Fprint(w, `{"id":"resp-th","object":"response","output":[],"usage":{"input_tokens":15,"output_tokens":2,"input_tokens_details":{"cached_tokens":5}}}`)
		} else {
			fmt.Fprint(w, ocReply(opencode.Chat))
		}
	}))
	t.Cleanup(e.up.Close)
	var err error
	e.s, err = New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.s.stopPerch(); e.s.logs.Close() })
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Quota.Enabled = false
		c.TokenHarbor.BaseURL = e.up.URL + "/v1"
		c.Routing.Strategy = "ordered"
		c.Routing.ConcurrencyWaitMs = 0
		c.Accounts = []config.Account{{ID: "th1", RuntimeKey: "thk_live_mock-secret-1", Enabled: true, Priority: 1, MaxConc: 1}, {ID: "th2", RuntimeKey: "thk_live_mock-secret-2", Enabled: true, Priority: 2, MaxConc: 1}}
		c.ClientKeys = []config.ClientKey{{ID: "client", Provider: config.ProviderTokenHarbor, Group: config.TokenHarborGroup, KeyHash: config.HashKey("downstream-th"), Enabled: true}, {ID: "other", Provider: config.ProviderTokenHarbor, Group: config.TokenHarborGroup, KeyHash: config.HashKey("downstream-other"), Enabled: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	seedTHCapabilities(e.s)
	e.front = httptest.NewServer(e.s.Handler())
	t.Cleanup(e.front.Close)
	for !e.s.logs.Ready() {
		time.Sleep(time.Millisecond)
	}
	return e
}

// Existing native protocol tests require verified fake entitlement, not a
// bypass of the production fail-closed gate.
func seedTHCapabilities(s *Server) {
	cfg, now := s.cfg.Get(), time.Now()
	for _, a := range cfg.Accounts {
		if a.Provider != config.ProviderTokenHarbor {
			continue
		}
		used := 0.0
		source := quota.ResponseBindingSource(cfg, a)
		s.quota.Load(map[string]*quota.Snapshot{a.ID: {Catalog: map[string]bool{"m": true}, CatalogAt: now, CatalogSource: source, TokenHarbor: &quota.TokenHarborState{Source: source, Owner: a.ID, Plan: thregister.Subscription{Status: "active", Tier: "plus", Name: "Agent Pass", CheckedAt: now}, Usage: thregister.PassUsage{OverageBilling: new(bool), UsedPercent: &used, Activated: true, ResetsAt: now.Add(time.Hour), CheckedAt: now}, CheckedAt: now, ValidUntil: now.Add(10 * time.Minute)}}})
	}
}

func (e *thEnv) post(t *testing.T, path, body string, headers map[string]string) (int, string, http.Header) {
	t.Helper()
	r, _ := http.NewRequest(http.MethodPost, e.front.URL+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer downstream-th")
	r.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	resp, err := e.front.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}
func (e *thEnv) count() int                    { e.mu.Lock(); defer e.mu.Unlock(); return len(e.calls) }
func (e *thEnv) setHandler(h http.HandlerFunc) { e.mu.Lock(); e.handler = h; e.mu.Unlock() }
func (e *thEnv) call(i int) thCall             { e.mu.Lock(); defer e.mu.Unlock(); return e.calls[i] }

func TestTokenHarborNativeBytesHeadersAndLogs(t *testing.T) {
	for _, p := range []opencode.Protocol{opencode.Chat, opencode.Responses, opencode.Messages} {
		t.Run(string(p), func(t *testing.T) {
			e := newTHEnv(t)
			body := ocRequest(p, "m", false)
			body = strings.TrimSuffix(body, "}") + `, "prompt_cache_key":"private-cache", "metadata":{"user_id":"{\"session_id\":\"private-session\"}"}, "tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}},"cache_control":{"type":"ephemeral"}}] }`
			status, _, _ := e.post(t, "/v1"+opencode.Path(p), body, map[string]string{"Session_id": "private-session", "X-Prompt-Cache-Key": "private-cache", "Anthropic-Beta": "prompt-caching-test", "X-Th-Confirm-Spend": "true", "X-Api-Key": "downstream-must-not-leak"})
			if status != 200 {
				t.Fatalf("status %d", status)
			}
			c := e.call(0)
			if c.body != body || c.key != "Bearer thk_live_mock-secret-1" || c.header.Get("X-Api-Key") != "" || c.header.Get("X-Th-Confirm-Spend") != "true" || headerValue(c.header, "Session_id") != "private-session" || c.header.Get("X-Prompt-Cache-Key") != "private-cache" || c.header.Get("Anthropic-Beta") != "prompt-caching-test" {
				t.Fatal("native body/header contract violated")
			}
			if p == opencode.Messages && c.header.Get("Anthropic-Version") == "" {
				t.Fatal("missing Messages version")
			}
			logs, _ := e.s.logs.Query("requests", map[string]string{"provider": config.ProviderTokenHarbor}, time.Time{}, time.Time{}, 10, 0)
			if len(logs.Items) != 1 {
				t.Fatal("missing request log")
			}
			text := string(logs.Items[0])
			for _, secret := range []string{"private-session", "private-cache", "thk_live_mock-secret", "downstream-th", "lookup"} {
				if strings.Contains(text, secret) {
					t.Fatal("request log leaks raw identity/body/secret")
				}
			}
			var log requestLog
			_ = json.Unmarshal(logs.Items[0], &log)
			if log.AffinityConfidence != "explicit" || log.Provider != config.ProviderTokenHarbor {
				t.Fatal("identity/provider not logged")
			}
			if p == opencode.Messages {
				if log.CacheCreationTokens == nil || *log.CacheCreationTokens != 30 || log.CacheRate == nil || *log.CacheRate != 1.0/3 {
					t.Fatalf("bad cache accounting: %s", text)
				}
				st := e.s.stats.SummaryFor(config.ProviderTokenHarbor).Total
				if st.Prompt != 60 || st.Cached != 20 || st.CacheCreation != 30 {
					t.Fatalf("bad overview %+v", st)
				}
			}
		})
	}
}

func TestTokenHarborSessionAndCacheAffinity(t *testing.T) {
	for _, headers := range []map[string]string{{"Session_id": "codex-s"}, {"X-Codex-Thread-Id": "codex-thread"}, {"X-Claude-Code-Session-Id": "claude-s"}, {"X-Opencode-Session-Id": "opencode-s"}, {"X-Opencode-Parent-Session-Id": "opencode-parent"}, {"X-Session-Id": "pi-openrouter-session", "User-Agent": "pi/1.0"}, {"X-Session-Affinity": "pi-messages-session", "User-Agent": "pi/1.0"}, {"X-Prompt-Cache-Key": "cache-header"}} {
		t.Run(fmt.Sprint(headers), func(t *testing.T) {
			e := newTHEnv(t)
			// Equal priority + least-selections makes losing affinity observable.
			_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				c.Routing.Strategy = "least-selections"
				for i := range c.Accounts {
					c.Accounts[i].Priority = 50
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				status, b, _ := e.post(t, "/v1/chat/completions", fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"turn %d"}]}`, i), headers)
				if status != 200 {
					t.Fatalf("%d %s", status, b)
				}
			}
			for i := 1; i < 3; i++ {
				if e.call(i).key != e.call(0).key {
					t.Fatal("affinity lost across turns")
				}
			}
		})
	}
	e := newTHEnv(t)
	cfg := e.s.cfg.Get()
	h := http.Header{"Session_id": []string{"one-session"}}
	a := e.s.tokenHarborIdentity(cfg, "scope", h, map[string]any{"prompt_cache_key": "prefix-a"})
	b := e.s.tokenHarborIdentity(cfg, "scope", h, map[string]any{"prompt_cache_key": "prefix-b"})
	if a.Fingerprint != b.Fingerprint || a.KeyType != "session_id" {
		t.Fatal("cache prefix overrode session")
	}
	claude := map[string]any{"metadata": map[string]any{"user_id": `{"session_id":"claude-session"}`}}
	if id := e.s.tokenHarborIdentity(cfg, "scope", nil, claude); id.KeyType != "claude_user_session" {
		t.Fatal("Claude metadata not detected")
	}
	for _, body := range []map[string]any{{"prompt_cache_key": "body-cache"}, {"metadata": map[string]any{"user_id": "user_h_account_a_session_12345678-abcd"}}} {
		if id := e.s.tokenHarborIdentity(cfg, "scope", nil, body); id.Confidence != "explicit" {
			t.Fatal("body affinity missing")
		}
	}
	if a.Fingerprint == e.s.tokenHarborIdentity(cfg, "different-client", h, nil).Fingerprint {
		t.Fatal("downstream scopes collide")
	}
}

func TestTokenHarborBoundBusyAndStatefulSafety(t *testing.T) {
	e := newTHEnv(t)
	status, _, _ := e.post(t, "/v1/responses", `{"model":"m","input":"hi"}`, map[string]string{"Session_id": "busy"})
	if status != 200 {
		t.Fatal(status)
	}
	cfg := e.s.cfg.Get().ForProvider(config.ProviderTokenHarbor)
	ident := e.s.tokenHarborIdentity(cfg, config.TokenHarborGroup+"\x00client", http.Header{"Session_id": []string{"busy"}}, nil)
	lease, err := e.s.pool.Acquire(context.Background(), cfg, pool.Request{Group: config.TokenHarborGroup, Model: "m", Affinity: ident.Fingerprint, PreserveBinding: true})
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = e.post(t, "/v1/responses", `{"model":"m","input":"hi again"}`, map[string]string{"Session_id": "busy"})
	if status != 429 || e.count() != 1 {
		t.Fatal("busy binding overflowed")
	}
	lease.Release()
	status, _, _ = e.post(t, "/v1/responses", `{"model":"m","input":"next","previous_response_id":"resp-th"}`, nil)
	if status != 200 || e.call(0).key != e.call(1).key {
		t.Fatal("stateful continuation changed account")
	}
	for _, test := range []struct {
		body    string
		headers map[string]string
	}{{`{"model":"m","input":"x","previous_response_id":"missing"}`, nil}, {`{"model":"m","input":"x","previous_response_id":"resp-th"}`, map[string]string{"Authorization": "Bearer downstream-other"}}} {
		before := e.count()
		status, _, _ = e.post(t, "/v1/responses", test.body, test.headers)
		if status != 409 || before != e.count() {
			t.Fatal("unknown/cross-client state guessed an account")
		}
	}
	saved, _ := e.s.pool.ExportState()
	if len(saved.Bindings) == 0 || saved.Bindings[0].Source == "" {
		t.Fatal("confirmed response not persisted")
	}
	restored := pool.New(e.s.quota.View)
	restored.ImportState(saved, map[string]bool{"th1": true, "th2": true})
	e.s.pool = restored
	status, _, _ = e.post(t, "/v1/responses", `{"model":"m","input":"restored","previous_response_id":"resp-th"}`, nil)
	if status != 200 {
		t.Fatal("restored binding unusable")
	}
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error { c.Accounts[0].RuntimeKey = "thk_live_replacement"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	status, _, _ = e.post(t, "/v1/responses", `{"model":"m","input":"x","previous_response_id":"resp-th"}`, nil)
	if status != 409 {
		t.Fatal("binding survived credential change")
	}
}

func TestTokenHarborErrorsAndNoToolStripping(t *testing.T) {
	for _, code := range []int{401, 402, 429, 503, 400, 404, 413, 422} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			e := newTHEnv(t)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "Bearer thk_live_mock-secret-1" {
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Retry-After", "7200")
					w.WriteHeader(code)
					fmt.Fprint(w, `{"error":{"code":"invalid_api_key","message":"[unsupported_tool] unsupported hosted tool type \"web_search\""}}`)
					return
				}
				fmt.Fprint(w, ocReply(opencode.Chat))
			})
			body := `{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"web_search"}]}`
			status, _, _ := e.post(t, "/v1/chat/completions", body, nil)
			if code == 401 || code == 402 || code == 429 || code == 503 {
				if status != 200 || e.count() != 2 {
					t.Fatalf("failure not switched %d count %d", status, e.count())
				}
			} else {
				if status != code || e.count() != 1 {
					t.Fatal("request error replayed or rewritten")
				}
			}
			if e.call(0).body != body {
				t.Fatal("tool request changed")
			}
			page, _ := e.s.logs.Query("errors", map[string]string{"provider": config.ProviderTokenHarbor}, time.Time{}, time.Time{}, 10, 0)
			if len(page.Items) != 1 {
				t.Fatal("missing isolated error log")
			}
			if code == 429 {
				var el errorLog
				_ = json.Unmarshal(page.Items[0], &el)
				if el.RetryAfterMs == nil || *el.RetryAfterMs != 7200000 {
					t.Fatal("Retry-After truncated")
				}
			}
		})
	}
	e := newTHEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(402)
		fmt.Fprint(w, `{"error":{"message":"This call is estimated at about $1, more than half of your $1 balance"}}`)
	})
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		// Even an editable 402 cooldown must not bypass provider spend consent.
		c.TokenHarborErrorRules = c.TokenHarborErrorRules[3:]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	status, message, _ := e.post(t, "/v1/chat/completions", ocRequest(opencode.Chat, "m", false), nil)
	if status != 402 || !strings.Contains(message, "more than half") {
		t.Fatal("spend confirmation message hidden")
	}
	if e.count() != 1 || e.call(0).header.Get("X-Th-Confirm-Spend") != "" {
		t.Fatal("spend confirmation bypassed")
	}
	cfg := e.s.cfg.Get()
	views := e.s.pool.Views(cfg)
	if views["th1"].Status != "ok" {
		t.Fatal("confirmation blamed account")
	}
}

func TestTokenHarborNativeStreamsAndUnsupportedPaths(t *testing.T) {
	for _, p := range []opencode.Protocol{opencode.Chat, opencode.Messages, opencode.Responses} {
		t.Run(string(p), func(t *testing.T) {
			e := newTHEnv(t)
			reply := ocStream(p)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, reply)
			})
			status, b, _ := e.post(t, "/v1"+opencode.Path(p), ocRequest(p, "m", true), nil)
			if status != 200 || b != reply {
				t.Fatal("native SSE was transformed")
			}
			for _, path := range []string{"/v1/embeddings", "/v1/completions", "/v1/responses/compact", "/v1/messages/count_tokens", "/v1/images/generations"} {
				before := e.count()
				status, _, _ = e.post(t, path, `{"model":"m"}`, nil)
				if status != 404 || e.count() != before {
					t.Fatal("unsupported path reached upstream")
				}
			}
		})
	}
}

func TestTokenHarborSettingsAndImportIsolation(t *testing.T) {
	e := newTHEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.CaptchaKey = "solver-secret-not-for-console"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	e.s.apiSettings(w, httptest.NewRequest("GET", "/api/settings?provider=tokenharbor", nil))
	if strings.Contains(w.Body.String(), "solver-secret") || strings.Contains(w.Body.String(), "captchaKey") {
		t.Fatal("registration credential leaked")
	}
	var doc struct {
		Settings settingsDoc `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	before := e.s.cfg.Get().Clone()
	doc.Settings.Routing.Strategy = "roundrobin"
	doc.Settings.TokenHarbor.ModelSyncSec = 600
	body, _ := json.Marshal(doc.Settings)
	w = httptest.NewRecorder()
	e.s.apiSaveSettings(w, httptest.NewRequest("PUT", "/api/settings?provider=tokenharbor", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	after := e.s.cfg.Get()
	if after.Routing.Strategy != before.Routing.Strategy || after.ForProvider(config.ProviderTokenHarbor).Routing.Strategy != "roundrobin" || after.TokenHarbor.CaptchaKey != before.TokenHarbor.CaptchaKey || after.TokenHarbor.ModelSyncSec != 600 {
		t.Fatal("settings clobbered shared/secret state")
	}
	if len(after.ErrorRules) != len(before.ErrorRules) || len(after.TokenHarborErrorRules) != len(before.TokenHarborErrorRules) {
		t.Fatal("rules crossed pools")
	}
	w = httptest.NewRecorder()
	e.s.apiImportAccounts(w, httptest.NewRequest("POST", "/api/accounts/import", strings.NewReader(`{"provider":"tokenharbor","text":"thk_live_new-mock,new\nthk_live_new-mock,new"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"added":1`) || !strings.Contains(w.Body.String(), `"unchanged":1`) {
		t.Fatalf("batch import %d %s", w.Code, w.Body)
	}
	ik := config.ImportKey{Provider: config.ProviderTokenHarbor, Group: config.TokenHarborGroup}
	if result := e.s.importOne(ik, importItem{RuntimeKey: "thk_live_open-mock"}, time.Now()); result.Status != "added" {
		t.Fatalf("open import %+v", result)
	}
	if result := e.s.importOne(ik, importItem{RuntimeKey: "sk-amr-wrong-pool"}, time.Now()); result.Status != "error" {
		t.Fatal("import crossed provider")
	}
	r := httptest.NewRequest("GET", "/api/accounts/th1/keys", nil)
	r.SetPathValue("id", "th1")
	w = httptest.NewRecorder()
	e.s.apiAccountKeys(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "thk_live_mock-secret-1") {
		t.Fatal("administrator cannot read saved upstream credential")
	}
	req, _ := http.NewRequest("GET", e.front.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer downstream-th")
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b := readAll(resp)
	if resp.StatusCode != 200 || !strings.Contains(b, `"id":"m"`) {
		t.Fatalf("models %d %s", resp.StatusCode, b)
	}
}
