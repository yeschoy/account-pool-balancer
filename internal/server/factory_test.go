package server

import (
	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factory"
	"account-pool-balancer/internal/rules"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func newFactoryEnv(t *testing.T) *deployTestEnv {
	e := newDeployEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Factory.BaseURL = e.up.URL
		r := c.Factory.EffectiveRouting()
		r.Strategy = "ordered"
		r.MaxAccounts = 2
		c.Factory.Routing = &r
		q := c.Factory.EffectiveQuota()
		q.Enabled = false
		c.Factory.Quota = &q
		c.Accounts = []config.Account{{ID: "f1", Provider: config.ProviderFactory, Group: "factory", RuntimeKey: "fk-mock_factory_key_1", Enabled: true, Priority: 1, MaxConc: 4}, {ID: "f2", Provider: config.ProviderFactory, Group: "factory", RuntimeKey: "fk-mock_factory_key_2", Enabled: true, Priority: 2, MaxConc: 4}}
		c.ClientKeys = []config.ClientKey{{ID: "client", Provider: config.ProviderFactory, Group: "factory", KeyHash: config.HashKey("downstream-ms"), Enabled: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.s.stopAltAuto)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/cli/whoami":
			fmt.Fprint(w, `{"userId":"test-user","orgId":"test-org"}`)
		case "/api/billing/limits":
			w.WriteHeader(403)
			fmt.Fprint(w, `{"detail":"not allowed"}`)
		case "/api/llm/o/v1/responses":
			fmt.Fprint(w, `{"id":"resp_factory_1","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`)
		case "/api/llm/a/v1/messages":
			fmt.Fprint(w, `{"type":"message","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn"}`)
		case "/api/llm/g/v1/generate":
			fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1}}`)
		case "/api/llm/o/v1/embeddings":
			fmt.Fprint(w, `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"text-embedding-3-large","usage":{"prompt_tokens":3,"total_tokens":3}}`)
		case "/api/llm/g/v1/countTokens":
			fmt.Fprint(w, `{"totalTokens":42}`)
		default:
			fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`)
		}
	}
	e.mu.Unlock()
	return e
}

