package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/rules"
)

// Exercise HTTP -> production matcher/evaluator -> scheduler, not a test-only
// matcher: a retry stop must still hold after a cooldown/quarantine is applied.
func TestALTAPIOfficialErrorPolicy(t *testing.T) {
	const modelError = `{"error":{"message":"The model does not exist or you do not have access to it","type":"invalid_request_error"}}`
	for _, tc := range []struct {
		name, body, rule, state   string
		status, calls, downstream int
		headers                   map[string]string
	}{
		{"official key", `{"error":{"message":"Invalid API key provided","type":"authentication_error"}}`, "altapi-token-invalid", "quarantined", 401, 1, 502, nil},
		{"HTTP200 key", `{"error":{"message":"Invalid API key provided","type":"authentication_error"}}`, "altapi-token-invalid", "quarantined", 200, 1, 502, nil},
		{"measured token", `{"error":{"message":"Invalid token","type":"new_api_error"}}`, "altapi-token-invalid", "quarantined", 200, 1, 502, nil},
		{"400 model", modelError, "altapi-model-forbidden", "ok", 400, 2, 200, nil},
		{"403 model", modelError, "altapi-model-forbidden", "ok", 403, 2, 200, nil},
		{"403 model CF transit", modelError, "altapi-model-forbidden", "ok", 403, 2, 200, map[string]string{"CF-Ray": "mock-ray"}},
		{"200 model", modelError, "altapi-model-forbidden", "ok", 200, 2, 200, nil},
		{"unknown model", `{"error":{"message":"model_not_found"}}`, "altapi-model-unknown", "ok", 404, 1, 404, nil},
		{"bad request", `{"error":{"message":"messages is required","type":"invalid_request_error"}}`, "altapi-bad-request", "ok", 400, 1, 400, nil},
		{"429", `{"error":{"message":"Too many requests","type":"rate_limit_error"}}`, "altapi-rate-limit", "cooldown", 429, 1, 429, map[string]string{"Retry-After": "120"}},
		{"429 empty", "", "altapi-rate-limit", "cooldown", 429, 1, 429, nil},
		{"429 empty CF transit", "", "altapi-rate-limit", "cooldown", 429, 1, 429, map[string]string{"CF-Ray": "mock-ray"}},
		{"403 CF challenge", "<html>challenge</html>", "altapi-cf-block", "cooldown", 403, 1, 503, map[string]string{"CF-Ray": "mock-ray", "Cf-Mitigated": "challenge"}},
		{"429 CF challenge", "<html>challenge</html>", "altapi-cf-block", "cooldown", 429, 1, 503, map[string]string{"Cf-Mitigated": "challenge"}},
		{"quota", `{"error":{"message":"Insufficient quota","type":"insufficient_quota"}}`, "altapi-insufficient-quota", "cooldown", 403, 2, 200, nil},
		{"server", `{"error":{"message":"Internal server error"}}`, "altapi-server-error", "cooldown", 503, 2, 200, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newALTAPIStickyEnv(t)
			cfg := e.srv.cfg.Get()
			var firstKey string
			e.up.mu.Lock()
			e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
				e.up.mu.Lock()
				if firstKey == "" {
					firstKey = key
				}
				fail := key == firstKey
				e.up.mu.Unlock()
				if !fail {
					writeOK(w, key)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}
			e.up.mu.Unlock()
			resp := e.post("/v1/chat/completions", chat(false), nil)
			out := readAll(resp)
			if resp.StatusCode != tc.downstream || e.up.callCount() != tc.calls {
				t.Fatalf("status=%d calls=%d, want status=%d calls=%d: %s", resp.StatusCode, e.up.callCount(), tc.downstream, tc.calls, out)
			}
			page, err := e.srv.logs.Query("errors", nil, time.Time{}, time.Time{}, 20, 0)
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("error log: %v rows=%d", err, len(page.Items))
			}
			var log errorLog
			if err := json.Unmarshal(page.Items[0], &log); err != nil {
				t.Fatal(err)
			}
			view := e.srv.pool.Views(cfg)[log.AccountID]
			if view.Status != tc.state {
				t.Fatalf("state=%s, want %s", view.Status, tc.state)
			}
			if log.RuleID != tc.rule || log.Switched != (tc.calls == 2) {
				t.Fatalf("wrong rule/retry: %+v", log)
			}
			if tc.rule == "altapi-model-forbidden" && log.RuleScope != "model:model" {
				t.Fatalf("model refusal cooled account: %s", log.RuleScope)
			}
			if tc.calls == 1 && log.RetryDecision == "overridden-by-error-rule" {
				t.Fatal("explicit stop overridden")
			}
			if tc.name == "429" && (resp.Header.Get("Retry-After") != "120" || view.State.CooldownUntil.Before(time.Now().Add(119*time.Second))) {
				t.Fatal("Retry-After lost")
			}
			if tc.rule == "altapi-bad-request" && view.Samples != 0 {
				t.Fatal("request error penalized account health")
			}
		})
	}
}

