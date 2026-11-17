package server

import (
	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/rules"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFactoryAccountCRUDImportAndKeyIsolation(t *testing.T) {
	e := newFactoryEnv(t)
	w := deployAdmin(t, e, "POST", "/api/accounts/import", map[string]any{"provider": "factory", "text": "fk-mock_new_factory_key,New account"})
	// Whitespace separates fields in the existing importer; labels with spaces
	// are edited through CRUD. Duplicate keys are skipped, not recreated.
	if w.Code != 200 {
		t.Fatal("import", w.Code, w.Body.String())
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/import", map[string]any{"provider": "factory", "text": "fk-mock_new_factory_key,New"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unchanged":1`) {
		t.Fatal("dedupe", w.Code, w.Body.String())
	}
	var added config.Account
	for _, a := range e.s.cfg.Get().Accounts {
		if a.RuntimeKey == "fk-mock_new_factory_key" {
			added = a
		}
	}
	if added.ID == "" || added.Group != "factory" || added.Provider != "factory" {
		t.Fatal("wrong ownership")
	}
	w = deployAdmin(t, e, "PUT", "/api/accounts/"+added.ID, map[string]any{"name": "Edited", "enabled": false, "maxConcurrent": 3, "maxRpm": 4, "weight": 2, "priority": 10})
	if w.Code != 200 {
		t.Fatal("edit")
	}
	a, _ := e.s.cfg.Get().Account(added.ID)
	if a.Enabled || a.Name != "Edited" || a.MaxConc != 3 || a.MaxRPM != 4 || a.Weight != 2 || a.Priority != 10 {
		t.Fatal("CRUD lost fields")
	}
	before := len(e.s.cfg.Get().Accounts)
	for _, text := range []string{"provider:deploy opaque_foreign_key", "sk-amr-foreign_key"} {
		w = deployAdmin(t, e, "POST", "/api/accounts/import", map[string]any{"provider": "factory", "text": text})
		if w.Code == 200 || len(e.s.cfg.Get().Accounts) != before {
			t.Fatal("cross-pool import accepted")
		}
	}
	w = deployAdmin(t, e, "POST", "/api/client-keys?provider=factory", map[string]any{"provider": "factory", "group": "factory", "name": "Factory client"})
	if w.Code != 200 {
		t.Fatal("client key")
	}
	var key struct {
		Key string `json:"key"`
	}
	json.Unmarshal(w.Body.Bytes(), &key)
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "configured-reference-not-entitlement") {
		t.Fatal("reference catalogue missing")
	}
	w = deployAdmin(t, e, "POST", "/api/import-keys?provider=factory", map[string]any{"provider": "factory", "group": "factory", "name": "Factory import"})
	if w.Code != 200 {
		t.Fatal("import key")
	}
	json.Unmarshal(w.Body.Bytes(), &key)
	body, _ := json.Marshal(map[string]any{"provider": "deploy", "runtimeKey": "opaque_foreign_secret"})
	req = httptest.NewRequest("POST", "/open/v1/accounts/import", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key.Key)
	rec = httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code == 200 || len(e.s.cfg.Get().Accounts) != before {
		t.Fatal("import credential crossed pool")
	}
	w = deployAdmin(t, e, "DELETE", "/api/accounts/"+added.ID, nil)
	if w.Code != 200 {
		t.Fatal("delete")
	}
}
func TestFactoryCustomRulesAndRateStopActuallyRun(t *testing.T) {
	for _, status := range []int{418, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			e := newFactoryEnv(t)
			if status == 418 {
				_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
					c.FactoryErrorRules = []rules.ErrorRule{{ID: "custom", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, When: rules.Match{Statuses: []int{418}, Headers: map[string]string{"X-Factory-Test": "yes"}}, Reset: &rules.Reset{Fallback: "2m"}}}
					c.FactoryRetryRules = []rules.RetryRule{{ID: "stop", Decision: rules.DecisionStop, When: rules.Match{Statuses: []int{418}}}}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "172800")
				w.Header().Set("X-Factory-Test", "yes")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"detail":"mock rejection"}`)
			}
			e.mu.Unlock()
			rsp, _ := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash"}`, nil)
			if len(e.snapshot()) != 1 || e.s.pool.Views(e.s.cfg.Get())["f1"].Status != "cooldown" {
				t.Fatal("editable rule not enforced")
			}
			if status == 429 && rsp.StatusCode != 429 {
				t.Fatal("rate status lost")
			}
		})
	}
}
