package server

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestTokenHarborMergeRegistrationSettingsPreservePool(t *testing.T) {
	e := newTHEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		routing := c.ForProvider(config.ProviderTokenHarbor).Routing
		routing.Strategy = "least-selections"
		c.TokenHarbor.Routing = &routing
		c.TokenHarbor.ModelSyncSec = 900
		c.TokenHarbor.CaptchaKey = "mock-solver-write-only"
		c.TokenHarbor.ProxyURL = "http://mockuser:mockpass@127.0.0.1:19899"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.s.cfg.Get()
	w := httptest.NewRecorder()
	e.s.apiTHSettings(w, httptest.NewRequest("GET", "/api/tokenharbor/settings", nil))
	for _, secret := range []string{"mock-solver-write-only", "mockuser", "mockpass"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("registration settings leak a credential")
		}
	}
	var view struct {
		HasCaptcha bool `json:"hasCaptcha"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || !view.HasCaptcha {
		t.Fatal("write-only credential status missing")
	}
	payload := map[string]any{"expectedRevision": cfg.Revision, "tokenHarbor": map[string]any{"baseUrl": cfg.TokenHarbor.BaseURL, "keyLabel": "merge-label", "group": "tokenharbor", "registerConcurrency": 3, "mailTimeoutSec": 600}}
	body, _ := json.Marshal(payload)
	w = httptest.NewRecorder()
	e.s.apiTHSaveSettings(w, httptest.NewRequest("PUT", "/api/tokenharbor/settings", bytes.NewReader(body)))
	if w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	after := e.s.cfg.Get()
	if after.ForProvider(config.ProviderTokenHarbor).Routing.Strategy != "least-selections" || after.TokenHarbor.ModelSyncSec != 900 || after.TokenHarbor.CaptchaKey != cfg.TokenHarbor.CaptchaKey || after.TokenHarbor.ProxyURL != cfg.TokenHarbor.ProxyURL || after.TokenHarbor.KeyLabel != "merge-label" {
		t.Fatal("registration save clobbered pool settings or write-only credentials")
	}
	for _, secret := range []string{"mock-solver-write-only", "mockuser", "mockpass"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("save response leaked credentials")
		}
	}
	payload["expectedRevision"], payload["clearCaptchaKey"], payload["clearProxy"] = after.Revision, true, true
	body, _ = json.Marshal(payload)
	w = httptest.NewRecorder()
	e.s.apiTHSaveSettings(w, httptest.NewRequest("PUT", "/api/tokenharbor/settings", bytes.NewReader(body)))
	if w.Code != 200 || e.s.cfg.Get().TokenHarbor.CaptchaKey != "" || e.s.cfg.Get().TokenHarbor.ProxyURL != "" {
		t.Fatal("explicit credential clear failed")
	}
}

func TestTokenHarborMergeSingleTabRetainsRegistration(t *testing.T) {
	page, err := fs.ReadFile(webFS, "web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(page)
	if strings.Count(html, `data-pool="tokenharbor"`) != 1 {
		t.Fatal("duplicate Token Harbor pool tab")
	}
	for _, part := range []string{`data-page="thregister"`, `data-page="thsettings"`, `id="tokenharbor-settings"`, `/assets/tokenharbor.js`} {
		if !strings.Contains(html, part) {
			t.Fatal("registration or native-pool UI lost during merge")
		}
	}
}
