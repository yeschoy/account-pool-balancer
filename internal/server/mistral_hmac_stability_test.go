package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestMistralHMACRepeatAndConcurrent(t *testing.T) {
	s := &Server{secret: bytes.Repeat([]byte{17}, 32)}
	originalSecret := bytes.Clone(s.secret)
	cfg := config.Default()
	cfg.Routing.FallbackHash = true
	wires := [][]byte{
		[]byte(`{"model":"zai-glm-5-3-flash","messages":[{"role":"system","content":"prefix <keep> 中文"},{"role":"user","content":"question"}]}`),
		[]byte(`{ "metadata":{"z":2,"a":1}, "messages":[{"content":"prefix \u003ckeep\u003e \u4e2d\u6587","role":"system"},{"content":"question","role":"user"}], "model":"zai-glm-5-3-flash" }`),
		[]byte(`{"max_tokens":32768,"temperature":0.9,"reasoning_effort":"max","tools":[{"type":"function","function":{"name":"unused"}}],"messages":[{"role":"system","content":"prefix <keep> 中文"},{"role":"user","content":"question"},{"role":"assistant","content":"answer"},{"role":"user","content":"later turn"}],"model":"zai-glm-5-3-flash"}`),
	}
	const scope = "mistral\x00synthetic-client-id"
	baseline, err := parseRoutingBodyWithHistory(wires[0], true, false)
	if err != nil {
		t.Fatal(err)
	}
	want := s.mistralIdentity(cfg, scope, nil, baseline)
	if want.Source != "message_hmac" || len(want.Fingerprint) != 64 {
		t.Fatal("missing fallback fingerprint")
	}
	// Request IDs, map/root ordering, JSON escapes, later turns, tools and
	// generation parameters are not the fallback identity. This does NOT mean
	// changing those fields preserves the model's actual prompt cache.
	check := func(raw []byte, marker string) error {
		obj, err := parseRoutingBodyWithHistory(raw, true, false)
		if err != nil {
			return err
		}
		h := http.Header{"X-Request-Id": {marker}, "User-Agent": {"synthetic/" + marker}}
		id := s.mistralIdentity(cfg, scope, h, obj)
		key, source := mistralRequestCacheKey(cfg, h, id)
		if id != want || key != "apb-mistral-"+want.Fingerprint || source != "session_hmac" {
			return fmt.Errorf("same identity inputs changed fingerprint or cache-key candidate")
		}
		return nil
	}
	for i := 0; i < 2000; i++ {
		if err := check(wires[i%len(wires)], fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errors := make(chan error, 32)
	for worker := 0; worker < 32; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 250; i++ {
				if err := check(wires[(worker+i)%len(wires)], fmt.Sprintf("%d-%d", worker, i)); err != nil {
					errors <- err
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if !bytes.Equal(s.secret, originalSecret) {
		t.Fatal("HMAC operation mutated its shared secret")
	}
	if s.mistralIdentity(cfg, "mistral\x00different-client-id", nil, baseline).Fingerprint == want.Fingerprint || (&Server{secret: bytes.Repeat([]byte{18}, 32)}).mistralIdentity(cfg, scope, nil, baseline).Fingerprint == want.Fingerprint {
		t.Fatal("client scope and secret must deliberately separate identities")
	}
	t.Log("10000 sequential/concurrent identity and cache-key candidates matched; no raw fingerprints logged")
}

func TestMistralHMACPersistence(t *testing.T) {
	e := newMistralEnv(t)
	const body = `{"model":"zai-glm-5-3-flash","messages":[{"role":"system","content":"stable"},{"role":"user","content":"question"}]}`
	obj, err := parseRoutingBodyWithHistory([]byte(body), true, false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.s.cfg.Get().ForProvider(config.ProviderMistral)
	before := e.s.mistralIdentity(cfg, "mistral\x00client1", nil, obj)
	if err := e.s.saveMetadata(true); err != nil {
		t.Fatal(err)
	}
	// Reconstruct a second local Server from saved fake files, without
	// restarting production or copying any real configuration/credentials.
	dir := t.TempDir()
	for _, name := range []string{"config.json", "metadata.json"} {
		data, err := os.ReadFile(filepath.Join(e.s.dataDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	restored, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.stopPerch(); restored.logs.Close() })
	after := restored.mistralIdentity(restored.cfg.Get().ForProvider(config.ProviderMistral), "mistral\x00client1", nil, obj)
	if before.Fingerprint == "" || before != after || !bytes.Equal(e.s.secret, restored.secret) {
		t.Fatal("saved routing secret or HMAC changed on local Server reconstruction")
	}
}

// Characterization of existing representation boundaries, not a claim that
// these boundaries caused a particular production cache miss. Changing shared
// contentText would affect other pools and persisted bindings too.
func TestMistralHMACRepresentationBoundaries(t *testing.T) {
	s := &Server{secret: bytes.Repeat([]byte{19}, 32)}
	cfg := config.Default()
	cfg.Routing.FallbackHash = true
	identity := func(system, user any, headers http.Header) Identity {
		body, _ := json.Marshal(map[string]any{"messages": []any{
			map[string]any{"role": "system", "content": system},
			map[string]any{"role": "user", "content": user},
		}})
		obj, err := parseRoutingBodyWithHistory(body, true, false)
		if err != nil {
			t.Fatal(err)
		}
		return s.mistralIdentity(cfg, "mistral\x00client", headers, obj)
	}
	chunks := func(text string) any { return []any{map[string]any{"type": "text", "text": text}} }
	if contentText("question") != "question" || contentText(chunks("question")) != "question\n" {
		t.Fatal("update characterization when extraction semantics intentionally change")
	}
	plain := identity("system", "question", nil)
	array := identity("system", chunks("question"), nil)
	if plain.Fingerprint == array.Fingerprint {
		t.Fatal("different extracted strings should not share the current fallback HMAC")
	}
	h := http.Header{"Session-Id": {"synthetic-stable-session"}}
	if identity("system", "question", h) != identity("system", chunks("question"), h) {
		t.Fatal("explicit session must not depend on content representation")
	}
	long := strings.Repeat("中", 1500) // Both representations clip before the extra newline.
	if identity(long, long, nil) != identity(chunks(long), chunks(long), nil) {
		t.Fatal("representation beyond the 4096-byte fingerprint prefix changed HMAC")
	}
	if identity(long+"A", long+"A", nil) != identity(long+"B", long+"B", nil) {
		t.Fatal("suffix outside the identity prefix should not affect HMAC")
	}
	if plain.Fingerprint == identity("different-system", "question", nil).Fingerprint {
		t.Fatal("a changed leading identity input must change the HMAC")
	}
	t.Log("string versus short text-chunk input adds LF and changes fallback; an explicit session bypasses this input boundary")
}

// Exercise the real HTTP handler, not just repeated calls to the hash helper.
// A hypothetical rehash of the already prepared body can differ (for example,
// its inserted cache key becomes an identity hint). The handler must keep the
// original selected identity rather than feeding converted output back into it.
func TestMistralConversionUsesOriginalHMACAndImmutableInput(t *testing.T) {
	for _, model := range []string{"deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash"} {
		for _, carrier := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/claude-carrier=%t", model, carrier), func(t *testing.T) {
				e := newMistralEnv(t)
				role, user := "system", `[{"type":"text","text":"synthetic question <&>"}]`
				if model == "deepseek-v4-1-flash-1017" {
					role = "developer"
					user = `[{"type":"text","text":"synthetic question <&>","cache_control":{"type":"ephemeral"}},{"type":"image_url","image_url":{"url":"https://example.invalid/synthetic.png","MimeType":""}}]`
				}
				label := ""
				if carrier {
					label = `,"user":"user_hash_account_org_session_12345678-abcd-1234-abcd-123456789abc"`
				}
				raw := []byte(fmt.Sprintf(`{"model":%q%s,"messages":[{"content":"synthetic instruction","role":%q},{"role":"user","content":%s},{"role":"assistant","reasoning":"complete synthetic thought","reasoning_content":" ","content":"answer","tool_calls":[{"id":"call-fixed","type":"function","function":{"name":"tool","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call-fixed","content":"tool-result"},{"role":"user","content":"next-user"}]}`, model, label, role, user))
				original := bytes.Clone(raw)
				obj, err := parseRoutingBodyWithHistory(raw, true, false)
				if err != nil {
					t.Fatal(err)
				}
				projection, _ := json.Marshal(obj)
				cfg := e.s.cfg.Get().ForProvider(config.ProviderMistral)
				scope := config.MistralGroup + "\x00client1"
				id := e.s.mistralIdentity(cfg, scope, nil, obj)
				if id.Fingerprint == "" || carrier && id.KeyType != "claude_user_session" || !carrier && id.Source != "message_hmac" {
					t.Fatal("fixture did not select the intended original identity")
				}
				key, _ := mistralRequestCacheKey(cfg, nil, id)
				var expected []byte
				for repeat := 0; repeat < 10; repeat++ {
					prepared, generated, err := prepareMistralRequest(raw, "/v1/chat/completions", key)
					if err != nil || generated != key {
						t.Fatal("unexpected preparation result", err)
					}
					afterProjection, err := json.Marshal(obj)
					if err != nil || !bytes.Equal(raw, original) || !bytes.Equal(projection, afterProjection) || e.s.mistralIdentity(cfg, scope, nil, obj) != id {
						t.Fatal("conversion mutated original bytes/projection/identity")
					}
					if repeat == 0 {
						expected = bytes.Clone(prepared)
					} else if !bytes.Equal(expected, prepared) {
						t.Fatal("identical original input produced unstable converted bytes")
					}
				}
				// The post-conversion hypothetical really differs for fallback
				// (inserted prompt_cache_key) and DeepSeek's removed user label.
				post, err := parseRoutingBodyWithHistory(expected, true, false)
				if err != nil {
					t.Fatal(err)
				}
				if (!carrier || model == "deepseek-v4-1-flash-1017") && e.s.mistralIdentity(cfg, scope, nil, post).Fingerprint == id.Fingerprint {
					t.Fatal("counterfactual rehash must distinguish the two execution orders")
				}
				var before, after struct{ Messages []map[string]any }
				if json.Unmarshal(original, &before) != nil || json.Unmarshal(expected, &after) != nil || len(before.Messages) != len(after.Messages) {
					t.Fatal("invalid converted message count")
				}
				for i, message := range before.Messages {
					wantRole := message["role"]
					if wantRole == "developer" && model == "deepseek-v4-1-flash-1017" {
						wantRole = "system"
					}
					if after.Messages[i]["role"] != wantRole || after.Messages[i]["tool_call_id"] != message["tool_call_id"] || message["role"] != "assistant" && contentText(message["content"]) != contentText(after.Messages[i]["content"]) {
						t.Fatal("conversion moved a message or altered user/tool/system text")
					}
				}
				for repeat := 0; repeat < 2; repeat++ {
					if status, _ := e.post(t, "/v1/chat/completions", string(original), nil); status != 200 || e.count() != repeat+1 {
						t.Fatal("local HTTP conversion failed")
					}
					call := e.call(repeat)
					if call.body != string(expected) || call.header.Get("X-Affinity") != key {
						t.Fatal("actual mock HTTP upload did not retain the pre-conversion HMAC key and prepared bytes")
					}
				}
			})
		}
	}
}
