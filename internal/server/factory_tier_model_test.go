package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/rules"
)

// factoryTierRefusalBody is Factory's measured answer (2026-11-24) when the
// ORG TIER does not carry the body's model ID. A pro_plus org returned it for
// every gemini-4-* request while a team org served the same bodies.
const factoryTierRefusalBody = `{"detail":"Invalid model ID in request body","status":400,"title":"Bad Request","requestId":"yul1::2747z-1795500502168-f68315fa790a"}`

// newFactoryTierEnv serves two Factory accounts where f1's org tier refuses
// gemini-4-* and f2's serves it, and counts calls per account.
func newFactoryTierEnv(t *testing.T) *deployTestEnv {
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/g/v1/generate") {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
			return
		}
		if strings.Contains(r.Header.Get("Authorization"), "fk-mock_factory_key_1") {
			w.WriteHeader(400)
			fmt.Fprint(w, factoryTierRefusalBody)
			return
		}
		fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`)
	}
	e.mu.Unlock()
	return e
}

func factoryGeminiPost(t *testing.T, e *deployTestEnv, model, text string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	body := `{"contents":[{"role":"user","parts":[{"text":"` + text + `"}]}]}`
	req := httptest.NewRequest("POST", "/v1beta/models/"+model+":generateContent", strings.NewReader(body))
	req.Header.Set("X-Goog-Api-Key", "downstream-ms")
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	return rec, rec.Body.String()
}

// A tier that does not carry the model answers Factory's "Invalid model ID in
// request body" — the wording it also uses for a typo. The pool must read it
// as an account×model entitlement gap: cool only that pair, switch accounts
// for this request, keep the account's health and its other models intact.
func TestFactoryTierModelRefusalSwitchesAndCoolsPair(t *testing.T) {
	e := newFactoryTierEnv(t)
	rec, out := factoryGeminiPost(t, e, "gemini-4-neon", "hi")
	if rec.Code != 200 || !strings.Contains(out, `"finishReason":"STOP"`) || out == factoryTierRefusalBody {
		t.Fatalf("request did not fail over: %d %s", rec.Code, out)
	}
	calls := e.snapshot()
	if len(calls) != 2 || !strings.Contains(calls[0].auth, "key_1") || !strings.Contains(calls[1].auth, "key_2") {
		t.Fatalf("calls=%d auth=%q/%q", len(calls), calls[0].auth, calls[len(calls)-1].auth)
	}
	page, err := e.s.logs.Query("errors", nil, time.Time{}, time.Time{}, 20, 0)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("errors: %v rows=%d", err, len(page.Items))
	}
	var el errorLog
	if err := json.Unmarshal(page.Items[0], &el); err != nil {
		t.Fatal(err)
	}
	if el.RuleID != "factory-model-id-rejected" || el.RuleScope != "model:model" || el.RuleAction != rules.ActionCooldown || !el.Switched || el.HealthAction != "none" || el.RetryDecision != "overridden-by-error-rule" {
		t.Fatalf("unexpected error row: %+v", el)
	}
	if el.AccountID != "f1" {
		t.Fatalf("refusal logged against %q", el.AccountID)
	}
	views := e.s.pool.Views(e.s.cfg.Get())
	f1 := views["f1"]
	if f1.Status != "ok" || f1.Samples != 0 || (f1.State != nil && !f1.State.CooldownUntil.IsZero()) {
		t.Fatalf("pair refusal changed account health: %+v", f1)
	}
	if f1.State == nil || f1.State.Models["gemini-4-neon"] == nil {
		t.Fatalf("model pair not cooled: %+v", f1.State)
	}
	// The account still serves its other models.
	e.mu.Lock()
	refuseAll := false
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/g/v1/generate") {
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
			return
		}
		if refuseAll {
			w.WriteHeader(400)
			fmt.Fprint(w, factoryTierRefusalBody)
			return
		}
		fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}]}`)
	}
	e.mu.Unlock()
	before := len(e.snapshot())
	if rec, out := factoryGeminiPost(t, e, "gemini-4-carbon", "another question"); rec.Code != 200 {
		t.Fatalf("other model of the same account: %d %s", rec.Code, out)
	}
	all := e.snapshot()
	if len(all) != before+1 || !strings.Contains(all[before].auth, "key_1") {
		t.Fatalf("f1 must still serve its own tier's models: %q", all[before].auth)
	}
	// A conversation that already learned the gap skips f1 entirely.
	before = len(e.snapshot())
	if rec, out := factoryGeminiPost(t, e, "gemini-4-neon", "hi"); rec.Code != 200 {
		t.Fatalf("cooled pair retried: %d %s", rec.Code, out)
	}
	if got := len(e.snapshot()) - before; got != 1 {
		t.Fatalf("cooled pair hit upstream %d times, want 1", got)
	}
}

