package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
	"account-pool-balancer/internal/rules"
)

func TestMistralRefreshQuotaProjectsBudget(t *testing.T) {
	e := newMistralEnv(t)
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/m1/refresh-quota", nil)
	r.SetPathValue("id", "m1")
	w := httptest.NewRecorder()
	e.s.apiAccountAction(w, r)
	var result struct {
		OK        bool `json:"ok"`
		Refreshed bool `json:"quotaRefreshed"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || !result.OK || !result.Refreshed {
		t.Fatalf("refresh did not report budget: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	e.s.apiAccounts(w, httptest.NewRequest(http.MethodGet, "/api/accounts?provider=mistral", nil))
	var response struct {
		Accounts []struct {
			ID    string          `json:"id"`
			Fresh bool            `json:"quotaFresh"`
			Quota *quota.Snapshot `json:"quota"`
		} `json:"accounts"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal("invalid account projection")
	}
	for _, a := range response.Accounts {
		if a.ID == "m1" {
			if !a.Fresh || a.Quota.Mistral == nil || a.Quota.Mistral.Budget.Remaining != 22.5 {
				t.Fatal("budget missing from account response")
			}
			return
		}
	}
	t.Fatal("account missing")
}

func TestMistralSettingsFullRoundTripAndIsolation(t *testing.T) {
	e := newMistralEnv(t)
	w := httptest.NewRecorder()
	e.s.apiSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings?provider=mistral", nil))
	var response struct {
		Settings settingsDoc `json:"settings"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatal("invalid settings")
	}
	before := e.s.cfg.Get()
	doc := response.Settings
	doc.Routing.Strategy = "least-used"
	doc.Routing.SessionHeaders = []string{"X-Mistral-Session"}
	doc.Routing.ExplicitTTLSec = 3600
	doc.Routing.FallbackTTLSec = 600
	doc.Routing.OnBindingBusy = "wait-overflow"
	doc.Routing.ConcurrencyWaitMs = 1200
	doc.Routing.MaxAccounts = 3
	doc.Routing.HealthFilter = true
	doc.Routing.MinimumHealth = 0.7
	doc.Routing.LowShare, doc.Routing.SpentShare = 70, 95
	doc.Routing.FirstEventSec, doc.Routing.IdleSec, doc.Routing.RequestSec = 90, 45, 120
	doc.Routing.MaxBodyMB, doc.Routing.ModelsCacheSec = 32, 300
	doc.Quota = config.Quota{Enabled: true, IntervalSec: 120, Concurrency: 3, StaleSec: 300}
	every, detail := 900, true
	doc.Mistral.ModelSyncSec, doc.Mistral.DetailLog = &every, &detail
	doc.ErrorRules[0].Note = "custom Mistral error rule"
	doc.RetryRules[0].Note = "custom Mistral retry rule"
	raw, _ := json.Marshal(doc)
	w = httptest.NewRecorder()
	e.s.apiSaveSettings(w, httptest.NewRequest(http.MethodPut, "/api/settings?provider=mistral", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatalf("settings save failed: %s", w.Body)
	}
	after := e.s.cfg.Get()
	if !reflect.DeepEqual(before.Routing, after.Routing) || before.Quota != after.Quota || !reflect.DeepEqual(before.TokenHarbor, after.TokenHarbor) || !reflect.DeepEqual(before.ErrorRules, after.ErrorRules) {
		t.Fatal("Mistral save changed another pool")
	}
	check := func() {
		t.Helper()
		c := e.s.cfg.Get()
		effective := c.ForProvider(config.ProviderMistral)
		if !reflect.DeepEqual(effective.Routing, doc.Routing) || effective.Quota != doc.Quota || c.Mistral.ModelInterval() != 900 || c.Mistral.DetailLog == nil || !*c.Mistral.DetailLog {
			t.Fatal("routing/polling/log settings did not survive round trip")
		}
		errors, retries := c.RulesFor(config.ProviderMistral)
		if errors[0].Note != "custom Mistral error rule" || retries[0].Note != "custom Mistral retry rule" {
			t.Fatal("independent rules not saved")
		}
	}
	check()
	if err := e.s.cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	check()
	stable := e.s.cfg.Get()
	doc.Revision = stable.Revision
	invalid := 10
	doc.Mistral.ModelSyncSec = &invalid
	raw, _ = json.Marshal(doc)
	w = httptest.NewRecorder()
	e.s.apiSaveSettings(w, httptest.NewRequest(http.MethodPut, "/api/settings?provider=mistral", bytes.NewReader(raw)))
	if w.Code != 400 || e.s.cfg.Get() != stable {
		t.Fatal("invalid polling setting was published")
	}
}

func TestMistralConfiguredRulesActuallyApply(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); w.Write([]byte(`{"code":3800}`)) })
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		r := c.Routing
		r.MaxAccounts = 2
		c.Mistral.Routing = &r
		c.MistralErrorRules = []rules.ErrorRule{{ID: "custom-mistral-3800", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, When: rules.Match{Statuses: []int{503}, BodyCodes: []string{"3800"}}, Reset: &rules.Reset{Fallback: "1m"}, DownstreamStatus: 503}}
		c.MistralRetryRules = []rules.RetryRule{{ID: "stop-mistral-3800", Decision: rules.DecisionStop, When: rules.Match{BodyCodes: []string{"3800"}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if status, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); status != 503 || e.count() != 2 {
		t.Fatalf("configured account cooldown did not take priority over stop rule: status=%d calls=%d", status, e.count())
	}
	views := e.s.pool.Views(e.s.cfg.Get())
	if views["m1"].State == nil || views["m1"].State.RuleID != "custom-mistral-3800" || !views["m1"].State.CooldownUntil.After(time.Now()) {
		t.Fatal("configured error rule did not cool account")
	}
	if views["m2"].State == nil || views["m2"].State.RuleID != "custom-mistral-3800" {
		t.Fatal("configured rule did not apply to second attempt")
	}
	page, _ := e.s.logs.Query("errors", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 20, 0)
	logged, _ := json.Marshal(page.Items)
	if !strings.Contains(string(logged), "stop-mistral-3800") || !strings.Contains(string(logged), "overridden-by-error-rule") {
		t.Fatal("configured retry rule not matched or priority not recorded")
	}
}

func TestMistralPoolDetailLogSwitch(t *testing.T) {
	e := newMistralEnv(t)
	for _, enabled := range []bool{false, true} {
		_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
			c.Logs.Detail = !enabled
			v := enabled
			c.Mistral.DetailLog = &v
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 200 {
			t.Fatal("mock inference failed")
		}
		logs, _ := e.s.logs.Query("details", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 20, 0)
		if enabled && len(logs.Items) == 0 || !enabled && len(logs.Items) != 0 {
			t.Fatalf("Mistral detail switch ignored: %v", enabled)
		}
	}
}

func TestMistralRefreshPermissionDoesNotReportSuccess(t *testing.T) {
	e := newMistralEnv(t)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"m"}]}`))
			return
		}
		if r.URL.Path == "/api/vibe/whoami" {
			w.Write([]byte(`{"plan_type":"API"}`))
			return
		}
		w.WriteHeader(403)
		w.Write([]byte(`{"error":"private-secret-body"}`))
	}))
	defer up.Close()
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Mistral.BaseURL = up.URL + "/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/accounts/m1/refresh-quota", nil)
	r.SetPathValue("id", "m1")
	w := httptest.NewRecorder()
	e.s.apiAccountAction(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"quotaRefreshed":false`) || strings.Contains(w.Body.String(), "private-secret-body") || e.s.quota.Get("m1").AuthFailed {
		t.Fatalf("permission failure not isolated: %s", w.Body)
	}
}
