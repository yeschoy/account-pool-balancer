package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestMistralThinkingParameters(t *testing.T) {
	for _, tc := range []struct{ fields, want string }{
		{`"thinking":{"type":"enabled"}`, "high"},
		{`"thinking":{"type":"disabled"}`, "high"},
		{`"enable_thinking":true`, "high"},
		{`"enable_thinking":false`, "high"},
		{`"thinking":{"type":"enabled"},"enable_thinking":true,"reasoning_effort":null`, "high"},
		{`"thinking":{"type":"disabled"},"enable_thinking":false,"reasoning_effort":"none"`, "high"},
		{`"enable_thinking":true,"reasoning_effort":"medium"`, "high"},
		{`"enable_thinking":false,"reasoning_effort":null`, "high"},
	} {
		body := []byte(`{` + tc.fields + `,"model":"deepseek-v4-1-flash-1017","messages":[]}`)
		out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		var got map[string]json.RawMessage
		if err != nil || json.Unmarshal(out, &got) != nil || string(got["reasoning_effort"]) != `"`+tc.want+`"` || got["thinking"] != nil || got["enable_thinking"] != nil {
			t.Fatalf("%s: %s %v", tc.fields, out, err)
		}
	}
	for _, effort := range []string{"low", "high"} {
		body := []byte(fmt.Sprintf(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":%q,"thinking":{"type":"enabled"}}`, effort))
		out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		if err != nil || !bytes.Contains(out, []byte(`"reasoning_effort":"`+effort+`"`)) {
			t.Fatalf("valid explicit effort lost: %s %v", out, err)
		}
	}
}

func TestMistralParameterRejectionsAreLocal(t *testing.T) {
	e := newMistralEnv(t)
	for _, tc := range []struct{ fields, message string }{

		{`"thinking":{"type":"enabled","budget_tokens":1024}`, "budgets"},
		{`"thinking":{"type":"disabled","budget_tokens":null}`, "budgets"},
		{`"thinking":{"type":"enabled","type":"disabled"}`, "requires only"},
		{`"thinking":{"type":"adaptive"}`, "enabled or disabled"},
		{`"thinking":null`, "requires only"},
		{`"thinking":true`, "requires only"},
		{`"enable_thinking":null`, "boolean"},
		{`"enable_thinking":"false"`, "boolean"},

		{`"reasoning":{"effort":"low"},"reasoning_effort":"high"`, "conflict"},
		{`"reasoning":{"effort":"low"},"reasoning_effort":"max"`, "conflict"},

		{`"reasoning":{"effort":"high","summary":"private-body"}`, "only effort"},
		{`"reasoning":{"summary":"private-body"}`, "only effort"},
		{`"reasoning":{}`, "only effort"},
		{`"reasoning":null`, "only effort"},
		{`"reasoning":"high"`, "only effort"},
		{`"reasoning":[]`, "only effort"},
		{`"reasoning":42`, "only effort"},
		{`"reasoning":{"effort":"high","effort":"high"}`, "duplicate"},
		{`"reasoning":{"effort":"high","eff\u006frt":"low"}`, "duplicate"},
		{`"reasoning":{"effort":"high"},"reasoning":{"effort":"high"}`, "duplicate"},
		{`"reasoning":{"effort":"high"},"reas\u006fning":{"effort":"high"}`, "duplicate"},
		{`"enable_thinking":true,"enable_thinking":true`, "duplicate"},
		{`"thinking":{"type":"disabled"},"th\u0069nking":{"type":"enabled"}`, "duplicate"},
		{`"reasoning_effort":"none","reasoning_effort":"high"`, "duplicate"},
		{`"seed":42,"random_seed":43`, "conflict"},
		{`"seed":42,"random_seed":null`, "conflict"},
		{`"seed":1.5`, "integer"},
		{`"seed":1e2`, "integer"},
		{`"seed":"42"`, "integer"},
		{`"seed":true`, "integer"},
		{`"seed":1,"seed":1`, "duplicate"},
		{`"seed":1,"random_seed":1,"random_seed":1`, "duplicate"},
		{`"logit_bias":{"1":1}`, "logit_bias"},
		{`"logit_bias":{"1":0}`, "logit_bias"},
		{`"logit_bias":[]`, "logit_bias"},
		{`"verbosity":"low"`, "verbosity"},
		{`"verbosity":"high"`, "verbosity"},
		{`"user":{}`, "user"},
		{`"safety_identifier":123`, "safety_identifier"},
		{`"prompt_cache_retention":"24h"`, "cannot guarantee"},
		{`"prompt_cache_retention":"in_memory"`, "cannot guarantee"},
		{`"prompt_cache_key":"a","prompt_cache_key":"b"`, "duplicate"},
		{`"metadata":{},"metadata":{}`, "duplicate"},
		{`"messages":[],"messages":[]`, "duplicate"},
		{`"messages":[{"role":"developer","role":"user","content":"private-body"}]`, "duplicate"},
		{`"messages":[{"role":"assistant","content":"a","content":"b","reasoning_content":"private-body"}]`, "duplicate"},
		{`"messages":[{"role":"assistant","reasoning_content":null,"reasoning_content":"private-body"}]`, "duplicate"},
		{`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":42}}]}]`, "image_url.url"},
		{`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":null}}]}]`, "image_url.url"},
		{`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":42,"MimeType":""}}]}]`, "image_url.url"},
		{`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"private-body","MimeType":"","MimeType":null}}]}]`, "duplicate"},
		{`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"private-body","url":"other"}}]}]`, "duplicate"},
		{`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"private-body"},"image_url":"other"}]}]`, "duplicate"},
		{`"store":true,"thinking":{"type":"enabled"}`, "stateless"},
		{`"store":false,"st\u006fre":null`, "stateless"},
	} {
		t.Run(tc.fields, func(t *testing.T) {
			st, reply := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017",`+tc.fields+`}`, nil)
			if st != 400 || e.count() != 0 || !strings.Contains(reply, tc.message) || !strings.Contains(reply, "invalid_mistral_request") || strings.Contains(reply, "private-body") {
				t.Fatalf("not rejected safely before upstream: %d %s", st, reply)
			}
		})
	}
}

func TestMistralDeveloperRoleOnlyAndCombinedWirePreservation(t *testing.T) {
	messages := `[ { "role" : "developer", "content" : [{"type":"text","text":"original"}], "n":9007199254740993, "tag":1,"tag":2 }, {"role":"user","content":"q"}, {"role":"developer","content":"late","tool_call_id":"opaque"}, {"role":"assistant","tool_calls":[{"id":"tool-id","function":{"arguments":"{\"large\":9007199254740993}"}}]}, {"role":"tool","tool_call_id":"tool-id","content":"result"} ]`
	body := []byte(`{ "model":"deepseek-v4-1-flash-1017", "reasoning_effort":"low", "messages":` + messages + `, "prompt_cache_key":null }`)
	out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "scope")
	want := strings.ReplaceAll(string(body), `"developer"`, `"system"`)
	if err != nil || string(out) != want {
		t.Fatalf("developer changed more than role: %s %v", out, err)
	}
	// Exercise adjacent removal runs at the beginning, middle and end together
	// with role/history rewriting and appended fields, not isolated removals.
	for _, fields := range []string{
		`"thinking":{"type":"disabled"},"store":false,"seed":9007199254740993,`,
		`"logit_bias": { },"verbosity":"medium","user":"root","safety_identifier":"label","prompt_cache_retention":null,`,
	} {
		for _, position := range []string{"first", "middle", "last"} {
			core := `"model":"deepseek-v4-1-flash-1017","messages":` + messages
			extra := `"tools": [ {"n":9007199254740993} ],"metadata": {"session_id":"client-session","n":9007199254740993,"store":true}`
			var input string
			switch position {
			case "first":
				input = `{` + fields + core + `,` + extra + `}`
			case "middle":
				input = `{` + core + `,` + fields + extra + `}`
			case "last":
				input = `{` + core + `,` + extra + `,` + strings.TrimSuffix(fields, ",") + `}`
			}
			prepared, key, err := prepareMistralRequest([]byte(input), "/v1/chat/completions", "scope")
			if err != nil || !json.Valid(prepared) || key != "apb-mistral-scope" {
				t.Fatalf("overlapping removals %s: %s %v", position, prepared, err)
			}
			var got, original map[string]json.RawMessage
			_ = json.Unmarshal(prepared, &got)
			_ = json.Unmarshal([]byte(input), &original)
			for _, k := range []string{"model", "metadata", "tools"} {
				if !bytes.Equal(got[k], original[k]) {
					t.Fatalf("neighbor wire bytes changed: %s", k)
				}
			}
			if string(got["messages"]) != strings.ReplaceAll(messages, `"developer"`, `"system"`) {
				t.Fatal("message order/tool/number bytes changed")
			}
		}
	}
	// Regression: remove the only field and then append another field.
	o := scanMistralObject([]byte(` { "store":false } `))
	o.set("store", nil)
	o.set("reasoning_effort", []byte(`"none"`))
	if !json.Valid(o.render()) || bytes.Contains(o.render(), []byte("store")) {
		t.Fatal("single-field removal/insertion malformed JSON")
	}
}

func TestMistralSeedAndEmptyDefaults(t *testing.T) {
	for _, tc := range []struct{ fields, want string }{
		{`"seed":9007199254740993`, `9007199254740993`},
		{`"seed":-42`, `-42`},
		{`"seed":0,"random_seed":-0`, `-0`},
		{`"seed":9007199254740993,"random_seed":9007199254740993`, `9007199254740993`},
		{`"seed":null,"random_seed":42`, `42`},
		{`"seed":null,"logit_bias":null,"verbosity":null,"user":null,"safety_identifier":"","prompt_cache_retention":""`, ``},
	} {
		out, _, err := prepareMistralRequest([]byte(`{"model":"deepseek-v4-1-flash-1017",`+tc.fields+`}`), "/v1/chat/completions", "")
		var got map[string]json.RawMessage
		if err != nil || json.Unmarshal(out, &got) != nil || string(got["random_seed"]) != tc.want {
			t.Fatalf("seed not preserved: %s %v", out, err)
		}
		for _, k := range []string{"seed", "logit_bias", "verbosity", "user", "safety_identifier", "prompt_cache_retention"} {
			if got[k] != nil {
				t.Fatalf("default/marker remained: %s", k)
			}
		}
	}
}

func TestMistralParametersCacheIdentityAndSingleAttempt(t *testing.T) {
	e := newMistralEnv(t)
	for _, key := range []string{`"client-cache"`, `null`, `""`} {
		body := `{"model":"deepseek-v4-1-flash-1017","thinking":{"type":"enabled"},"store":false,"user":"root-user-is-not-a-session","seed":42,"prompt_cache_key":` + key + `,"metadata": {"session_id":"metadata-session","store":true,"n":9007199254740993},"messages":[{"role":"developer","content":"d"},{"role":"assistant","reasoning_content":"thought","content":"answer"}]}`
		before := e.count()
		st, reply := e.post(t, "/v1/chat/completions", body, nil)
		if st != 200 || e.count() != before+1 {
			t.Fatalf("mock did not receive one native attempt: %d %s", st, reply)
		}
		var got, original map[string]json.RawMessage
		_ = json.Unmarshal([]byte(e.call(before).body), &got)
		_ = json.Unmarshal([]byte(body), &original)
		if !bytes.Equal(got["prompt_cache_key"], original["prompt_cache_key"]) || !bytes.Equal(got["metadata"], original["metadata"]) || string(got["random_seed"]) != "42" || !bytes.Contains(got["messages"], []byte(`"thinking"`)) {
			t.Fatal("cache/metadata/seed/reasoning changed")
		}
	}
	cfg := e.s.cfg.Get().ForProvider(config.ProviderMistral)
	scope := "identity-test"
	body := map[string]any{"user": "root-user-is-not-a-session"}
	id := e.s.identity(cfg, scope, nil, body)
	if id.Fingerprint != "" {
		t.Fatal("root user became a conversation identity")
	}
	body["metadata"] = map[string]any{"session_id": "explicit-session"}
	h := http.Header{"X-Prompt-Cache-Key": []string{"new-cache-hint"}}
	id = e.s.identity(cfg, scope, h, body)
	if id.Fingerprint != e.s.fingerprint(scope, "session", "explicit-session") || id.KeyType != "session_id" {
		t.Fatal("new cache hint overrode existing explicit metadata identity")
	}
	// Native random_seed errors must remain visible; never retry without seed
	// or sweep another account to turn an unsupported setting into HTTP200.
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(422)
		fmt.Fprint(w, `{"error":{"message":"random_seed not supported by mock","type":"invalid_request_error"}}`)
	})
	before := e.count()
	st, _ := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","seed":42,"messages":[]}`, nil)
	if st != 422 || e.count() != before+1 || !strings.Contains(e.call(before).body, `"random_seed":42`) {
		t.Fatal("unsupported seed was hidden or replayed")
	}
}