func TestALTAPICommittedStreamDoesNotReplay(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	e.up.mu.Lock()
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"already delivered\"}}]}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"Too many requests\",\"type\":\"rate_limit_error\"}}\n\n")
		w.(http.Flusher).Flush()
	}
	e.up.mu.Unlock()
	resp := e.post("/v1/chat/completions", chat(true), nil)
	out := readAll(resp)
	if resp.StatusCode != 200 || !strings.Contains(out, "already delivered") || e.up.callCount() != 1 {
		t.Fatalf("committed output was lost or replayed: status=%d calls=%d", resp.StatusCode, e.up.callCount())
	}
}

func TestALTAPICustomStopAndRulePersistence(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	before := e.srv.cfg.Get().Clone()
	w := deployAdmin(t, &deployTestEnv{s: e.srv}, "GET", "/api/settings?provider=altapi", nil)
	var doc struct {
		Settings settingsDoc `json:"settings"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &doc) != nil {
		t.Fatal("settings GET failed")
	}
	doc.Settings.ErrorRules = []rules.ErrorRule{{ID: "custom-alt-cooldown", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, When: rules.Match{Statuses: []int{418}}, Reset: &rules.Reset{Fallback: "2m"}}}
	doc.Settings.RetryRules = []rules.RetryRule{{ID: "custom-alt-stop", Decision: rules.DecisionStop, When: rules.Match{Statuses: []int{418}}}}
	w = deployAdmin(t, &deployTestEnv{s: e.srv}, "PUT", "/api/settings?provider=altapi", doc.Settings)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	after := e.srv.cfg.Get().Clone()
	loaded, err := config.Load(filepath.Join(e.srv.dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.ALTAPIErrorRules, loaded.Get().ALTAPIErrorRules) || !reflect.DeepEqual(after.ALTAPIRetryRules, loaded.Get().ALTAPIRetryRules) {
		t.Fatal("custom rules not persisted")
	}
	for _, provider := range []string{config.ProviderOpenDesign, config.ProviderRiver, config.ProviderDeploy, config.ProviderX50, config.ProviderMistral} {
		x, y := before.RulesFor(provider)
		a, b := after.RulesFor(provider)
		if !reflect.DeepEqual(x, a) || !reflect.DeepEqual(y, b) {
			t.Fatalf("changed other pool %s", provider)
		}
	}
	e.up.mu.Lock()
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		w.WriteHeader(418)
		fmt.Fprint(w, `{"error":{"message":"custom"}}`)
	}
	e.up.mu.Unlock()
	_ = readAll(e.post("/v1/chat/completions", chat(false), nil))
	cooled := 0
	for _, view := range e.srv.pool.Views(after) {
		if view.Status == "cooldown" {
			cooled++
		}
	}
	if e.up.callCount() != 1 || cooled != 1 {
		t.Fatal("custom cooldown must apply without overriding custom stop")
	}
}
