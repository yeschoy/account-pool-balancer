package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func setTokenHarborDetail(t *testing.T, e *thEnv, enabled bool) {
	t.Helper()
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.TokenHarbor.DetailLog = enabled
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestTokenHarborDetailSwitch(t *testing.T) {
	for _, global := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("global=%t/tokenharbor=%t", global, enabled), func(t *testing.T) {
				e := newTHEnv(t)
				if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
					c.Logs.Detail, c.TokenHarbor.DetailLog = global, enabled
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				for _, code := range []int{200, 400} {
					e.setHandler(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(code)
						if code == 400 {
							fmt.Fprint(w, `{"error":{"message":"mock failure"}}`)
						} else {
							fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
						}
					})
					status, _, h := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil)
					if status != code {
						t.Fatalf("wire status changed: %d, want %d", status, code)
					}
					filters := map[string]string{"requestId": h.Get("X-Request-Id")}
					details, _ := e.s.logs.Query("details", filters, time.Time{}, time.Time{}, 10, 0)
					want := 0
					if enabled {
						want = 1
					}
					if len(details.Items) != want {
						t.Fatalf("status %d: got %d details, want %d", code, len(details.Items), want)
					}
					requests, _ := e.s.logs.Query("requests", filters, time.Time{}, time.Time{}, 10, 0)
					if len(requests.Items) != 1 {
						t.Fatal("ordinary request log was disabled")
					}
					if code == 400 {
						errors, _ := e.s.logs.Query("errors", filters, time.Time{}, time.Time{}, 10, 0)
						if len(errors.Items) != 1 {
							t.Fatal("ordinary error log was disabled")
						}
					}
				}
			})
		}
	}
}

func TestTokenHarborDetailSettingsIsolation(t *testing.T) {
	e := newTHEnv(t)
	w := httptest.NewRecorder()
	e.s.apiSettings(w, httptest.NewRequest("GET", "/api/settings?provider=tokenharbor", nil))
	var response struct {
		Settings settingsDoc `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	doc := response.Settings
	if doc.TokenHarbor.DetailLog == nil || *doc.TokenHarbor.DetailLog {
		t.Fatal("settings must explicitly show default off")
	}
	*doc.TokenHarbor.DetailLog = true
	save := func(provider string) {
		t.Helper()
		doc.Revision = e.s.cfg.Get().Revision
		body, _ := json.Marshal(doc)
		w = httptest.NewRecorder()
		e.s.apiSaveSettings(w, httptest.NewRequest("PUT", "/api/settings?provider="+provider, bytes.NewReader(body)))
		if w.Code != 200 {
			t.Fatalf("settings save: %d %s", w.Code, w.Body.String())
		}
	}
	save("tokenharbor")
	if !e.s.cfg.Get().TokenHarbor.DetailLog || e.s.cfg.Get().Logs.Detail {
		t.Fatal("pool detail switch changed shared logging")
	}
	*doc.TokenHarbor.DetailLog = false
	save("opendesign")
	if !e.s.cfg.Get().TokenHarbor.DetailLog {
		t.Fatal("another pool changed TokenHarbor detail logging")
	}
	doc.TokenHarbor.DetailLog = nil
	save("tokenharbor")
	if !e.s.cfg.Get().TokenHarbor.DetailLog {
		t.Fatal("legacy settings client cleared the switch")
	}
	registration, _ := json.Marshal(map[string]any{"tokenHarbor": config.TokenHarborDefaults()})
	w = httptest.NewRecorder()
	e.s.apiTHSaveSettings(w, httptest.NewRequest("PUT", "/api/tokenharbor/settings", bytes.NewReader(registration)))
	if w.Code != 200 || !e.s.cfg.Get().TokenHarbor.DetailLog {
		t.Fatal("registration settings changed pool detail logging", w.Code)
	}
	if err := e.s.cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	if !e.s.cfg.Get().TokenHarbor.DetailLog {
		t.Fatal("switch did not persist to disk")
	}
}