// TestFactoryNativeProtocolsAndNoRewrite pins what the gateway owns. Task
// instructions, tool schemas and large integers survive untouched; the client
// identity and the fixed system opening become droid's, because Factory serves
// a subscription's models to droid only (magpie magpie-community/plugins
// packages/factory, droidBody/factoryHeaders).
func TestFactoryNativeProtocolsAndNoRewrite(t *testing.T) {
	e := newFactoryEnv(t)
	cases := []struct{ path, model, target, provider string }{{"/v1/chat/completions", "deepseek-v4.1-flash", "/api/llm/o/v1/chat/completions", "fireworks"}, {"/v1/responses", "gpt-5.4", "/api/llm/o/v1/responses", "openai"}, {"/v1/messages", "claude-haiku-4-5-20251001", "/api/llm/a/v1/messages", "bedrock_anthropic"}}
	for _, v := range cases {
		body := fmt.Sprintf(`{ "model":%q,"messages":[{"role":"system","content":"Original user instructions"},{"role":"user","content":"test"}],"tools":[{"unknown":9007199254740993}] }`, v.model)
		rsp, out := e.do(t, v.path, body, map[string]string{"X-Api-Key": "downstream-secret", "X-Factory-Client": "caller", "X-Client-Version": "spoof", "X-Factory-Org-Id": "other-org", "X-Session-Id": "caller-session"})
		if rsp.StatusCode != 200 {
			t.Fatal(v.path, rsp.StatusCode, out)
		}
		calls := e.snapshot()
		c := calls[len(calls)-1]
		h := c.header
		if c.path != v.target || c.auth != "Bearer fk-mock_factory_key_1" {
			t.Fatal("native root/auth changed", v.path)
		}
		if v.path == "/v1/messages" && c.apiKey != "placeholder" || v.path != "/v1/messages" && c.apiKey != "" {
			t.Fatal("x-api-key wrong for the route", v.path, c.apiKey)
		}
		if h.Get("X-Api-Provider") != v.provider || h.Get("X-Factory-Client") != "cli" || h.Get("X-Client-Version") != factory.DroidVersion || h.Get("X-Factory-Org-Id") != "" || h.Get("X-Session-Id") == "caller-session" || h.Get("User-Agent") != "factory-cli/"+factory.DroidVersion || h.Get("X-Factory-Request-Surface") != config.FactorySurfaceTUI || h.Get("X-Provider-Routing-Source") != factory.RoutingSourceConfiguredOrder || h.Get("OpenAI-Platform") != "" {
			t.Fatalf("client identity not droid's: %v", h)
		}
		if tp := h.Get("Traceparent"); len(tp) != 55 || tp[:3] != "00-" || tp[52:] != "-01" {
			t.Fatalf("traceparent not W3C: %q", tp)
		}
		if h.Get("Accept") != "application/json" || h.Get("Content-Type") != "application/json" {
			t.Fatal("wire content headers", h)
		}
		if !strings.Contains(c.body, `"unknown":9007199254740993`) || !strings.Contains(c.body, "Original user instructions") {
			t.Fatal("caller content rewritten", c.body)
		}
	}
	before := len(e.snapshot())
	for _, path := range []string{"/v1/messages/extra", "/v1/responses/extra", "/v1/embeddings/extra"} {
		rsp, _ := e.do(t, path, `{"model":"deepseek-v4.1-flash"}`, nil)
		if rsp.StatusCode != 404 {
			t.Fatal("unsupported accepted", path)
		}
	}
	rsp, _ := e.do(t, "/v1/chat/completions", `{"model":"gpt-5.4"}`, nil)
	if rsp.StatusCode != 400 || len(e.snapshot()) != before {
		t.Fatal("wrong protocol reached upstream")
	}
}
func TestFactoryPolicyCannotRotateOrChangeHealth(t *testing.T) {
	e := newFactoryEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.FactoryErrorRules = []rules.ErrorRule{{ID: "force-switch", Scope: rules.ScopeAccount, Action: rules.ActionSwitchAccount, When: rules.Match{Statuses: []int{403}}}}
		c.FactoryRetryRules = []rules.RetryRule{}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(403)
		fmt.Fprint(w, `{"code":"content_policy_rejected","detail":"This request was blocked by Factory's usage policy check."}`)
	}
	e.mu.Unlock()
	rsp, out := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[]}`, nil)
	if rsp.StatusCode != 403 || len(e.snapshot()) != 1 {
		t.Fatal("policy rotated", rsp.StatusCode, out)
	}
	v := e.s.pool.Views(e.s.cfg.Get())["f1"]
	if v.Status != "ok" || v.Samples != 0 {
		t.Fatal("policy degraded credential", v)
	}
}
func TestFactoryManagementRoundTripAndQuotaPermissions(t *testing.T) {
	e := newFactoryEnv(t)
	before := e.s.cfg.Get().Clone()
	w := deployAdmin(t, e, "GET", "/api/settings?provider=factory", nil)
	var doc struct {
		Settings settingsDoc `json:"settings"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &doc) != nil {
		t.Fatal("settings GET")
	}
	d := doc.Settings
	d.Routing.Strategy = "weighted"
	d.Routing.ExplicitTTLSec = 180
	d.Factory.DetailLog = true
	d.Factory.InferenceURL = e.up.URL
	d.Factory.RequestSurface = config.FactorySurfaceExec
	d.Factory.OpenAIPlatform = "org-roundtrip"
	d.Factory.ProxyPool = []string{"http://p1.example:8080", "socks5://p2.example:1080"}
	d.Factory.UserAgents = []string{"factory-cli/1.15.0", "factory-cli/1.16.0"}
	d.Factory.UserAgent = "factory-cli/1.15.0"
	d.Factory.ManagementUserAgent = "Bun/1.5.0"
	d.Quota.Enabled = false
	d.ErrorRules = []rules.ErrorRule{}
	d.RetryRules = []rules.RetryRule{}
	w = deployAdmin(t, e, "PUT", "/api/settings?provider=factory", d)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	got := e.s.cfg.Get()
	if got.Factory.EffectiveRouting().Strategy != "weighted" || !got.Factory.DetailLog || len(got.FactoryErrorRules) != 0 || got.Factory.InferenceURL != e.up.URL || got.Factory.EffectiveRequestSurface() != config.FactorySurfaceExec || got.Factory.OpenAIPlatform != "org-roundtrip" || len(got.Factory.ProxyPool) != 2 || len(got.Factory.UserAgents) != 2 || got.Factory.EffectiveUserAgent(config.Account{}) != "factory-cli/1.15.0" || got.Factory.EffectiveManagementUserAgent() != "Bun/1.5.0" {
		t.Fatal("settings not applied")
	}
	if !reflect.DeepEqual(before.Deploy, got.Deploy) || !reflect.DeepEqual(before.Routing, got.Routing) || !reflect.DeepEqual(before.Logs, got.Logs) {
		t.Fatal("cross pool settings")
	}
	if err := e.s.Reload(); err != nil {
		t.Fatal(err)
	}
	if e.s.cfg.Get().Factory.EffectiveRouting().ExplicitTTLSec != 180 || e.s.cfg.Get().Factory.EffectiveRequestSurface() != config.FactorySurfaceExec || e.s.cfg.Get().Factory.OpenAIPlatform != "org-roundtrip" || e.s.cfg.Get().Factory.InferenceURL != e.up.URL || len(e.s.cfg.Get().Factory.ProxyPool) != 2 || len(e.s.cfg.Get().Factory.UserAgents) != 2 {
		t.Fatal("reload lost value")
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/f1/refresh-quota", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"quotaRefreshed":false`) || !strings.Contains(w.Body.String(), `unsupported-credential`) {
		t.Fatal("quota permission hidden", w.Code, w.Body.String())
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/f1/test", map[string]any{"inference": true})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatal("test", w.Code, w.Body.String())
	}
	if err := e.s.saveMetadata(true); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(e.s.dataDir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { loaded.stopAltAuto(); loaded.stopPerch(); loaded.logs.Close() })
	if q := loaded.quota.Get("f1"); q == nil || q.Factory == nil || q.Factory.QuotaState != "unsupported-credential" {
		t.Fatal("persisted quota missing")
	}
}