// The stop rule still owns every refusal the request itself is answerable
// for: a 400 without the tier wording must not rotate accounts.
func TestFactoryPlainBadRequestStillStops(t *testing.T) {
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		fmt.Fprint(w, `{"detail":"invalid contents: parts must be a list"}`)
	}
	e.mu.Unlock()
	rsp, out := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rsp.StatusCode != 400 || len(e.snapshot()) != 1 {
		t.Fatalf("plain 400 rotated: %d calls=%d %s", rsp.StatusCode, len(e.snapshot()), out)
	}
}

// The default rules must keep the tier refusal distinct from the request's own
// 400s, and the retry stop list must remain untouched by it.
func TestFactoryMainlineRulesStayOrdered(t *testing.T) {
	rs := rules.DefaultFactoryErrorRules()
	rule := rules.MatchError(rs, 400, strings.ToLower(factoryTierRefusalBody), http.Header{})
	if rule == nil || rule.ID != "factory-model-id-rejected" {
		t.Fatalf("tier refusal rule not matched: %+v", rule)
	}
	if r := rules.MatchError(rs, 400, strings.ToLower(`{"detail":"invalid field"}`), http.Header{}); r != nil {
		t.Fatalf("plain 400 matched %q", r.ID)
	}
	if r := rules.MatchError(rs, 403, strings.ToLower(`{"code":"content_policy_rejected"}`), http.Header{}); r == nil || r.ID != "factory-policy" {
		t.Fatalf("policy gate lost: %+v", r)
	}
	if r := rules.MatchRetry(rules.DefaultFactoryRetryRules(), 400, "", http.Header{}); r == nil || r.Decision != rules.DecisionStop {
		t.Fatalf("stop rule changed: %+v", r)
	}
	// The measured cooldown is long enough not to re-probe every conversation
	// and short enough to pick the model up after a plan upgrade.
	if d := rules.Cooldown(rule, http.Header{}, "", time.Now()); d < 12*time.Hour || d > 7*24*time.Hour {
		t.Fatalf("cooldown %v outside the measured window", d)
	}
}

// When no account's tier carries the model, the pool must say so instead of
// reporting a cooldown the caller could wait out: the answer is stable until
// a plan changes, which the caller cannot influence.
func TestFactoryTierModelRefusalAllAccountsIsNotEntitled(t *testing.T) {
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/g/v1/generate") {
			w.WriteHeader(400)
			fmt.Fprint(w, factoryTierRefusalBody)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}]}`)
	}
	e.mu.Unlock()
	// The first request can only learn the gap from the upstream answer; both
	// accounts are tried and the client gets a plain 503, never a fake 400.
	rec, out := factoryGeminiPost(t, e, "gemini-4-neon", "tier probe")
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "" || strings.Contains(out, "Invalid model ID") {
		t.Fatalf("all-accounts tier gap: %d retry-after=%q %s", rec.Code, rec.Header().Get("Retry-After"), out)
	}
	if len(e.snapshot()) != 2 {
		t.Fatalf("attempts=%d, want both accounts tried once", len(e.snapshot()))
	}
	// Afterwards both pairs are known bad, so the answer says why instead of
	// pretending a wait would help.
	rec, out = factoryGeminiPost(t, e, "gemini-4-neon", "second question")
	if rec.Code != 503 || !strings.Contains(out, "not included in any upstream account") || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("learned tier gap: %d retry-after=%q %s", rec.Code, rec.Header().Get("Retry-After"), out)
	}
}
