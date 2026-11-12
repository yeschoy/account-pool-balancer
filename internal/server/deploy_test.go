package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/rules"
)

// Keep the Deploy fixture independent of providers not yet released online.
// Requests still run through the real shared scheduler and HTTP handlers.
type deployTestCall struct {
	path, auth, apiKey, body string
	header                   http.Header
}
type deployTestEnv struct {
	s         *Server
	front, up *httptest.Server
	mu        sync.Mutex
	calls     []deployTestCall
	handler   http.HandlerFunc
}

func (e *deployTestEnv) snapshot() []deployTestCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]deployTestCall(nil), e.calls...)
}
func (e *deployTestEnv) do(t *testing.T, path, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.front.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer downstream-ms")
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}
func newDeployEnv(t *testing.T) *deployTestEnv {
	t.Helper()
	e := &deployTestEnv{}
	e.up = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		e.mu.Lock()
		e.calls = append(e.calls, deployTestCall{path: r.URL.Path, auth: r.Header.Get("Authorization"), apiKey: r.Header.Get("X-Api-Key"), body: string(b), header: r.Header.Clone()})
		h := e.handler
		e.mu.Unlock()
		if h != nil {
			h(w, r)
			return
		}
		w.WriteHeader(503)
	}))
	t.Cleanup(e.up.Close)
	var err error
	e.s, err = New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.s.stopPerch(); e.s.logs.Close() })
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Deploy.BaseURL = e.up.URL + "/api/v1"
		r := c.Deploy.EffectiveRouting()
		r.Strategy = "ordered"
		r.MaxAccounts = 2
		c.Deploy.Routing = &r
		q := c.Deploy.EffectiveQuota()
		q.Enabled = false
		c.Deploy.Quota = &q
		c.Accounts = []config.Account{
			{ID: "d1", Provider: config.ProviderDeploy, Group: "deploy", RuntimeKey: "mock_deploy_secret_1", Enabled: true, Priority: 1, MaxConc: 4},
			{ID: "d2", Provider: config.ProviderDeploy, Group: "deploy", RuntimeKey: "mock_deploy_secret_2", Enabled: true, Priority: 2, MaxConc: 4},
		}
		c.ClientKeys = []config.ClientKey{{ID: "client", Provider: config.ProviderDeploy, Group: "deploy", KeyHash: config.HashKey("downstream-ms"), Enabled: true}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/models") {
			fmt.Fprintf(w, `{"data":[{"id":%q},{"id":"zai-org/glm-5.3-flash"}]}`, config.DeployDefaultModel)
			return
		}
		fmt.Fprint(w, `{"id":"mock-completion","choices":[{"message":{"content":null,"reasoning_content":"thinking"},"finish_reason":"length"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"prompt_tokens_details":{"cached_tokens":2}}}`)
	}
	e.mu.Unlock()
	e.front = httptest.NewServer(e.s.Handler())
	t.Cleanup(e.front.Close)
	for !e.s.logs.Ready() {
		time.Sleep(time.Millisecond)
	}
	return e
}
func deployAdmin(t *testing.T, e *deployTestEnv, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, path, bytes.NewReader(b))
	login := httptest.NewRecorder()
	sess := e.s.admin.create(login, httptest.NewRequest("POST", "/api/login", nil))
	for _, c := range login.Result().Cookies() {
		r.AddCookie(c)
	}
	r.Header.Set("X-CSRF-Token", sess.csrf)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, r)
	return w
}
func deploySettingsDoc(t *testing.T, e *deployTestEnv) settingsDoc {
	t.Helper()
	w := deployAdmin(t, e, "GET", "/api/settings?provider=deploy", nil)
	var out struct {
		Settings settingsDoc `json:"settings"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatal("settings GET", w.Code, w.Body.String())
	}
	return out.Settings
}

func TestDeployNativeChatAndUnsupportedPaths(t *testing.T) {
	e := newDeployEnv(t)
	body := `{ "model":"deepseek-ai/deepseek-v4.1-flash-1017", "messages":[{"role":"user","content":"ping"}],"tools":[{"type":"function","function":{"name":"native_tool","parameters":{"type":"object","properties":{"z":{"type":"integer"},"a":{"type":"string"}}}}}],"reasoning_effort":"high","extra":{"big":9007199254740993} }`
	rsp, out := e.do(t, "/v1/chat/completions", body, map[string]string{"User-Agent": "test-client/1", "X-Api-Key": "downstream-must-not-leak"})
	if rsp.StatusCode != 200 || !strings.Contains(out, `"reasoning_content":"thinking"`) {
		t.Fatal("native result", rsp.StatusCode, out)
	}
	calls := e.snapshot()
	if len(calls) != 1 || calls[0].path != "/api/v1/chat/completions" || calls[0].body != body || calls[0].auth != "Bearer mock_deploy_secret_1" || calls[0].apiKey != "" {
		t.Fatal("native request/root/auth changed")
	}
	for _, path := range []string{"/v1/responses", "/v1/messages", "/v1/embeddings", "/v1/chat/completions/other", "/v1/models"} {
		rsp, _ = e.do(t, path, body, nil)
		if rsp.StatusCode != 404 {
			t.Fatal("unsupported path not refused", path, rsp.StatusCode)
		}
	}
	if len(e.snapshot()) != 1 {
		t.Fatal("unsupported path reached upstream")
	}
}
func TestDeployErrorRetryDecisionsAndCooldown(t *testing.T) {
	for _, st := range []int{401, 402, 403, 405, 429, 503} {
		t.Run(fmt.Sprint(st), func(t *testing.T) {
			e := newDeployEnv(t)
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Retry-After", "172800")
				w.WriteHeader(st)
				fmt.Fprint(w, `{"message":"Your Organization is currently paused. Please check your account balance"}`)
			}
			e.mu.Unlock()
			rsp, _ := e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, nil)
			want := 2
			if st == 403 || st == 405 || st == 429 {
				want = 1
			}
			if len(e.snapshot()) != want {
				t.Fatalf("status %d rotated %d accounts, want %d", st, len(e.snapshot()), want)
			}
			views := e.s.pool.Views(e.s.cfg.Get())
			if views["d1"].Status != "cooldown" && st != 403 {
				t.Fatal("error rule did not affect scheduler", st, views["d1"].Status)
			}
			if st == 429 {
				if rsp.StatusCode != 429 {
					t.Fatal("rate status lost", rsp.StatusCode)
				}
				if views["d1"].State.CooldownUntil.Before(time.Now().Add(47 * time.Hour)) {
					t.Fatal("Retry-After shortened")
				}
			}
			if st == 405 && rsp.StatusCode != 503 {
				t.Fatal("organization pause not mapped", rsp.StatusCode)
			}
		})
	}
}
func TestDeployCustomRulesAndAttemptLimit(t *testing.T) {
	e := newDeployEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.DeployErrorRules = []rules.ErrorRule{{ID: "custom-header", Scope: rules.ScopeAccount, Action: rules.ActionCooldown, When: rules.Match{Statuses: []int{418}, Headers: map[string]string{"X-Deploy-Rule": "yes"}}, Reset: &rules.Reset{Fallback: "2m"}}}
		c.DeployRetryRules = []rules.RetryRule{{ID: "custom-stop", Decision: rules.DecisionStop, When: rules.Match{Statuses: []int{418}}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Deploy-Rule", "yes")
		w.WriteHeader(418)
		fmt.Fprint(w, `{"message":"custom"}`)
	}
	e.mu.Unlock()
	e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, nil)
	if len(e.snapshot()) != 1 || e.s.pool.Views(e.s.cfg.Get())["d1"].Status != "cooldown" {
		t.Fatal("editable stop or header error rule ignored")
	}
	// Remove stop: another eligible account is now selected, within MaxAccounts.
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error {
		c.DeployRetryRules = []rules.RetryRule{}
		c.Deploy.Routing.MaxAccounts = 1
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.s.pool.Recover("d1")
	before := len(e.snapshot())
	e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, nil)
	if len(e.snapshot()) != before+1 {
		t.Fatal("attempt limit ignored")
	}
}
func TestDeploySettingsPersistWithoutCrossPoolMutation(t *testing.T) {
	e := newDeployEnv(t)
	before := e.s.cfg.Get()
	doc := deploySettingsDoc(t, e)
	if doc.Deploy == nil || doc.Deploy.QuotaState != "not-connected" || doc.Deploy.DetailLog == nil || *doc.Deploy.DetailLog {
		t.Fatal("capability/default missing")
	}
	doc.Routing.Strategy = "weighted"
	doc.Routing.DirectMaxConns = 9
	doc.Routing.ProxyMaxConns = 3
	doc.Routing.SessionHeaders = []string{"X-Deploy-Session"}
	doc.Quota.Concurrency = 4
	doc.Quota.IntervalSec = 90
	doc.Deploy.ModelSyncSec = 120
	*doc.Deploy.DetailLog = true
	doc.ErrorRules = []rules.ErrorRule{}
	doc.RetryRules = []rules.RetryRule{}
	doc.Pricing.Models[0].Input = 0.11
	// A stale/all-pool client cannot overwrite global/shared blocks via Deploy.
	doc.Logs.RetentionDays = 30
	doc.Upstream.RuntimeBase = "http://127.0.0.1:1/v1"
	doc.OpenCode.Enabled = true
	w := deployAdmin(t, e, "PUT", "/api/settings?provider=deploy", doc)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	after := e.s.cfg.Get()
	if !reflect.DeepEqual(before.Routing, after.Routing) || !reflect.DeepEqual(before.Quota, after.Quota) || !reflect.DeepEqual(before.Logs, after.Logs) || !reflect.DeepEqual(before.Upstream, after.Upstream) || !reflect.DeepEqual(before.OpenCode, after.OpenCode) || !reflect.DeepEqual(before.ErrorRules, after.ErrorRules) || !reflect.DeepEqual(before.Pricing, after.Pricing) {
		t.Fatal("Deploy save changed another/shared pool")
	}
	if after.Deploy.Routing.Strategy != "weighted" || after.Deploy.Quota.Concurrency != 4 || !after.Deploy.DetailLog || len(after.DeployErrorRules) != 0 || len(after.DeployRetryRules) != 0 {
		t.Fatal("own settings ignored")
	}
	loaded, err := config.Load(filepath.Join(e.s.dataDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Deploy, loaded.Get().Deploy) {
		t.Fatal("disk roundtrip failed")
	}
	other := doc
	other.Revision = after.Revision
	*other.Deploy.DetailLog = false
	w = deployAdmin(t, e, "PUT", "/api/settings?provider=opendesign", other)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if !e.s.cfg.Get().Deploy.DetailLog {
		t.Fatal("other pool overwrote Deploy")
	}
}
func TestDeployImportIsolationAndAdminGuards(t *testing.T) {
	e := newDeployEnv(t)
	w := deployAdmin(t, e, "POST", "/api/accounts/import", map[string]any{"provider": "deploy", "text": "opaque_new_secret,New\nopaque_new_secret,Duplicate"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"added":1`) || !strings.Contains(w.Body.String(), `"unchanged":1`) {
		t.Fatal("opaque import dedup", w.Code, w.Body.String())
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/import", map[string]any{"provider": "deploy", "text": "provider:opendesign,sk-amr-cross-pool,Foreign"})
	if w.Code != 400 {
		t.Fatal("console import escaped Deploy scope")
	}
	res := e.s.importOne(config.ImportKey{Provider: config.ProviderDeploy, Group: "deploy"}, importItem{Provider: config.ProviderOpenDesign, RuntimeKey: "sk-amr-foreign"}, time.Now())
	if res.Status != "error" {
		t.Fatal("Deploy import key crossed provider")
	}
	res = e.s.importOne(config.ImportKey{Provider: config.ProviderOpenDesign, Group: "default"}, importItem{Provider: config.ProviderDeploy, RuntimeKey: "opaque_different_secret"}, time.Now())
	if res.Status != "error" {
		t.Fatal("foreign import key crossed into Deploy")
	}
	w = httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, httptest.NewRequest("PUT", "/api/settings?provider=deploy", strings.NewReader(`{}`)))
	if w.Code != 401 {
		t.Fatal("missing auth not rejected")
	}
	r := httptest.NewRequest("PUT", "/api/settings?provider=deploy", strings.NewReader(`{}`))
	login := httptest.NewRecorder()
	e.s.admin.create(login, r)
	for _, c := range login.Result().Cookies() {
		r.AddCookie(c)
	}
	w = httptest.NewRecorder()
	e.s.Handler().ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("CSRF not required")
	}
}
func TestDeployCatalogAndInferenceTestsAreSeparate(t *testing.T) {
	e := newDeployEnv(t)
	w := deployAdmin(t, e, "POST", "/api/accounts/d1/test", map[string]any{"inference": true})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string]map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result["inference"]["ok"] != true || result["runtime"]["note"] == nil || result["quota"]["state"] != "not-connected" {
		t.Fatal("test conflated catalog and permission", w.Body.String())
	}
	calls := e.snapshot()
	if len(calls) != 2 {
		t.Fatal("test made unexpected calls", len(calls))
	}
	var body map[string]any
	json.Unmarshal([]byte(calls[1].body), &body)
	if body["model"] != config.DeployDefaultModel || body["max_tokens"] != float64(256) {
		t.Fatal("invalid test model/budget")
	}
	w = deployAdmin(t, e, "POST", "/api/accounts/d1/refresh-quota", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"quotaRefreshed":false`) {
		t.Fatal("catalog refresh claimed balance", w.Code, w.Body.String())
	}
	w = deployAdmin(t, e, "GET", "/api/accounts?provider=deploy", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"quotaFresh":false`) || !strings.Contains(w.Body.String(), `"catalogFresh":true`) {
		t.Fatal("bad account projection", w.Code, w.Body.String())
	}
}
func TestDeployStreamNoReplayAndDetailsPrivacy(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			e := newDeployEnv(t)
			_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				c.Logs.Detail = !enabled
				c.Deploy.DetailLog = enabled
				c.Deploy.Routing.SessionHeaders = []string{"X-Deploy-Session"}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"id\":\"stream\",\"choices\":[{\"delta\":{\"reasoning_content\":\"native\"}}]}\n\n")
				w.(http.Flusher).Flush()
				fmt.Fprint(w, "data: {\"error\":{\"message\":\"upstream failed\"}}\n\n")
			}
			e.mu.Unlock()
			rsp, out := e.do(t, "/v1/chat/completions", `{"model":"native","stream":true,"messages":[{"role":"user","content":"hi"}]}`, map[string]string{"X-Deploy-Session": "private-session"})
			if len(e.snapshot()) != 1 || !strings.Contains(out, "reasoning_content") {
				t.Fatal("stream dropped or replayed")
			}
			filters := map[string]string{"requestId": rsp.Header.Get("X-Request-Id")}
			details, _ := e.s.logs.Query("details", filters, time.Time{}, time.Time{}, 10, 0)
			if (len(details.Items) > 0) != enabled {
				t.Fatal("independent detail switch ignored")
			}
			for _, kind := range []string{"requests", "errors", "details"} {
				page, _ := e.s.logs.Query(kind, filters, time.Time{}, time.Time{}, 10, 0)
				for _, row := range page.Items {
					for _, secret := range []string{"mock_deploy_secret_1", "downstream-ms", "private-session"} {
						if strings.Contains(string(row), secret) {
							t.Fatal("secret in logs", kind)
						}
					}
				}
			}
		})
	}
}
func TestDeployNeverStripsToolsOrReplaysUnknownOutcomes(t *testing.T) {
	for _, kind := range []string{"unsupported-tool", "unknown-outcome"} {
		t.Run(kind, func(t *testing.T) {
			e := newDeployEnv(t)
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				if kind == "unknown-outcome" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err == nil {
						conn.Close()
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(400)
				fmt.Fprint(w, `{"error":{"message":"[code=unsupported_tool] unsupported hosted tool type \\\"web_search\\\""}}`)
			}
			e.mu.Unlock()
			body := `{"model":"native","messages":[],"tools":[{"type":"web_search"}]}`
			rsp, _ := e.do(t, "/v1/chat/completions", body, nil)
			calls := e.snapshot()
			if len(calls) != 1 || calls[0].body != body || rsp.StatusCode < 400 {
				t.Fatal("request replayed/rewritten or unknown outcome succeeded")
			}
		})
	}
}
func TestDeployTruncatedStreamIsNotSuccess(t *testing.T) {
	e := newDeployEnv(t)
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\\n\\n")
	}
	e.mu.Unlock()
	rsp, out := e.do(t, "/v1/chat/completions", `{"model":"native","messages":[],"stream":true}`, nil)
	if rsp.StatusCode != 200 || strings.Contains(out, "[DONE]") || len(e.snapshot()) != 1 {
		t.Fatal("committed stream changed or replayed")
	}
	page, _ := e.s.logs.Query("requests", map[string]string{"requestId": rsp.Header.Get("X-Request-Id")}, time.Time{}, time.Time{}, 10, 0)
	if len(page.Items) != 1 || !strings.Contains(string(page.Items[0]), `"result":"failed"`) {
		t.Fatal("truncated stream reported success")
	}
}
func TestDeployDoesNotFollowInferenceRedirects(t *testing.T) {
	e := newDeployEnv(t)
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }
	e.mu.Unlock()
	e.do(t, "/v1/chat/completions", `{"model":"native","messages":[]}`, nil)
	if hits.Load() != 0 {
		t.Fatal("credential followed redirect")
	}
}