// The 1.14.0 capture's client surface and OpenAI organization are settings,
// not hard-coded constants: exec adds the exec output format, and the OpenAI
// platform token appears only on the OpenAI route when configured.
func TestFactoryCaptureSurfaceAndOpenAIPlatform(t *testing.T) {
	e := newFactoryEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Factory.RequestSurface = config.FactorySurfaceExec
		c.Factory.OpenAIPlatform = "org-captured"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	rsp, out := e.do(t, "/v1/responses", `{"model":"gpt-5.4","input":"hi"}`, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode, out)
	}
	h := e.snapshot()[0].header
	if h.Get("X-Factory-Request-Surface") != config.FactorySurfaceExec || h.Get("X-Factory-Exec-Output-Format") != "json" || h.Get("OpenAI-Platform") != "org-captured" {
		t.Fatalf("captured surface/platform not sent: %v", h)
	}
	rsp, out = e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode, out)
	}
	q := e.snapshot()[1].header
	if q.Get("OpenAI-Platform") != "" || q.Get("X-Factory-Exec-Output-Format") != "json" {
		t.Fatalf("platform leaked to a non-OpenAI route: %v", q)
	}
}

// The 1.14.0 capture pins the model plane per protocol: DeepSeek and Kimi are
// OpenAI Chat on /api/llm/o/v1/chat/completions, with baseten and databricks
// as their x-api-provider. Gemini is Google generate on its own route; it
// must not be configured as a chat route that would be sent to o/v1.
func TestFactoryCaptureModelPaths(t *testing.T) {
	e := newFactoryEnv(t)
	for _, c := range []struct{ model, wantPath, wantProvider string }{
		{"deepseek-v4.1-flash-1017", "/api/llm/o/v1/chat/completions", "baseten"},
		{"kimi-k3.1-pro", "/api/llm/o/v1/chat/completions", "databricks"},
	} {
		rsp, out := e.do(t, "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, c.model), nil)
		if rsp.StatusCode != 200 {
			t.Fatal(c.model, rsp.StatusCode, out)
		}
		call := e.snapshot()[len(e.snapshot())-1]
		if call.path != c.wantPath || call.header.Get("X-Api-Provider") != c.wantProvider {
			t.Fatalf("%s: path=%s provider=%s want=%s/%s", c.model, call.path, call.header.Get("X-Api-Provider"), c.wantPath, c.wantProvider)
		}
	}
	if m, ok := e.s.cfg.Get().Factory.Model("gemini-4-carbon"); !ok || m.Protocol != "generate" || m.Upstream != "google" {
		t.Fatal("Gemini must be configured on the generate route, not chat")
	}
}

// The Gemini native surface maps Google's model/action path to Factory's one
// generated endpoint, puts the path model in the body, opens droid's system
// instruction and relays the Google response unchanged.
func TestFactoryGeminiNativeGeneratePassThrough(t *testing.T) {
	e := newFactoryEnv(t)
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"maxOutputTokens":16}}`
	req := httptest.NewRequest("POST", "/v1beta/models/gemini-4-carbon:generateContent", strings.NewReader(body))
	req.Header.Set("X-Goog-Api-Key", "downstream-ms")
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"finishReason":"STOP"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	call := e.snapshot()[0]
	if call.path != "/api/llm/g/v1/generate" || !strings.Contains(call.body, `"model":"gemini-4-carbon"`) || !strings.Contains(call.body, factory.DroidLine) {
		t.Fatalf("wrong Gemini upstream: path=%s body=%s", call.path, call.body)
	}
	h := call.header
	if h.Get("X-Api-Provider") != "google" || h.Get("User-Agent") != "factory-cli/"+factory.DroidVersion || h.Get("Accept") != "*/*" || h.Get("X-Goog-Api-Key") != "" {
		t.Fatalf("Gemini identity wrong: %v", h)
	}
}

func TestFactoryGeminiStreamTerminatesOnFinishReason(t *testing.T) {
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"O\"}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"K\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":1}}\n\n")
	}
	e.mu.Unlock()
	req := httptest.NewRequest("POST", "/v1beta/models/gemini-4-carbon:streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("X-Goog-Api-Key", "downstream-ms")
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "finishReason") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	call := e.snapshot()[0]
	if call.path != "/api/llm/g/v1/generate" {
		t.Fatal("stream query leaked to Factory", call.path)
	}
}

func TestFactoryGeminiModelTestAndProtocolGuard(t *testing.T) {
	e := newFactoryEnv(t)
	if got := e.s.cfg.Get().Factory.Models[0].Protocol; got == "" {
		t.Fatal("models missing")
	}
	w := deployAdmin(t, e, "POST", "/api/accounts/f1/test", map[string]any{"inference": true, "model": "gemini-4-carbon"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatal("Gemini model test", w.Code, w.Body.String())
	}
	// A configured chat model is not a Gemini generate route.
	req := httptest.NewRequest("POST", "/v1beta/models/gpt-5.4:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("X-Goog-Api-Key", "downstream-ms")
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "not a Google generate route") {
		t.Fatal("protocol guard", rec.Code, rec.Body.String())
	}
	before := len(e.snapshot())
	rsp, out := e.do(t, "/v1/chat/completions", `{"model":"gemini-4-carbon","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rsp.StatusCode != 400 || len(e.snapshot()) != before || !strings.Contains(out, "generateContent") {
		t.Fatal("Gemini chat route should be refused locally", rsp.StatusCode, out)
	}
}

