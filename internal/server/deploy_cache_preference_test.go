package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestDeployWithCachePreferenceKeepsClientBytes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"model":"m","messages":[]}`, `{"model":"m","messages":[],"cache_preference":64}`},
		{` { "model":"m", "n":9007199254740993 } `, ` { "model":"m", "n":9007199254740993,"cache_preference":64 } `},
		{`{}`, `{"cache_preference":64}`},
		{` { } `, ` { "cache_preference":64} `},
		{`{"model":"m","cache_preference":3}`, `{"model":"m","cache_preference":3}`},
		{`{"cache_preference":null,"model":"m"}`, `{"cache_preference":null,"model":"m"}`},
		{`{"metadata":{"cache_preference":3}}`, `{"metadata":{"cache_preference":3},"cache_preference":64}`},
	} {
		if got := string(deployWithCachePreference([]byte(tc.in), 64)); got != tc.want {
			t.Fatalf("%s: got %s want %s", tc.in, got, tc.want)
		}
		if !json.Valid(deployWithCachePreference([]byte(tc.in), 64)) {
			t.Fatal("invalid JSON", tc.in)
		}
	}
	if got := string(deployWithCachePreference([]byte(`{"model":"m"}`), 0)); got != `{"model":"m"}` {
		t.Fatal("0 must not send the field", got)
	}
}

func TestDeployCachePreferenceDefaultSettingAndClientChoice(t *testing.T) {
	e := newDeployEnv(t)
	body := `{"model":"deepseek-ai/deepseek-v4.1-flash-1017","messages":[{"role":"user","content":"ping"}],"stream":false}`
	if rsp, _ := e.do(t, "/v1/chat/completions", body, nil); rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode)
	}
	if got := e.snapshot()[0].body; got != strings.TrimSuffix(body, "}")+`,"cache_preference":64}` {
		t.Fatal("default cache_preference not added in place", got)
	}
	own := `{"model":"deepseek-ai/deepseek-v4.1-flash-1017","cache_preference":2,"messages":[]}`
	e.do(t, "/v1/chat/completions", own, nil)
	if got := e.snapshot()[1].body; got != own {
		t.Fatal("client cache_preference overridden", got)
	}

	doc := deploySettingsDoc(t, e)
	if doc.Deploy == nil || doc.Deploy.CachePreference == nil || *doc.Deploy.CachePreference != 64 {
		t.Fatal("settings did not expose effective default")
	}
	v := 200
	doc.Deploy.CachePreference = &v
	if w := deployAdmin(t, e, "PUT", "/api/settings?provider=deploy", doc); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	loaded, err := config.Load(filepath.Join(e.s.dataDir, "config.json"))
	if err != nil || loaded.Get().Deploy.EffectiveCachePreference() != 200 || *deploySettingsDoc(t, e).Deploy.CachePreference != 200 {
		t.Fatal("saved cache_preference not persisted", err)
	}
	e.do(t, "/v1/chat/completions", body, nil)
	if got := e.snapshot()[2].body; !strings.HasSuffix(got, `,"cache_preference":200}`) {
		t.Fatal("saved value not used", got)
	}

	for _, bad := range []int{-1, config.DeployMaxCachePreference + 1} {
		doc = deploySettingsDoc(t, e)
		b := bad
		doc.Deploy.CachePreference = &b
		if w := deployAdmin(t, e, "PUT", "/api/settings?provider=deploy", doc); w.Code == 200 {
			t.Fatal("invalid cache_preference accepted", bad)
		}
		if e.s.cfg.Get().Deploy.EffectiveCachePreference() != 200 {
			t.Fatal("rejected save changed memory")
		}
	}

	doc = deploySettingsDoc(t, e)
	zero := 0
	doc.Deploy.CachePreference = &zero
	if w := deployAdmin(t, e, "PUT", "/api/settings?provider=deploy", doc); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if *deploySettingsDoc(t, e).Deploy.CachePreference != 0 {
		t.Fatal("0 not kept distinct from the default")
	}
	e.do(t, "/v1/chat/completions", body, nil)
	if got := e.snapshot()[3].body; got != body {
		t.Fatal("0 still sent cache_preference", got)
	}
}

func TestDeployCachePreferenceSentOnceAcrossRetries(t *testing.T) {
	e := newDeployEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }
	e.mu.Unlock()
	e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, nil)
	calls := e.snapshot()
	if len(calls) != 2 {
		t.Fatal("503 did not retry on the second account", len(calls))
	}
	for _, c := range calls {
		if c.body != `{"model":"native","messages":[],"cache_preference":64}` {
			t.Fatal("attempt body", c.body)
		}
	}
}
