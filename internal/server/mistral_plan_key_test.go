package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

// Issue #7216: a subscription (CHAT/Vibe) key bills a request to the spent
// $15 api_budget unless the body carries metadata.call_source, so a gateway
// gets 402 billing_api_budget_exhausted while the $150 vibe_budget is unused.

func TestMistralPlanKeyAppendOnlySplice(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		applied    bool
		want       string
	}{
		{"absent metadata", `{"model":"m","messages":[]}`, true,
			`{"model":"m","messages":[],"metadata":{"call_source":"vibe_code"}}`},
		{"caller metadata object untouched", `{"model":"m","metadata":{"call_source":"other","store":true}}`, false,
			`{"model":"m","metadata":{"call_source":"other","store":true}}`},
		{"caller explicit null untouched", `{"model":"m","metadata":null}`, false,
			`{"model":"m","metadata":null}`},
		{"trailing whitespace stays outside the object", "{\"model\":\"m\"}\n", true,
			"{\"model\":\"m\",\"metadata\":{\"call_source\":\"vibe_code\"}}\n"},
		{"already prepared once", `{"model":"m","metadata":{"call_source":"vibe_code"}}`, false,
			`{"model":"m","metadata":{"call_source":"vibe_code"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tpl := config.MistralChatMetadata{Mode: config.MistralChatMetadataAllKeys, CallSource: config.MistralCallSourceVibeCode}
			out, applied := appendMistralPlanKeyMetadata([]byte(tc.body), tpl)
			if applied != tc.applied || string(out) != tc.want {
				t.Fatalf("applied=%v got %q want %q", applied, out, tc.want)
			}
		})
	}
}

func TestMistralPlanKeyClientNameTemplate(t *testing.T) {
	out, applied := appendMistralPlanKeyMetadata([]byte(`{"model":"m"}`), config.MistralChatMetadata{
		Mode: config.MistralChatMetadataAllKeys, CallSource: config.MistralCallSourceVibeCode, ClientName: config.MistralClientNameVibeProgrammat,
	})
	want := `{"model":"m","metadata":{"call_source":"vibe_code","client_name":"vibe_programmatic"}}`
	if !applied || string(out) != want {
		t.Fatalf("got %q", out)
	}
	// The appended field must be the documented location: metadata.call_source,
	// never a root-level key.
	var fields map[string]json.RawMessage
	if json.Unmarshal(out, &fields) != nil {
		t.Fatal("invalid JSON")
	}
	if _, root := fields["call_source"]; root {
		t.Fatal("call_source must not be a root field")
	}
	var meta map[string]string
	if json.Unmarshal(fields["metadata"], &meta) != nil || meta["call_source"] != "vibe_code" {
		t.Fatalf("wrong metadata: %s", fields["metadata"])
	}
}

func TestMistralPlanKeyScopesAndKeyType(t *testing.T) {
	body := []byte(`{"model":"m","messages":[]}`)
	mistralAccount := config.Account{ID: "m1", Provider: config.ProviderMistral}
	vibe := &quota.Snapshot{Mistral: &quota.MistralState{KeyType: "CHAT"}}
	studio := &quota.Snapshot{Mistral: &quota.MistralState{KeyType: "API"}}
	all := config.MistralChatMetadata{Mode: config.MistralChatMetadataAllKeys, CallSource: config.MistralCallSourceVibeCode}
	chatKey := config.MistralChatMetadata{Mode: config.MistralChatMetadataChatKey, CallSource: config.MistralCallSourceVibeCode}
	for _, tc := range []struct {
		name string
		tpl  config.MistralChatMetadata
		path string
		snap *quota.Snapshot
		acct config.Account
		want bool
	}{
		{"off never injects", config.MistralChatMetadata{Mode: config.MistralChatMetadataOff, CallSource: config.MistralCallSourceVibeCode}, "/v1/chat/completions", vibe, mistralAccount, false},
		{"all-keys on Chat", all, "/v1/chat/completions", studio, mistralAccount, true},
		{"Responses routes the same way", all, "/v1/responses", vibe, mistralAccount, true},
		{"token counting untouched", all, "/v1/responses/input_tokens", vibe, mistralAccount, false},
		{"FIM untouched", all, "/v1/fim/completions", vibe, mistralAccount, false},
		{"embeddings untouched", all, "/v1/embeddings", vibe, mistralAccount, false},
		{"chat-key with a Vibe verdict", chatKey, "/v1/chat/completions", vibe, mistralAccount, true},
		{"chat-key with a Studio verdict", chatKey, "/v1/chat/completions", studio, mistralAccount, false},
		{"chat-key without a read verdict", chatKey, "/v1/chat/completions", nil, mistralAccount, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, applied := applyMistralPlanKey(body, tc.path, tc.tpl, tc.acct, tc.snap != nil && tc.snap.Mistral != nil && tc.snap.Mistral.KeyType == "CHAT")
			if applied != tc.want {
				t.Fatalf("applied=%v want %v", applied, tc.want)
			}
			if !applied && !bytes.Equal(out, body) {
				t.Fatal("a skipped scope changed the body")
			}
			if applied && !bytes.Contains(out, []byte(`"metadata":{"call_source":"vibe_code"}`)) {
				t.Fatalf("unexpected body %s", out)
			}
		})
	}
}

// The plan-key decision must not disturb the storage/compat layer: parameter
// validation, identity and the cache-key budget keep reading the same payload.
func TestMistralPlanKeyLeavesPrepareAndCacheKeyAlone(t *testing.T) {
	raw := `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":"hi"}],"temperature":1.2}`
	before, key, err := prepareMistralRequest([]byte(raw), "/v1/chat/completions", "scope")
	if err != nil || key != "scope" {
		t.Fatalf("%v %q", err, key)
	}
	if bytes.Contains(before, []byte(`"metadata"`)) {
		t.Fatal("prepare must not own the plan-key marker")
	}
	all := config.MistralChatMetadata{Mode: config.MistralChatMetadataAllKeys, CallSource: config.MistralCallSourceVibeCode}
	withKey, applied := appendMistralPlanKeyMetadata(before, all)
	if !applied || !strings.HasPrefix(string(withKey), strings.TrimSuffix(string(before), "}")) {
		t.Fatal("plan-key marker must be appended, not rewritten into the prepared bytes")
	}
	var got struct {
		PromptCacheKey string          `json:"prompt_cache_key"`
		Temperature    float64         `json:"temperature"`
		Metadata       json.RawMessage `json:"metadata"`
	}
	if json.Unmarshal(withKey, &got) != nil || got.PromptCacheKey != "scope" || got.Temperature != 1.2 || string(got.Metadata) != `{"call_source":"vibe_code"}` {
		t.Fatalf("prepared fields changed: %s", withKey)
	}
}

// Full HTTP path: a CHAT-key account in chat-key mode sends the marker, the
// API-key account in the same pool sends nothing, and the request identity and
// existing metadata are untouched.
func TestMistralPlanKeyHTTPChatKeyMode(t *testing.T) {
	e := newMistralEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Mistral.ChatMetadata = &config.MistralChatMetadata{Mode: config.MistralChatMetadataChatKey, CallSource: config.MistralCallSourceVibeCode, ClientName: config.MistralClientNameVibeProgrammat}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.setMistralKeyType(t, "m1", "CHAT")
	e.setMistralKeyType(t, "m2", "API")
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	if st, resp := e.post(t, "/v1/chat/completions", body, nil); st != 200 {
		t.Fatalf("%d %s", st, resp)
	}
	if e.count() != 1 {
		t.Fatalf("expected one attempt, got %d", e.count())
	}
	sent := e.call(0).body
	if !strings.Contains(sent, `"metadata":{"call_source":"vibe_code","client_name":"vibe_programmatic"}`) {
		t.Fatalf("CHAT key attempt did not carry the plan-key marker: %s", sent)
	}
	if !strings.HasPrefix(sent, `{"model":"m","messages":[{"role":"user","content":"hello"}]`) {
		t.Fatalf("caller bytes were rewritten: %s", sent)
	}
}

// all-keys mode is prepared on the payload, so a retried request reuses one
// prepared body and every attempt carries exactly one marker.
func TestMistralPlanKeyHTTPAllKeysRetries(t *testing.T) {
	e := newMistralEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Mistral.ChatMetadata = &config.MistralChatMetadata{Mode: config.MistralChatMetadataAllKeys, CallSource: config.MistralCallSourceVibeCode}
		c.Routing.MaxAccounts = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if e.count() == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"object":"error","message":"temporary"}`)
			return
		}
		fmt.Fprint(w, `{"id":"reply","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	if st, resp := e.post(t, "/v1/chat/completions", body, nil); st != 200 {
		t.Fatalf("%d %s", st, resp)
	}
	if e.count() != 2 {
		t.Fatalf("expected two attempts, got %d", e.count())
	}
	for i := 0; i < e.count(); i++ {
		sent := e.call(i).body
		if strings.Count(sent, `"metadata"`) != 1 || !strings.Contains(sent, `"metadata":{"call_source":"vibe_code"}`) {
			t.Fatalf("attempt %d carried %q", i, sent)
		}
	}
	if e.call(0).body != e.call(1).body {
		t.Fatalf("a retried payload changed: %q vs %q", e.call(0).body, e.call(1).body)
	}
}

