package server

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

// settingsOf performs an authenticated admin request against the pool and
// decodes the JSON reply.
func (e *msEnv) settingsOf(t *testing.T, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, path+"?provider=modelstudio", rd)
	login := httptest.NewRecorder()
	sess := e.s.admin.create(login, httptest.NewRequest("POST", "/api/login", nil))
	for _, c := range login.Result().Cookies() {
		r.AddCookie(c)
	}
	r.Header.Set("X-CSRF-Token", sess.csrf)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, r)
	out := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// The panel must be visible, loadable, savable, and survive a reload; these
// are the explicit onward gates, not a mocked form.
func TestModelStudioSettingsRoundTrip(t *testing.T) {
	e := newMSEEnv(t)
	code, got := e.settingsOf(t, "GET", "/api/settings", nil)
	if code != 200 {
		t.Fatalf("GET settings %d %v", code, got)
	}
	settings, _ := got["settings"].(map[string]any)
	ms, ok := settings["modelStudio"].(map[string]any)
	if !ok {
		t.Fatalf("modelStudio block missing: %v", got)
	}
	if ms["console"] == nil {
		t.Fatal("console state must be projected")
	}
	cs, _ := ms["console"].(map[string]any)
	if cs["state"] != quota.ModelStudioQuotaNotConnected || cs["reason"] == "" {
		t.Fatalf("console projection = %v", cs)
	}
	if ms["region"] != config.ModelStudioRegionSingapore {
		t.Fatalf("region = %v", ms["region"])
	}

	// Save a non-default region, sync interval, detail switch and rules.
	errRules, _ := settings["errorRules"].([]any)
	retryRules, _ := settings["retryRules"].([]any)
	body := map[string]any{
		"revision":   settings["revision"],
		"upstream":   settings["upstream"],
		"routing":    settings["routing"],
		"quota":      settings["quota"],
		"logs":       settings["logs"],
		"pricing":    settings["pricing"],
		"errorRules": errRules, "retryRules": retryRules,
		"modelStudio": map[string]any{
			"region":       "cn-beijing",
			"modelSyncSec": 120,
			"detailLog":    true,
			"planOverrides": map[string]string{
				"ms2": config.ModelStudioPlanTokenPlan,
			},
		},
	}
	if code, got := e.settingsOf(t, "PUT", "/api/settings", body); code != 200 {
		t.Fatalf("PUT settings %d %v", code, got)
	}
	cfg := e.s.cfg.Get()
	if cfg.ModelStudio.Region != "cn-beijing" || cfg.ModelStudio.ModelSyncSec != 120 || !cfg.ModelStudio.DetailLog {
		t.Fatalf("settings not applied: %+v", cfg.ModelStudio)
	}
	if cfg.ModelStudio.Routing == nil || cfg.ModelStudio.Quota == nil {
		t.Fatal("modelstudio routing/quota were not persisted independently")
	}

	// Reload from disk: the values must survive, not just live in memory.
	st, err := config.Load(filepath.Join(e.s.dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	reloaded := st.Get()
	if reloaded.ModelStudio.Region != "cn-beijing" || reloaded.ModelStudio.ModelSyncSec != 120 || !reloaded.ModelStudio.DetailLog {
		t.Fatalf("settings lost on reload: %+v", reloaded.ModelStudio)
	}
	if reloaded.ModelStudio.PlanOverrides["ms2"] != config.ModelStudioPlanTokenPlan {
		t.Fatalf("plan override lost: %v", reloaded.ModelStudio.PlanOverrides)
	}
}

// Saving one pool must not write another pool's routing or rules.
func TestModelStudioSaveDoesNotTouchOtherPools(t *testing.T) {
	e := newMSEEnv(t)
	before := e.s.cfg.Get()
	msGlobalRouting := before.Routing
	thRules := len(before.TokenHarborErrorRules)
	code, got := e.settingsOf(t, "GET", "/api/settings", nil)
	if code != 200 {
		t.Fatalf("GET %d", code)
	}
	settings, _ := got["settings"].(map[string]any)
	body := map[string]any{
		"revision": settings["revision"], "upstream": settings["upstream"],
		"routing": map[string]any{"strategy": "ordered", "maxAccountsPerRequest": 1},
		"quota":   map[string]any{"enabled": true, "intervalSec": 300},
		"logs":    settings["logs"], "pricing": settings["pricing"],
		"errorRules": settings["errorRules"], "retryRules": settings["retryRules"],
		"modelStudio": map[string]any{"region": "us-east-1", "modelSyncSec": 600, "detailLog": false},
	}
	if code, got := e.settingsOf(t, "PUT", "/api/settings", body); code != 200 {
		t.Fatalf("PUT %d %v", code, got)
	}
	after := e.s.cfg.Get()
	if after.Routing.Strategy != msGlobalRouting.Strategy || after.Routing.MaxAccounts != msGlobalRouting.MaxAccounts {
		t.Fatal("global routing changed by a modelstudio save")
	}
	if after.Quota.IntervalSec != before.Quota.IntervalSec {
		t.Fatal("global quota changed by a modelstudio save")
	}
	if len(after.TokenHarborErrorRules) != thRules {
		t.Fatal("tokenharbor rules changed by a modelstudio save")
	}
	if after.ModelStudio.Routing == nil || after.ModelStudio.Routing.MaxAccounts != 1 {
		t.Fatal("modelstudio routing override not saved")
	}
}

// An invalid region or an attempted console enable is refused before saving.
func TestModelStudioSettingsValidation(t *testing.T) {
	e := newMSEEnv(t)
	_, got := e.settingsOf(t, "GET", "/api/settings", nil)
	settings, _ := got["settings"].(map[string]any)
	base := map[string]any{
		"revision": settings["revision"], "upstream": settings["upstream"],
		"routing": settings["routing"], "quota": settings["quota"],
		"logs": settings["logs"], "pricing": settings["pricing"],
		"errorRules": settings["errorRules"], "retryRules": settings["retryRules"],
	}
	for _, tc := range []struct{ name, region string }{
		{"unknown region", "mars-north-1"},
	} {
		b := map[string]any{}
		for k, v := range base {
			b[k] = v
		}
		b["modelStudio"] = map[string]any{"region": tc.region, "modelSyncSec": 600}
		// The API must reject an unknown region outright rather than
		// silently falling back to the default host.
		if code, body := e.settingsOf(t, "PUT", "/api/settings", b); code != 400 {
			t.Fatalf("%s: expected 400, got %d %v", tc.name, code, body)
		}
		if e.s.cfg.Get().ModelStudio.Region != config.ModelStudioRegionSingapore {
			t.Fatalf("%s: rejected save still changed the region", tc.name)
		}
	}
	b := map[string]any{}
	for k, v := range base {
		b[k] = v
	}
	b["modelStudio"] = map[string]any{"region": "ap-southeast-1", "modelSyncSec": 5}
	if code, _ := e.settingsOf(t, "PUT", "/api/settings", b); code == 200 {
		t.Fatal("invalid modelSyncSec accepted")
	}
	if e.s.cfg.Get().ModelStudio.ModelSyncSec == 5 {
		t.Fatal("invalid value persisted")
	}
}