func TestFactoryGeminiNonStreamAggregatesSSE(t *testing.T) {
	e := newFactoryEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hello\"}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\" world\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2}}\n\n")
	}
	e.mu.Unlock()
	req := httptest.NewRequest("POST", "/v1beta/models/gemini-4-carbon:generateContent", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("X-Goog-Api-Key", "downstream-ms")
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" || !strings.Contains(rec.Body.String(), `"text":"Hello world"`) || !strings.Contains(rec.Body.String(), `"finishReason":"STOP"`) {
		t.Fatal(rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
}

func TestFactoryGeminiStreamWrapsJSON(t *testing.T) {
	e := newFactoryEnv(t)
	req := httptest.NewRequest("POST", "/v1beta/models/gemini-4-carbon:streamGenerateContent?alt=sse", strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	req.Header.Set("X-Goog-Api-Key", "downstream-ms")
	rec := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") || !strings.HasPrefix(body, "data: ") || !strings.Contains(body, `"finishReason":"STOP"`) {
		t.Fatal(rec.Code, rec.Header().Get("Content-Type"), body)
	}
}

func TestFactoryEmbeddings(t *testing.T) {
	e := newFactoryEnv(t)
	body := `{"model":"text-embedding-3-large","input":"hello"}`
	rsp, out := e.do(t, "/v1/embeddings", body, nil)
	if rsp.StatusCode != 200 || !strings.Contains(out, `"embedding":[0.1,0.2]`) {
		t.Fatal(rsp.StatusCode, out)
	}
	call := e.snapshot()[0]
	if call.path != "/api/llm/o/v1/embeddings" || call.header.Get("X-Api-Provider") != "openai" || call.header.Get("User-Agent") != "factory-cli/"+factory.DroidVersion {
		t.Fatal("embeddings route", call.path, call.header)
	}
}

func TestFactoryGeminiCountTokens(t *testing.T) {
	e := newFactoryEnv(t)
	body := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
	rsp, out := e.do(t, "/v1beta/models/gemini-4-carbon:countTokens", body, nil)
	if rsp.StatusCode != 200 || !strings.Contains(out, `"totalTokens":42`) {
		t.Fatal(rsp.StatusCode, out)
	}
	call := e.snapshot()[0]
	if !strings.Contains(call.body, `"model":"gemini-4-carbon"`) {
		t.Fatalf("count body: %s", call.body)
	}
	if call.path != "/api/llm/g/v1/countTokens" {
		t.Fatal("count path", call.path)
	}
}

func TestFactoryAutoVirtualModel(t *testing.T) {
	e := newFactoryEnv(t)
	body := `{"model":"auto","messages":[{"role":"user","content":"hi"}]}`
	rsp, out := e.do(t, "/v1/chat/completions", body, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode, out)
	}
	call := e.snapshot()[0]
	if call.path != "/api/llm/o/v1/chat/completions" || !strings.Contains(call.body, `"model":"glm-5.3"`) || strings.Contains(call.body, `"model":"auto"`) {
		t.Fatalf("auto chat: path=%s body=%s", call.path, call.body)
	}
	body = `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	rsp, out = e.do(t, "/v1beta/models/auto:generateContent", body, nil)
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode, out)
	}
	call = e.snapshot()[1]
	if call.path != "/api/llm/g/v1/generate" || !strings.Contains(call.body, `"model":"gemini-4-argon"`) {
		t.Fatalf("auto gemini: path=%s body=%s", call.path, call.body)
	}
}

func TestFactoryStripsClientFingerprintAndAddressHeaders(t *testing.T) {
	e := newFactoryEnv(t)
	body := `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`
	_, out := e.do(t, "/v1/chat/completions", body, map[string]string{
		"X-Forwarded-For": "203.0.113.9", "X-Real-IP": "203.0.113.9",
		"X-Stainless-OS": "MacOS", "X-Stainless-Lang": "js",
		"Sec-CH-UA": `"Chromium"`, "Origin": "https://leak.example",
		"Accept-Language": "zh-CN", "OpenAI-Organization": "org-leak",
	})
	if !strings.Contains(out, "OK") {
		t.Fatal(out)
	}
	h := e.snapshot()[0].header
	for _, name := range []string{"X-Forwarded-For", "X-Real-IP", "X-Stainless-OS", "X-Stainless-Lang", "Sec-Ch-Ua", "Origin", "Accept-Language", "Openai-Organization"} {
		if h.Get(name) != "" {
			t.Fatalf("%s leaked upstream as %q", name, h.Get(name))
		}
	}
	// The Anthropic path gets droid's fixed protocol version, not the caller's.
	_, out = e.do(t, "/v1/messages", `{"model":"claude-haiku-4-5-20251001","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"Anthropic-Version": "2099-01-01"})
	if !strings.Contains(out, "OK") {
		t.Fatal(out)
	}
	h = e.snapshot()[1].header
	if h.Get("Anthropic-Version") != "2023-06-01" {
		t.Fatalf("anthropic version = %q", h.Get("Anthropic-Version"))
	}
}

func TestFactoryTransportPinsHTTP11(t *testing.T) {
	e := newFactoryEnv(t)
	tr := e.s.factoryTransportFor("", e.s.cfg.Get().Factory.EffectiveRouting(), "factory-http11-test")
	if tr.ForceAttemptHTTP2 {
		t.Fatal("Factory transport must stay on HTTP/1.1 like the captured Connection header")
	}
}
