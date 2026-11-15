package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/rules"
	"account-pool-balancer/internal/x50signupstore"
)

func TestX50RulesDriveStopRetryCooldownAndLogs(t *testing.T) {
	e := newX50PoolEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.X50.DetailLog = true
		c.X50ErrorRules = []rules.ErrorRule{{ID: "custom-x50", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, When: rules.Match{Statuses: []int{418}, BodyContains: []string{"match-me"}, Headers: map[string]string{"X-Mock-Rule": "yes"}}, Reset: &rules.Reset{Fallback: "2m"}}}
		c.X50RetryRules = []rules.RetryRule{{ID: "custom-stop", Decision: rules.DecisionStop, When: rules.Match{Statuses: []int{418}}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Mock-Rule", "yes")
		w.WriteHeader(418)
		fmt.Fprint(w, `{"error":"match-me sk-50x-mock1 mock-session1 private-x50-session"}`)
	}
	e.mu.Unlock()
	e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, map[string]string{"X-Session-Id": "private-x50-session"})
	if len(e.snapshot()) != 1 || e.s.pool.Views(e.s.cfg.Get())["x1"].Status != "cooldown" {
		t.Fatal("custom matching/stop/cooldown ignored")
	}
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error { c.X50RetryRules[0].Disabled = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	e.s.pool.Recover("x1")
	before := len(e.snapshot())
	e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, map[string]string{"X-Session-Id": "private-x50-session"})
	if len(e.snapshot()) != before+2 {
		t.Fatal("disabled stop did not allow bounded retry")
	}
	w := deployAdmin(t, e, "GET", "/api/logs/errors?provider=x50", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "custom-x50") {
		t.Fatal("errors unavailable", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "sk-50x-mock1") || strings.Contains(w.Body.String(), "mock-session1") || strings.Contains(w.Body.String(), "private-x50-session") {
		t.Fatal("credentials in errors")
	}
	// Independently disable all new captures without disabling rule decisions.
	off := false
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error {
		c.X50.RequestLog = &off
		c.X50.ErrorLog = &off
		c.X50.DetailLog = false
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	requestBefore := deployAdmin(t, e, "GET", "/api/logs/requests?provider=x50", nil).Body.String()
	errorBefore := deployAdmin(t, e, "GET", "/api/logs/errors?provider=x50", nil).Body.String()
	e.s.pool.Recover("x1")
	e.s.pool.Recover("x2")
	e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, nil)
	if deployAdmin(t, e, "GET", "/api/logs/requests?provider=x50", nil).Body.String() != requestBefore || deployAdmin(t, e, "GET", "/api/logs/errors?provider=x50", nil).Body.String() != errorBefore {
		t.Fatal("log switches ignored")
	}
}
func TestX50ImportWriteFailureNotImportedAndInvalidCandidatePartial(t *testing.T) {
	e := newX50PoolEnv(t)
	st, _ := e.s.x50Store()
	good, _ := st.Add(x50signupstore.Row{Email: "good@example.test", Status: x50signupstore.Keyed, APIKey: "sk-50x-candidate-good"})
	bad, _ := st.Add(x50signupstore.Row{Email: "bad@example.test", Status: x50signupstore.Keyed, APIKey: "sk-50x-candidate-bad", ProxyURL: "invalid"})
	w := deployAdmin(t, e, "POST", "/api/x50/pool/import", map[string]any{"ids": []string{good.ID, bad.ID}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"added":1`) || !strings.Contains(w.Body.String(), `"error"`) {
		t.Fatal("not partial", w.Body.String())
	}
	fresh, _ := st.Add(x50signupstore.Row{Email: "writefail@example.test", Status: x50signupstore.Keyed, APIKey: "sk-50x-writefail"})
	cfgPath := filepath.Join(e.s.dataDir, "config.json")
	if err := os.Rename(cfgPath, cfgPath+".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfgPath, 0700); err != nil {
		t.Fatal(err)
	}
	w = deployAdmin(t, e, "POST", "/api/x50/pool/import", map[string]any{"ids": []string{fresh.ID}})
	if w.Code < 400 || x50AccountFor(e.s.cfg.Get(), fresh.APIKey) != "" {
		t.Fatal("failed write marked imported", w.Code)
	}
}
func TestX50RegistrationLogRedaction(t *testing.T) {
	e := newX50PoolEnv(t)
	m, _ := e.s.x50Mgr()
	row, _ := m.store.Add(x50signupstore.Row{Email: "fake@example.test", Password: "reg-password-fake", SessionToken: "reg-session-fake", APIKey: "sk-50x-reg-fake", MailboxToken: "mail-token-fake"})
	m.addLog("error", row.Email, "reg-password-fake reg-session-fake sk-50x-reg-fake mail-token-fake token=not-yet-stored")
	m.finish(row.ID, x50signupstore.Failed, "", fmt.Errorf("session_token=new-session-not-stored reg-password-fake"))
	raw, _ := json.Marshal(m.Logs())
	stored, _ := m.store.Get(row.ID)
	for _, v := range []string{"reg-password-fake", "reg-session-fake", "sk-50x-reg-fake", "mail-token-fake", "not-yet-stored", "new-session-not-stored"} {
		if strings.Contains(string(raw), v) || strings.Contains(stored.Error, v) {
			t.Fatal("credential leaked", v)
		}
	}
}
func TestX50UnknownSendResultNeverReplayed(t *testing.T) {
	e := newX50PoolEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}
	e.mu.Unlock()
	e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, nil)
	if len(e.snapshot()) != 1 {
		t.Fatal("uncertain sent request replayed")
	}
}

func TestX50RateAfterAndSelectedDirectTransport(t *testing.T) {
	e := newX50PoolEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "172800")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"error":"rate"}`)
	}
	e.mu.Unlock()
	e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, nil)
	if len(e.snapshot()) != 1 || e.s.pool.Views(e.s.cfg.Get())["x1"].State.CooldownUntil.Before(time.Now().Add(47*time.Hour)) {
		t.Fatal("rate stop/cooldown")
	}
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Accounts[2].ProxyURL = "http://127.0.0.1:1"
		c.Accounts[3].ProxyURL = "http://127.0.0.1:2"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w := deployAdmin(t, e, "POST", "/api/x50/pool/proxy", map[string]any{"scope": "selected", "ids": []string{"x1"}, "proxyUrl": "", "confirm": "x50"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cfg := e.s.cfg.Get()
	x1, _ := cfg.Account("x1")
	x2, _ := cfg.Account("x2")
	if x1.ProxyURL != "" || x2.ProxyURL != "http://127.0.0.1:2" {
		t.Fatal("selected scope")
	}
	tr := e.s.x50HTTPClient(cfg, x1.ProxyURL).Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Fatal("direct still uses proxy or environment proxy")
	}
}
