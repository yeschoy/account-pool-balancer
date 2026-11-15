package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/pool"
)

func newALTAPIStickyEnv(t *testing.T) *env {
	t.Helper()
	return newEnv(t, []config.Account{
		{ID: "alt-a", Provider: config.ProviderALTAPI, Group: "altapi", MaxConc: 1},
		{ID: "alt-b", Provider: config.ProviderALTAPI, Group: "altapi", MaxConc: 1},
	}, func(c *config.Config) {
		c.ALTAPI.BaseURL = c.Upstream.RuntimeBase
		r := c.ALTAPI.EffectiveRouting()
		r.Strategy, r.OnBindingBusy, r.ConcurrencyWaitMs = "least-selections", "reject", 0
		r.ExplicitTTLSec = 120
		c.ALTAPI.Routing = &r
		q := c.ALTAPI.EffectiveQuota()
		q.Enabled = false
		c.ALTAPI.Quota = &q
		// A conflicting global configuration must never control this pool.
		c.Routing.Sticky = false
		c.ClientKeys[0].Provider, c.ClientKeys[0].Group = config.ProviderALTAPI, "altapi"
		c.ClientKeys = append(c.ClientKeys, config.ClientKey{ID: "ck2", Provider: config.ProviderALTAPI, Group: "altapi", KeyHash: config.HashKey("altapi-other-client"), Enabled: true})
	})
}

func altStickyLogs(e *env) []map[string]any {
	logs := e.requestLogs() // logstore queries are newest-first
	slices.Reverse(logs)
	return logs
}

func altStickyPost(t *testing.T, e *env, body map[string]any, h map[string]string, want int) {
	t.Helper()
	resp := e.post("/v1/chat/completions", body, h)
	out := readAll(resp)
	if resp.StatusCode != want {
		t.Fatalf("status %d, want %d: %s", resp.StatusCode, want, out)
	}
}

func TestALTAPIStickySessionOutranksCache(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	for _, cache := range []string{"prefix-one", "prefix-two"} {
		body := chat(false)
		body["prompt_cache_key"] = cache
		altStickyPost(t, e, body, map[string]string{"X-Session-Id": "same-session"}, 200)
	}
	logs := altStickyLogs(e)
	if len(logs) != 2 || logs[0]["accountId"] != logs[1]["accountId"] {
		t.Fatal("changing a cache key moved the session to another account")
	}
	for _, log := range logs {
		if log["affinityKeyType"] != "session_id" || log["affinityConfidence"] != "explicit" {
			t.Fatal("cache key overrode the real session", log)
		}
	}
	if logs[1]["bindingResult"] != "hit" {
		t.Fatal("same session did not hit its binding", logs[1])
	}
	body := chat(false)
	body["prompt_cache_key"] = "prefix-two"
	altStickyPost(t, e, body, map[string]string{"X-Session-Id": "different-session"}, 200)
	logs = altStickyLogs(e)
	if len(logs) != 3 || logs[2]["bindingResult"] != "miss" || logs[2]["accountId"] == logs[0]["accountId"] {
		t.Fatal("a shared cache key collapsed distinct explicit sessions")
	}
}

func TestALTAPIStickyCallerIsolation(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	for _, key := range []string{e.key, "altapi-other-client", e.key} {
		altStickyPost(t, e, chat(false), map[string]string{"Authorization": "Bearer " + key, "X-Session-Id": "shared-session-id"}, 200)
	}
	logs := altStickyLogs(e)
	if len(logs) != 3 || logs[0]["accountId"] == logs[1]["accountId"] || logs[0]["accountId"] != logs[2]["accountId"] {
		t.Fatal("different downstream keys shared a binding or lost their owner")
	}
	if logs[1]["bindingResult"] != "miss" || logs[2]["bindingResult"] != "hit" {
		t.Fatal("downstream binding scopes were not independent")
	}
	for _, value := range []string{"shared-session-id", e.key, "altapi-other-client"} {
		if strings.Contains(e.allLogText(), value) {
			t.Fatal("raw caller/session identity leaked into request/error logs")
		}
	}
}