func TestMistralNewParametersScope(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"other-model", "/v1/chat/completions"},
		{"deepseek-v4-1-flash-1017", "/v1/responses"},
		{"deepseek-v4-1-flash-1017", "/v1/fim/completions"},
	} {
		body := []byte(` { "model":"` + tc.model + `", "thinking":{"type":"disabled"},"enable_thinking":true,"seed":42,"store":false,"logit_bias":{},"verbosity":"high","user":"marker","safety_identifier":"label","prompt_cache_retention":"24h","metadata":{"session_id":"keep"},"messages":[{"role":"developer","content":"keep"}] } `)
		out, _, err := prepareMistralRequest(body, tc.path, "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatalf("new adaptation escaped exact scope: %s %s", tc.model, tc.path)
		}
	}
	// Another provider never invokes Mistral preparation, even for this model.
	e := newMistralEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Upstream.RuntimeBase = e.up.URL + "/v1"
		c.Accounts = append(c.Accounts, config.Account{ID: "other-account", Group: "default", RuntimeKey: "sk-amr-other-runtime-secret", Enabled: true, MaxConc: 1})
		c.ClientKeys = append(c.ClientKeys, config.ClientKey{ID: "other-provider", Group: "default", KeyHash: config.HashKey("other-client"), Enabled: true})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		` {"model":"deepseek-v4-1-flash-1017","thinking":{"type":"disabled"},"seed":42,"store":false,"messages":[{"role":"developer","content":"keep"}]} `,
		` {"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":"keep"}]} `,
		` {"model":"deepseek-v4-1-flash-1017","reasoning":{"effort":"low"},"messages":[{"role":"user","content":"keep"}]} `,
		` {"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}]} `,
	} {
		before := e.count()
		st, reply := e.post(t, "/v1/chat/completions", body, map[string]string{"Authorization": "Bearer other-client"})
		if st != 200 || e.count() != before+1 || e.call(before).body != body {
			t.Fatalf("other provider changed: %d %s", st, reply)
		}
	}
}
