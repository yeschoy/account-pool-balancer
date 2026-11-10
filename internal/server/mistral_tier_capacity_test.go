package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/rules"
)

func TestMistralTierDenialOnlyCoolsAffectedModel(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer mstrl_mock_secret_1" && strings.Contains(e.call(e.count()-1).body, `"model":"denied-model"`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			fmt.Fprint(w, `{"code":"1910","type":"tier_not_allowed","message":"Model not included in subscription tier"}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	})
	headers := map[string]string{"X-Pi-Session-Id": "model-tier-session"}
	body := `{"model":"denied-model","messages":[]}`
	if st, b := e.post(t, "/v1/chat/completions", body, headers); st != 200 || e.count() != 2 {
		t.Fatalf("tier denial did not fail over once: status=%d calls=%d reply=%s", st, e.count(), b)
	}
	views := e.s.pool.Views(e.s.cfg.Get())
	v := views["m1"]
	if v.State == nil || v.State.HardQuarantined || v.State.CooldownUntil.After(time.Now()) || v.Samples != 0 {
		t.Fatalf("tier denial penalized the whole account: %+v", v)
	}
	ms := v.State.Models["denied-model"]
	if ms == nil || ms.RuleID != "mistral-tier-not-allowed" || ms.CooldownUntil.Before(time.Now().Add(23*time.Hour)) {
		t.Fatalf("missing bounded model qualification cooldown: %+v", ms)
	}
	if st, b := e.post(t, "/v1/chat/completions", body, headers); st != 200 || e.count() != 3 || e.call(2).key != "Bearer mstrl_mock_secret_2" {
		t.Fatalf("cooled model reused denied account: status=%d calls=%d reply=%s", st, e.count(), b)
	}
	if st, b := e.post(t, "/v1/chat/completions", `{"model":"other-model","messages":[]}`, nil); st != 200 || e.call(3).key != "Bearer mstrl_mock_secret_1" {
		t.Fatalf("model cooldown leaked to another model: status=%d reply=%s", st, b)
	}
	page, _ := e.s.logs.Query("errors", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
	if len(page.Items) != 1 {
		t.Fatalf("unexpected error count: %d", len(page.Items))
	}
	var rec errorLog
	if err := json.Unmarshal(page.Items[0], &rec); err != nil {
		t.Fatal(err)
	}
	if rec.RuleID != "mistral-tier-not-allowed" || rec.RuleScope != "model:model" || rec.HealthAction != "none" || !rec.Switched || rec.RetryDecision != "overridden-by-error-rule" {
		t.Fatalf("wrong model-only decision: %s", page.Items[0])
	}
	// Existing operator recovery clears model exclusions without changing configuration.
	e.s.pool.Recover("m1")
	if e.s.pool.Views(e.s.cfg.Get())["m1"].State != nil {
		t.Fatal("operator recovery did not clear model qualification cooldown")
	}
}

func TestMistralTierDenialExhaustedReturns503(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		fmt.Fprint(w, `{"code":1910,"type":"tier_not_allowed"}`)
	})
	st, b := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil)
	if st != 503 || e.count() != 2 || !strings.Contains(b, "mistral_tier_not_allowed") {
		t.Fatalf("tier failure leaked channel-disabling 403 or unbounded retry: status=%d calls=%d reply=%s", st, e.count(), b)
	}
	for _, v := range e.s.pool.Views(e.s.cfg.Get()) {
		if v.Samples != 0 || v.State == nil || v.State.HardQuarantined || v.State.CooldownUntil.After(time.Now()) {
			t.Fatal("model qualification refusal penalized account")
		}
	}
}