func TestALTAPIStickyCacheHeaderAndParent(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	altStickyPost(t, e, chat(false), map[string]string{"X-Prompt-Cache-Key": "cache-only"}, 200)
	altStickyPost(t, e, chat(false), map[string]string{"Prompt-Cache-Key": "cache-only"}, 200)
	altStickyPost(t, e, chat(false), map[string]string{"X-Session-Id": "parent"}, 200)
	body := chat(false)
	body["prompt_cache_key"] = "different-cache"
	altStickyPost(t, e, body, map[string]string{"X-Session-Id": "child", "X-Parent-Session-Id": "parent"}, 200)
	logs := altStickyLogs(e)
	if len(logs) != 4 || logs[1]["affinityKeyType"] != "prompt_cache_key" || logs[1]["bindingResult"] != "hit" || logs[0]["accountId"] != logs[1]["accountId"] {
		t.Fatal("cache-only header fallback did not bind")
	}
	if logs[3]["affinityKeyType"] != "parent_session" || logs[3]["bindingResult"] != "hit" || logs[2]["accountId"] != logs[3]["accountId"] {
		t.Fatal("child did not follow the parent session")
	}
}

func TestALTAPIStickyTTLSlidesAndExpires(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	now := time.Now()
	e.srv.pool.SetClock(func() time.Time { return now })
	h := map[string]string{"X-Session-Id": "ttl-session"}
	altStickyPost(t, e, chat(false), h, 200)
	now = now.Add(90 * time.Second)
	altStickyPost(t, e, chat(false), h, 200)
	now = now.Add(90 * time.Second)
	altStickyPost(t, e, chat(false), h, 200)
	now = now.Add(121 * time.Second)
	altStickyPost(t, e, chat(false), h, 200)
	logs := altStickyLogs(e)
	if len(logs) != 4 || logs[1]["bindingResult"] != "hit" || logs[2]["bindingResult"] != "hit" || logs[3]["bindingResult"] != "miss" || logs[2]["accountId"] == logs[3]["accountId"] {
		t.Fatal("ALTAPI sliding TTL did not use its independent routing settings")
	}
}

func TestALTAPIStickyBusyPolicyPreservesOwner(t *testing.T) {
	for _, policy := range []string{"reject", "overflow", "wait-overflow"} {
		t.Run(policy, func(t *testing.T) {
			e := newALTAPIStickyEnv(t)
			_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
				c.ALTAPI.Routing.OnBindingBusy = policy
				if policy == "wait-overflow" {
					c.ALTAPI.Routing.ConcurrencyWaitMs = 10
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			h := map[string]string{"X-Session-Id": "busy-session"}
			altStickyPost(t, e, chat(false), h, 200)
			cfg := e.srv.cfg.Get().ForProvider(config.ProviderALTAPI)
			owner := e.requestLogs()[0]["accountId"].(string)
			// Occupy the actual owner without creating a second binding.
			other := map[string]bool{}
			for _, a := range cfg.Accounts {
				if a.ID != owner {
					other[a.ID] = true
				}
			}
			hold, err := e.srv.pool.Acquire(context.Background(), cfg, pool.Request{Group: "altapi", Excluded: other})
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Release()
			want := 200
			if policy == "reject" {
				want = 429
			}
			altStickyPost(t, e, chat(false), h, want)
			hold.Release()
			altStickyPost(t, e, chat(false), h, 200)
			logs := altStickyLogs(e)
			if len(logs) != 3 || logs[2]["accountId"] != owner || logs[2]["bindingResult"] != "hit" {
				t.Fatal("busy handling overwrote the original session owner")
			}
			if policy != "reject" && (logs[1]["bindingResult"] != "temporary-overflow" || logs[1]["accountId"] == owner) {
				t.Fatal("temporary overflow did not follow the saved busy policy")
			}
		})
	}
}

func TestALTAPIStickyFailedOwnerRebinds(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	h := map[string]string{"X-Session-Id": "recoverable-session"}
	altStickyPost(t, e, chat(false), h, 200)
	e.up.mu.Lock()
	failedKey := e.up.calls[0]
	e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
		if key == failedKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":{"message":"temporary upstream failure"}}`)
			return
		}
		writeOK(w, key)
	}
	e.up.mu.Unlock()
	altStickyPost(t, e, chat(false), h, 200)
	altStickyPost(t, e, chat(false), h, 200)
	logs := altStickyLogs(e)
	if len(logs) != 3 || logs[1]["switched"] != true || logs[0]["accountId"] == logs[1]["accountId"] || logs[1]["accountId"] != logs[2]["accountId"] || logs[2]["bindingResult"] != "hit" {
		t.Fatal("recoverable owner failure did not rebind once and retain the new owner")
	}
	if e.up.callCount() != 4 {
		t.Fatal("unexpected retry count during owner recovery")
	}
}

