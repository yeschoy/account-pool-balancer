package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

// refusedSystem carries a refused client phrase in the system channel (an
// "arness" self-description and the pi opening) that the prompt adapters and
// the phrase escape both rewrite.
const refusedSystem = `You are an expert coding assistant operating inside pi, a coding agent harness.`

func factoryAmpBody() string {
	return `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"` + refusedSystem + `"}]}}`
}

// TestFactoryAmpSkipsPromptRewrite pins the Amp fast path: an Amp request is
// sent without the string adapters, any other client keeps them, and the
// switch restores the full path for Amp.
func TestFactoryAmpSkipsPromptRewrite(t *testing.T) {
	e := newFactoryEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Factory.SystemMode = config.SystemMagpie
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/v1beta/models/gemini-4-neon:generateContent"
	send := func(amp bool) string {
		t.Helper()
		var h map[string]string
		if amp {
			h = map[string]string{"X-Amp-Thread-Id": "T-amp-1"}
		}
		before := len(e.snapshot())
		rsp, out := e.do(t, path, factoryAmpBody(), h)
		if rsp.StatusCode != 200 {
			t.Fatal(rsp.StatusCode, out)
		}
		calls := e.snapshot()
		if len(calls) != before+1 {
			t.Fatalf("calls = %d", len(calls))
		}
		return calls[len(calls)-1].body
	}
	if body := send(true); !strings.Contains(body, refusedSystem) {
		t.Fatalf("Amp request was rewritten: %s", body)
	}
	if logs := e.s.logs; logs != nil {
		page, _ := logs.Query("requests", nil, time.Time{}, time.Time{}, 1, 0)
		if len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), `"clientFamily":"amp"`) {
			t.Fatalf("Amp request not labelled: %s", page.Items)
		}
	}
	if body := send(false); strings.Contains(body, refusedSystem) {
		t.Fatalf("non-Amp request kept the refused phrase: %s", body)
	}
	off := false
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Factory.AmpSkipPromptRewrite = &off; return nil }); err != nil {
		t.Fatal(err)
	}
	if body := send(true); strings.Contains(body, refusedSystem) {
		t.Fatalf("switch off still skipped rewrite: %s", body)
	}
}

func TestFactoryAmpSettingsRoundTripAndIsolation(t *testing.T) {
	e := newFactoryEnv(t)
	read := func(provider string) settingsDoc {
		t.Helper()
		w := deployAdmin(t, e, "GET", "/api/settings?provider="+provider, nil)
		var response struct {
			Settings settingsDoc `json:"settings"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil {
			t.Fatalf("settings GET: %d %s", w.Code, w.Body.String())
		}
		return response.Settings
	}
	save := func(provider string, doc settingsDoc) {
		t.Helper()
		doc.Revision = e.s.cfg.Get().Revision
		w := deployAdmin(t, e, "PUT", "/api/settings?provider="+provider, doc)
		if w.Code != 200 {
			t.Fatalf("settings PUT: %d %s", w.Code, w.Body.String())
		}
	}
	doc := read("factory")
	if !doc.Factory.EffectiveAmpSkipPromptRewrite() {
		t.Fatal("legacy configs must default to the Amp fast path")
	}
	for _, enabled := range []bool{false, true, false} {
		doc = read("factory")
		doc.Factory.AmpSkipPromptRewrite = &enabled
		save("factory", doc)
		if err := e.s.cfg.Reload(); err != nil {
			t.Fatal(err)
		}
		got := read("factory").Factory
		if got.AmpSkipPromptRewrite == nil || *got.AmpSkipPromptRewrite != enabled || got.EffectiveAmpSkipPromptRewrite() != enabled {
			t.Fatalf("switch did not persist: want %t", enabled)
		}
	}
	// An older client omitting the new field must not re-enable the fast path
	// after an administrator disabled it to recover from a policy rejection.
	doc = read("factory")
	doc.Factory.AmpSkipPromptRewrite = nil
	save("factory", doc)
	if e.s.cfg.Get().Factory.EffectiveAmpSkipPromptRewrite() {
		t.Fatal("omitted field reset the disabled switch")
	}
	doc = read("deploy")
	on := true
	doc.Factory.AmpSkipPromptRewrite = &on
	save("deploy", doc)
	if err := e.s.cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	if read("factory").Factory.EffectiveAmpSkipPromptRewrite() {
		t.Fatal("another pool changed the Factory switch")
	}
}

// TestFactoryAmpPlanKeepsRequiredAdapters: trusted bodies still get droid's
// opening, because Factory refuses a system channel without it.
func TestFactoryAmpPlanKeepsRequiredAdapters(t *testing.T) {
	cfg := config.Default()
	cfg.Factory.SystemMode = config.SystemMagpie
	plan, err := prepareFactoryFor(cfg, "/v1beta/models/gemini-4-neon:generateContent", []byte(factoryAmpBody()), true)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.trustedPrompt || !strings.Contains(string(plan.body), refusedSystem) {
		t.Fatalf("trusted plan rewritten: %s", plan.body)
	}
	untrusted, err := prepareFactoryFor(cfg, "/v1beta/models/gemini-4-neon:generateContent", []byte(factoryAmpBody()), false)
	if err != nil {
		t.Fatal(err)
	}
	if untrusted.trustedPrompt || len(untrusted.body) == 0 {
		t.Fatal("untrusted plan")
	}
	// Both carry the same droid opening line ahead of the caller's text.
	open := func(b []byte) string {
		s := string(b)
		i := strings.Index(s, `"systemInstruction"`)
		if i < 0 {
			t.Fatalf("no systemInstruction: %s", s)
		}
		return s[i : i+80]
	}
	if open(plan.body) != open(untrusted.body) {
		t.Fatalf("opening differs:\n%s\n%s", open(plan.body), open(untrusted.body))
	}
}