func TestMistralPlanKeyHTTPOffByDefault(t *testing.T) {
	e := newMistralEnv(t)
	e.setMistralKeyType(t, "m1", "CHAT")
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	if st, resp := e.post(t, "/v1/chat/completions", body, nil); st != 200 {
		t.Fatalf("%d %s", st, resp)
	}
	if sent := e.call(0).body; strings.Contains(sent, "call_source") {
		t.Fatalf("an unconfigured pool must forward the caller's body: %s", sent)
	}
}

// setMistralKeyType publishes a workspace read so chat-key mode has a decided
// key type. It uses the same poller view the attempt reads.
func (e *mistralEnv) setMistralKeyType(t *testing.T, id, kind string) {
	t.Helper()
	e.s.quota.Load(map[string]*quota.Snapshot{id: {Mistral: &quota.MistralState{KeyType: kind, CheckedAt: time.Now()}}})
}

// The console boundary: the plan-key template is saved only into the Mistral
// block, survives a reload, keeps the other pools' bytes, and an invalid
// template is refused without publishing a partial configuration.
func TestMistralPlanKeySettingsRoundTrip(t *testing.T) {
	e := newMistralEnv(t)
	w := httptest.NewRecorder()
	e.s.apiSettings(w, httptest.NewRequest(http.MethodGet, "/api/settings?provider=mistral", nil))
	var response struct {
		Settings settingsDoc `json:"settings"`
	}
	if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Settings.Mistral == nil {
		t.Fatalf("invalid settings: %s", w.Body)
	}
	before := e.s.cfg.Get()
	doc := response.Settings
	tpl := config.MistralChatMetadata{Mode: config.MistralChatMetadataChatKey, CallSource: config.MistralCallSourceVibeCode, ClientName: config.MistralClientNameVibeProgrammat}
	doc.Mistral.ChatMetadata = &tpl
	raw, _ := json.Marshal(doc)
	w = httptest.NewRecorder()
	e.s.apiSaveSettings(w, httptest.NewRequest(http.MethodPut, "/api/settings?provider=mistral", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatalf("save failed: %d %s", w.Code, w.Body)
	}
	check := func() {
		t.Helper()
		c := e.s.cfg.Get()
		got, on := c.Mistral.EffectiveChatMetadata()
		if !on || got != tpl {
			t.Fatalf("template not saved: %+v on=%v", got, on)
		}
		if !reflect.DeepEqual(before.Factory, c.Factory) || !reflect.DeepEqual(before.OpenCode, c.OpenCode) || !reflect.DeepEqual(before.TokenHarbor, c.TokenHarbor) {
			t.Fatal("plan-key save changed another pool")
		}
	}
	check()
	if err := e.s.cfg.Reload(); err != nil {
		t.Fatal(err)
	}
	check()
	stable := e.s.cfg.Get()
	doc.Revision = stable.Revision
	doc.Mistral.ChatMetadata = &config.MistralChatMetadata{Mode: config.MistralChatMetadataAllKeys, CallSource: "api_code"}
	raw, _ = json.Marshal(doc)
	w = httptest.NewRecorder()
	e.s.apiSaveSettings(w, httptest.NewRequest(http.MethodPut, "/api/settings?provider=mistral", bytes.NewReader(raw)))
	if w.Code != http.StatusBadRequest || e.s.cfg.Get() != stable {
		t.Fatalf("a free-form call_source was published: %d %s", w.Code, w.Body)
	}
	// Off is a real, savable state that must stop the injection completely.
	doc.Mistral.ChatMetadata = &config.MistralChatMetadata{Mode: config.MistralChatMetadataOff}
	raw, _ = json.Marshal(doc)
	w = httptest.NewRecorder()
	e.s.apiSaveSettings(w, httptest.NewRequest(http.MethodPut, "/api/settings?provider=mistral", bytes.NewReader(raw)))
	if w.Code != 200 {
		t.Fatalf("off not savable: %d %s", w.Code, w.Body)
	}
	if _, on := e.s.cfg.Get().Mistral.EffectiveChatMetadata(); on {
		t.Fatal("off still reports an effective template")
	}
}

// The request log says whether the pool applied the template, so an operator
// can tell a 402 that predates the setting from one that ignores it.
func TestMistralPlanKeyRequestLogFlag(t *testing.T) {
	e := newMistralEnv(t)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Mistral.ChatMetadata = &config.MistralChatMetadata{Mode: config.MistralChatMetadataAllKeys, CallSource: config.MistralCallSourceVibeCode}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 200 {
		t.Fatal("mock inference failed")
	}
	page, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 20, 0)
	if len(page.Items) == 0 || !strings.Contains(string(mustJSON(t, page.Items[0])), `"mistralPlanKey":true`) {
		t.Fatal("request log did not record the applied template")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Measured on 2026-11-27 against the three kevin-11.26 Vibe keys: the upstream
// answers 402 for a Chat or Responses body with no call_source, and 200 once it
// is present. A body that says nothing about call_source is rewritten; any body
// that already names one, and every shape the upstream ignores anyway
// (client_name alone, a root-level key, an empty object), is left as sent.
func TestMistralPlanKeyMeasuredShapes(t *testing.T) {
	tpl := config.MistralChatMetadata{Mode: config.MistralChatMetadataAllKeys, CallSource: config.MistralCallSourceVibeCode, ClientName: config.MistralClientNameVibeProgrammat}
	for _, tc := range []struct {
		name   string
		body   string
		change bool
	}{
		{"missing", `{"model":"m","input":"ping"}`, true},
		{"call_source present", `{"model":"m","metadata":{"call_source":"other"}}`, false},
		{"client_name only", `{"model":"m","metadata":{"client_name":"vibe_programmatic"}}`, false},
		{"empty object", `{"model":"m","metadata":{}}`, false},
		{"explicit null", `{"model":"m","metadata":null}`, false},
		// The upstream ignores the root-level key (measured 402), so it is not
		// a reason to skip the metadata the request actually needs; the root
		// key itself is preserved.
		{"root-level key", `{"model":"m","call_source":"vibe_code"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, changed := appendMistralPlanKeyMetadata([]byte(tc.body), tpl)
			if changed != tc.change {
				t.Fatalf("changed=%v want %v: %s", changed, tc.change, out)
			}
			if !changed && string(out) != tc.body {
				t.Fatalf("body rewritten when it must not be: %s", out)
			}
			if changed && !bytes.Contains(out, []byte(`"metadata":{"call_source":"vibe_code","client_name":"vibe_programmatic"}`)) {
				t.Fatalf("injection did not carry call_source: %s", out)
			}
		})
	}
	// Token counting and FIM returned no measurement: they keep their bytes.
	for _, path := range []string{"/v1/responses/input_tokens", "/v1/fim/completions"} {
		if _, applied := applyMistralPlanKey([]byte(`{"model":"m"}`), path, tpl, config.Account{Provider: config.ProviderMistral}, true); applied {
			t.Fatalf("%s must stay unverified and untouched", path)
		}
	}
}

// A key whose type was never read must not be treated as a Studio key: that
// would silently keep the 402 the mode exists to fix. The request reads the
// type once, then decides from the workspace's own answer.
func TestMistralPlanKeyUnknownTypeIsReadOnce(t *testing.T) {
	for _, kind := range []string{"CHAT", "API"} {
		t.Run(kind, func(t *testing.T) {
			e := newMistralEnv(t)
			var reads int
			var sent []string
			var mu sync.Mutex
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/vibe/whoami":
					mu.Lock()
					reads++
					mu.Unlock()
					fmt.Fprintf(w, `{"plan_type":%q,"plan_name":"INDIVIDUAL","customer_id":"ws-1234567890"}`, kind)
				case "/v1/billing/budget":
					fmt.Fprintf(w, `{"api_budget":{"initial_budget":30,"usage_percentage":100,"currency":"USD","reset_at":%q,"payg_enabled":false}}`, time.Now().Add(24*time.Hour).Format(time.RFC3339))
				case "/v1/billing/subscription":
					fmt.Fprint(w, `{"plan_name":"INDIVIDUAL","payg_enabled":false,"vibe_payg_enabled":false}`)
				case "/v1/models":
					fmt.Fprint(w, `{"object":"list","data":[{"id":"m"}]}`)
				default:
					b, _ := io.ReadAll(r.Body)
					mu.Lock()
					sent = append(sent, string(b))
					mu.Unlock()
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"reply","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
				}
			}))
			defer up.Close()
			if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				c.Mistral.BaseURL = up.URL + "/v1"
				c.Mistral.ChatMetadata = &config.MistralChatMetadata{Mode: config.MistralChatMetadataChatKey, CallSource: config.MistralCallSourceVibeCode}
				r := c.Routing
				r.MaxAccounts = 1
				c.Mistral.Routing = &r
				c.Quota.Enabled = false
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if snap := e.s.quota.View("m1"); snap != nil && snap.Mistral != nil {
				t.Fatalf("precondition: type must start unknown: %+v", snap.Mistral)
			}
			if st, resp := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 200 {
				t.Fatalf("%d %s", st, resp)
			}
			mu.Lock()
			first := reads
			mu.Unlock()
			if first == 0 {
				t.Fatal("an unknown key type was not read before deciding")
			}
			mu.Lock()
			body := sent[len(sent)-1]
			mu.Unlock()
			has := strings.Contains(body, `"call_source":"vibe_code"`)
			if has != (kind == "CHAT") {
				t.Fatalf("%s key injected=%v: %s", kind, has, body)
			}
			// A second request must reuse the read, not go to whoami again.
			if st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); st != 200 {
				t.Fatal("second request failed")
			}
			mu.Lock()
			second := reads
			mu.Unlock()
			if second != first {
				t.Fatalf("type was re-read per request: %d -> %d", first, second)
			}
		})
	}
}

// Issue #7216 section 7: a history replayed from another provider carries
// reasoning fields Mistral forbids. Measured 2026-11-28 against the live
// upstream with a Vibe key: reasoning_content, reasoning and thinking each
// return 422 extra_forbidden on mistral-small-latest, while the native thinking
// chunk returns 200 — for every model tried, not only the exact reasoning ones.
func TestMistralThoughtFieldsConsumedForEveryModel(t *testing.T) {
	e := newMistralEnv(t)
	var mu sync.Mutex
	var bodies []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"object":"list","data":[{"id":"any-model"}]}`)
			return
		case "/api/vibe/whoami", "/v1/billing/budget", "/v1/billing/subscription":
			w.WriteHeader(500)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		var fields map[string]json.RawMessage
		if json.Unmarshal(b, &fields) != nil {
			w.WriteHeader(400)
			return
		}
		// A model that refuses the extension fields, like the live upstream.
		var msgs []map[string]json.RawMessage
		_ = json.Unmarshal(fields["messages"], &msgs)
		for _, m := range msgs {
			for _, key := range []string{"reasoning_content", "reasoning", "thinking"} {
				// reasoning_content is the pool's own canonical field and is
				// always consumed before sending, so its presence is a bug too.
				if _, bad := m[key]; bad {
					w.WriteHeader(422)
					fmt.Fprintf(w, `{"detail":[{"type":"extra_forbidden","loc":["body","messages",0,"assistant",%q]}]}`, key)
					return
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	defer up.Close()
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Mistral.BaseURL = up.URL + "/v1"
		c.Quota.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"mistral-small-latest", "mistral-large-4-0", "zai-glm-5-3", "deepseek-v4-1-flash-1017", "any-model"} {
		for _, field := range []string{"reasoning_content", "reasoning", "thinking"} {
			body := fmt.Sprintf(`{"model":%q,"max_tokens":1,"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"answer",%q:"thought"},{"role":"user","content":"again"}]}`, model, field)
			if st, resp := e.post(t, "/v1/chat/completions", body, nil); st != 200 {
				t.Fatalf("%s/%s: %d %s", model, field, st, resp)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, b := range bodies {
		// The native chunk legitimately contains "thinking":[...] and
		// "type":"thinking"; only the OpenAI/Anthropic fields may not appear.
		for _, leaked := range []string{`"reasoning_content"`, `"reasoning":`, `"thinking":`} {
			if leaked == `"thinking":` && !strings.Contains(b, `"content":"answer"`) {
				continue // the native chunk form, checked separately below
			}
			if strings.Contains(b, leaked) {
				t.Fatalf("extension field reached the upstream: %s", b)
			}
		}
		if !strings.Contains(b, `"type":"thinking"`) {
			t.Fatalf("the thought was dropped instead of converted: %s", b)
		}
	}
}