func TestMistralBackendCapacityStopsAndPreservesRetryAfter(t *testing.T) {
	for _, hint := range []string{"45", time.Now().Add(2 * time.Minute).UTC().Format(http.TimeFormat), ""} {
		t.Run("hint="+hint, func(t *testing.T) {
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if hint != "" {
					w.Header().Set("Retry-After", hint)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(429)
				fmt.Fprint(w, `{"code":"3505","type":"backend_out_of_capacity"}`)
			})
			r, _ := http.NewRequest(http.MethodPost, e.front.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
			r.Header.Set("Authorization", "Bearer mistral-client")
			r.Header.Set("Content-Type", "application/json")
			resp, err := e.front.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			reply, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 429 || resp.Header.Get("Retry-After") != hint || e.count() != 1 || !strings.Contains(string(reply), "mistral_backend_out_of_capacity") {
				t.Fatalf("capacity failure rotated accounts or lost classification/Retry-After: status=%d hint=%q calls=%d reply=%s", resp.StatusCode, resp.Header.Get("Retry-After"), e.count(), reply)
			}
			for _, v := range e.s.pool.Views(e.s.cfg.Get()) {
				if v.Samples != 0 || v.Status != "ok" {
					t.Fatalf("service capacity penalized an account: %+v", v)
				}
			}
			page, _ := e.s.logs.Query("errors", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
			if len(page.Items) != 1 {
				t.Fatalf("unexpected error count: %d", len(page.Items))
			}
			var rec errorLog
			if json.Unmarshal(page.Items[0], &rec) != nil || rec.RuleID != "mistral-backend-out-of-capacity" || rec.HealthAction != "none" || rec.Switched || rec.RetryDecision != "stop" {
				t.Fatalf("wrong capacity decision: %s", page.Items[0])
			}
			// There is no account/model-wide outage cache or new scheduler.
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
			})
			if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 200 || e.call(1).key != e.call(0).key {
				t.Fatal("service capacity poisoned later account eligibility")
			}
		})
	}
}

func TestMistralOrdinaryRateLimitStillCoolsAndSwitches(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer mstrl_mock_secret_1" {
			w.Header().Set("Retry-After", "45")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"code":1300,"type":"rate_limited"}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	})
	if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 200 || e.count() != 2 {
		t.Fatalf("ordinary account rate limit stopped switching: status=%d calls=%d", st, e.count())
	}
	state := e.s.pool.Views(e.s.cfg.Get())["m1"].State
	if state == nil || state.RuleID != "mistral-rate-limited" || state.CooldownUntil.Before(time.Now().Add(40*time.Second)) {
		t.Fatal("ordinary rate limit lost Retry-After account cooldown")
	}
}

func TestMistralCustomCapacityRuleRemainsAuthoritative(t *testing.T) {
	e := newMistralEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.MistralErrorRules = []rules.ErrorRule{{ID: "operator-429", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, When: rules.Match{Statuses: []int{429}}, Reset: &rules.Reset{Fallback: "5s"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		fmt.Fprint(w, `{"code":3505}`)
	})
	if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 429 || e.count() != 2 {
		t.Fatalf("overrode operator cooldown with capacity policy: status=%d calls=%d", st, e.count())
	}
}

func TestMistralOtherForbiddenKeepsExistingPolicy(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		fmt.Fprint(w, `{"code":1911,"message":"different permission error"}`)
	})
	if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 502 || e.count() != 1 {
		t.Fatalf("ordinary forbidden was reclassified/retried: status=%d calls=%d", st, e.count())
	}
	v := e.s.pool.Views(e.s.cfg.Get())["m1"]
	if v.Samples != 1 || v.State != nil {
		t.Fatal("ordinary forbidden acquired tier-model cooldown behavior")
	}
}

func TestMistralCustomTierRuleRemainsAuthoritative(t *testing.T) {
	e := newMistralEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.MistralErrorRules = append([]rules.ErrorRule{{ID: "operator-403", Scope: rules.ScopeAccount, Action: rules.ActionHardQuarantine, When: rules.Match{Statuses: []int{403}}}}, c.MistralErrorRules...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"code":1910}`)
	})
	if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 502 || e.count() != 2 {
		t.Fatalf("overrode saved operator 403 policy: status=%d calls=%d", st, e.count())
	}
	for _, v := range e.s.pool.Views(e.s.cfg.Get()) {
		if v.State == nil || !v.State.HardQuarantined || v.State.RuleID != "operator-403" {
			t.Fatal("saved custom 403 policy did not apply")
		}
	}
}

func TestMistralCapacityRetryHeaderDoesNotAffectOtherRules(t *testing.T) {
	for _, id := range []string{"other-provider-rule", "mistral-backend-out-of-capacity"} {
		w := httptest.NewRecorder()
		s := &Server{}
		// Rule IDs are only unique within a pool. A different provider may
		// legitimately save a rule with the same ID; it is not Mistral.
		r := &attemptResult{status: 429, ruleID: id, terminal: "upstream_http", header: http.Header{"Retry-After": []string{"123"}}}
		if st := s.writeFailure(w, config.Default(), "openai", r, 1); st != 429 || w.Header().Get("Retry-After") != "1" || strings.Contains(w.Body.String(), "mistral_backend_out_of_capacity") {
			t.Fatal("Mistral capacity handling changed unrelated/provider-colliding rules")
		}
	}
}