func TestALTAPIStickyConcurrentFirstRequests(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			c.Accounts[i].MaxConc = 20
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			altStickyPost(t, e, chat(false), map[string]string{"X-Session-Id": "concurrent-first"}, 200)
		}()
	}
	close(start)
	wg.Wait()
	e.up.mu.Lock()
	defer e.up.mu.Unlock()
	if len(e.up.calls) != 12 {
		t.Fatal("concurrent requests were not all delivered")
	}
	for _, key := range e.up.calls {
		if key != e.up.calls[0] {
			t.Fatal("concurrent first requests created different owners")
		}
	}
}

func TestALTAPIStickySettingsRoundTripAndIsolation(t *testing.T) {
	e := newALTAPIStickyEnv(t)
	admin := func(method string, body any) *httptest.ResponseRecorder {
		return deployAdmin(t, &deployTestEnv{s: e.srv}, method, "/api/settings?provider=altapi", body)
	}
	get := func() settingsDoc {
		w := admin("GET", nil)
		var out struct {
			Settings settingsDoc `json:"settings"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatal("settings GET failed", w.Code)
		}
		return out.Settings
	}
	before := e.srv.cfg.Get().Clone()
	doc := get()
	doc.Routing.Sticky = false
	doc.Routing.ExplicitTTLSec, doc.Routing.FallbackTTLSec = 180, 90
	doc.Routing.SessionHeaders = []string{"X-Altapi-Custom-Session"}
	doc.Quota.IntervalSec = 120
	// Even a stale whole-console document cannot edit another pool/global block.
	doc.Upstream.RuntimeBase = "http://127.0.0.1:1/v1"
	doc.Logs.RetentionDays = 30
	doc.OpenCode.Enabled = true
	w := admin("PUT", doc)
	if w.Code != 200 {
		t.Fatal("settings PUT failed", w.Code, w.Body.String())
	}
	after := e.srv.cfg.Get().Clone()
	loaded, err := config.Load(filepath.Join(e.srv.dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.ALTAPI, loaded.Get().ALTAPI) || !reflect.DeepEqual(get().Routing, doc.Routing) {
		t.Fatal("ALTAPI settings did not survive GET/disk round-trip")
	}
	// Invalid independent routing must neither publish nor persist a new revision.
	invalid := get()
	invalid.Routing.ExplicitTTLSec = -1
	if w := admin("PUT", invalid); w.Code != 400 || !reflect.DeepEqual(after, e.srv.cfg.Get()) {
		t.Fatal("invalid ALTAPI settings were accepted or changed the current config")
	}
	reloaded, err := config.Load(filepath.Join(e.srv.dataDir, "config.json"))
	if err != nil || !reflect.DeepEqual(after.ALTAPI, reloaded.Get().ALTAPI) {
		t.Fatal("rejected settings changed the persisted ALTAPI block")
	}
	// Exclude the explicitly edited ALTAPI block and revision; everything else is unchanged.
	after.ALTAPI, after.Revision = before.ALTAPI, before.Revision
	if !reflect.DeepEqual(before, after) {
		t.Fatal("saving ALTAPI settings changed another pool or shared configuration")
	}
	for range 2 {
		altStickyPost(t, e, chat(false), map[string]string{"X-Altapi-Custom-Session": "saved-session"}, 200)
	}
	logs := altStickyLogs(e)
	if len(logs) != 2 || logs[0]["accountId"] == logs[1]["accountId"] || logs[1]["bindingResult"] != "not-applicable" || logs[1]["affinityKeyType"] != "custom_header" {
		t.Fatal("saved sticky=false/custom session header did not actually take effect")
	}
}
