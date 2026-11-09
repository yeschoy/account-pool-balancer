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
