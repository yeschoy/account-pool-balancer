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

const altapiUserQuotaBody = `{"error":{"message":"用户余额不足, 需要 $6.669924: insufficient_user_quota (request id: quota-mock)","type":"new_api_error"}}`

func TestALTAPIUserQuotaSwitchWithoutCooldown(t *testing.T) {
	for _, tc := range []struct {
		status int
		spaced bool
	}{{200, false}, {400, false}, {403, false}, {403, true}} {
		t.Run(fmt.Sprintf("%d/spaced=%v", tc.status, tc.spaced), func(t *testing.T) {
			e := newALTAPIStickyEnv(t)
			var failedKey string
			e.up.mu.Lock()
			e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
				e.up.mu.Lock()
				if failedKey == "" {
					failedKey = key
				}
				fail := key == failedKey
				e.up.mu.Unlock()
				if fail {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					failure := altapiUserQuotaBody
					if tc.spaced {
						failure = strings.ReplaceAll(failure, "insufficient_user_quota", "insufficient user quota")
					}
					fmt.Fprint(w, failure)
					return
				}
				writeOK(w, key)
			}
			e.up.mu.Unlock()
			h := map[string]string{"X-Session-Id": "quota-switch-session"}
			resp := e.post("/v1/chat/completions", chat(false), h)
			out := readAll(resp)
			if resp.StatusCode != 200 || e.up.callCount() != 2 {
				t.Fatalf("status=%d calls=%d: %s", resp.StatusCode, e.up.callCount(), out)
			}
			page, err := e.srv.logs.Query("errors", nil, time.Time{}, time.Time{}, 20, 0)
			if err != nil || len(page.Items) != 1 {
				t.Fatalf("errors: %v rows=%d", err, len(page.Items))
			}
			var log errorLog
			if err := json.Unmarshal(page.Items[0], &log); err != nil {
				t.Fatal(err)
			}
			if log.RuleID != "altapi-user-quota-switch" || log.RuleAction != "switch-account" || log.HealthAction != "none" || !log.Switched || log.RetryAfterMs != nil {
				t.Fatalf("quota must only switch: %+v", log)
			}
			view := e.srv.pool.Views(e.srv.cfg.Get())[log.AccountID]
			if view.Status != "ok" || view.Samples != 0 || (view.State != nil && !view.State.CooldownUntil.IsZero()) {
				t.Fatalf("quota changed account health: %+v", view)
			}
			// The same session must stay on the successful replacement.
			altStickyPost(t, e, chat(false), h, 200)
			logs := altStickyLogs(e)
			if logs[1]["accountId"] != logs[0]["accountId"] || logs[1]["bindingResult"] != "hit" || e.up.callCount() != 3 {
				t.Fatal("successful replacement did not retain the session binding")
			}
			// The failed account remains immediately usable by a new request;
			// no hidden model/account cooldown may be left behind.
			_, err = e.srv.cfg.Update(-1, func(c *config.Config) error {
				for i := range c.Accounts {
					c.Accounts[i].Enabled = c.Accounts[i].ID == log.AccountID
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			e.up.mu.Lock()
			e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) { writeOK(w, key) }
			e.up.mu.Unlock()
			altStickyPost(t, e, chat(false), map[string]string{"X-Session-Id": "new-quota-session"}, 200)
			if e.up.callCount() != 4 {
				t.Fatal("failed account was not immediately selectable")
			}
		})
	}
}

func TestALTAPIUserQuotaSwitchSettingsRoundTrip(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	before := e.srv.cfg.Get().Clone()
	w := deployAdmin(t, &deployTestEnv{s: e.srv}, "GET", "/api/settings?provider=altapi", nil)
	var doc struct {
		Settings settingsDoc `json:"settings"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &doc) != nil {
		t.Fatal("settings GET failed")
	}
	rule := rules.MatchError(doc.Settings.ErrorRules, 403, altapiUserQuotaBody, nil)
	if rule == nil || rule.Action != rules.ActionSwitchAccount || rule.Reset != nil || rule.RefreshQuota {
		t.Fatalf("GET lost switch-only rule: %+v", rule)
	}
	w = deployAdmin(t, &deployTestEnv{s: e.srv}, "PUT", "/api/settings?provider=altapi", doc.Settings)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	loaded, err := config.Load(filepath.Join(e.srv.dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(doc.Settings.ErrorRules, loaded.Get().ALTAPIErrorRules) {
		t.Fatal("switch-only rule lost after save/reload")
	}
	for _, provider := range []string{config.ProviderOpenDesign, config.ProviderRiver, config.ProviderDeploy, config.ProviderX50, config.ProviderMistral} {
		a, b := before.RulesFor(provider)
		x, y := loaded.Get().RulesFor(provider)
		if !reflect.DeepEqual(a, x) || !reflect.DeepEqual(b, y) {
			t.Fatalf("changed %s rules", provider)
		}
	}
}

func TestALTAPIUserQuotaSwitchBoundsAndStop(t *testing.T) {
	for _, tc := range []struct {
		name      string
		max, want int
		stop      bool
	}{
		{"attempt budget", 1, 1, false},
		{"each account once", 5, 2, false},
		{"explicit stop", 5, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newALTAPIStickyEnv(t)
			_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
				c.ALTAPI.Routing.MaxAccounts = tc.max
				if tc.stop {
					c.ALTAPIRetryRules = append([]rules.RetryRule{{ID: "operator-quota-stop", Decision: rules.DecisionStop, When: rules.Match{BodyContains: []string{"insufficient_user_quota"}}}}, c.ALTAPIRetryRules...)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			e.up.mu.Lock()
			e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(403)
				fmt.Fprint(w, altapiUserQuotaBody)
			}
			e.up.mu.Unlock()
			resp := e.post("/v1/chat/completions", chat(false), nil)
			out := readAll(resp)
			if resp.StatusCode != 503 || e.up.callCount() != tc.want || resp.Header.Get("Retry-After") != "" {
				t.Fatalf("status=%d calls=%d retry=%s: %s", resp.StatusCode, e.up.callCount(), resp.Header.Get("Retry-After"), out)
			}
			for _, view := range e.srv.pool.Views(e.srv.cfg.Get()) {
				if view.Status != "ok" || view.Samples != 0 {
					t.Fatal("exhausted attempts changed account health", view)
				}
			}
		})
	}
}

func TestALTAPIUserQuotaCommittedStreamDoesNotSwitch(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	e.up.mu.Lock()
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"already delivered\"}}]}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: "+altapiUserQuotaBody+"\n\n")
		w.(http.Flusher).Flush()
	}
	e.up.mu.Unlock()
	resp := e.post("/v1/chat/completions", chat(true), nil)
	out := readAll(resp)
	if resp.StatusCode != 200 || !strings.Contains(out, "already delivered") || e.up.callCount() != 1 {
		t.Fatalf("committed quota failure replayed: status=%d calls=%d", resp.StatusCode, e.up.callCount())
	}
	for _, view := range e.srv.pool.Views(e.srv.cfg.Get()) {
		if view.Status != "ok" || (view.State != nil && !view.State.CooldownUntil.IsZero()) {
			t.Fatal("committed quota error cooled account", view)
		}
	}
}
