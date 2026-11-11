package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

const layoutNativeFixture = `[{"type":"text","text":"before "},{"type":"thinking","thinking":[{"type":"text","text":"private-thought-中文"}],"closed":true},{"type":"text","text":"after"}]`

func recordLayoutFixture(s *mistralLayouts, scope, content string) *mistralLayoutLog {
	log := &mistralLayoutLog{}
	c := s.capture(scope, log)
	c.observe([]byte(`{"choices":[{"index":0,"message":{"content":`+content+`},"finish_reason":"stop"}]}`), false)
	return log
}

func TestMistralLayoutScopesAndSignedCheckpoint(t *testing.T) {
	s := newMistralLayouts([]byte("synthetic-private-secret"))
	cfg := config.Default()
	ck := config.ClientKey{ID: "synthetic-key-id", Group: "synthetic-group", KeyHash: config.HashKey("synthetic-client-secret")}
	ident := Identity{Fingerprint: "synthetic-session-HMAC"}
	scope := s.scope(cfg, ck, ident, "zai-glm-5-3", "/v1/chat/completions")
	log := recordLayoutFixture(s, scope, layoutNativeFixture)
	key := s.textKey(scope, "before after", "private-thought-中文")
	r, ok, err := s.get(key)
	if err != nil || !ok || log.Captured != 1 || len(r.Blocks) != 3 {
		t.Fatal("native layout not captured")
	}
	for _, tc := range []struct {
		ck    config.ClientKey
		id    Identity
		model string
	}{
		{config.ClientKey{ID: "different", Group: ck.Group, KeyHash: ck.KeyHash}, ident, "zai-glm-5-3"},
		{config.ClientKey{ID: ck.ID, Group: "different", KeyHash: ck.KeyHash}, ident, "zai-glm-5-3"},
		{config.ClientKey{ID: ck.ID, Group: ck.Group, KeyHash: "rotated"}, ident, "zai-glm-5-3"},
		{ck, Identity{Fingerprint: "different-session"}, "zai-glm-5-3"},
		{ck, ident, "deepseek-v4-1-flash-1017"},
	} {
		other := s.scope(cfg, tc.ck, tc.id, tc.model, "/v1/chat/completions")
		if _, hit, err := s.get(s.textKey(other, "before after", "private-thought-中文")); hit || err != nil {
			t.Fatal("layout crossed caller/key/group/session/model boundary")
		}
	}
	for _, tc := range []struct{ model, path string }{{"mistral-large-4-0", "/v1/chat/completions"}, {"zai-glm-5-3", "/v1/responses"}, {"zai-glm-5-3", "/v1/fim/completions"}} {
		if s.scope(cfg, ck, ident, tc.model, tc.path) != "" {
			t.Fatal("layout feature escaped exact scope")
		}
	}
	if s.scope(cfg, ck, Identity{}, "zai-glm-5-3", "/v1/chat/completions") != "" {
		t.Fatal("anonymous history acquired a global layout scope")
	}
	exported := s.export()
	wire, _ := json.Marshal(exported)
	for _, private := range []string{"before", "after", "private-thought", "synthetic-session", "synthetic-key-id", "synthetic-client-secret", "synthetic-private-secret"} {
		if bytes.Contains(wire, []byte(private)) {
			t.Fatalf("checkpoint retained original text/identity: %s", private)
		}
	}
	restored := newMistralLayouts([]byte("synthetic-private-secret"))
	if err := restored.importRecords(exported); err != nil {
		t.Fatal(err)
	}
	r, ok, err = restored.get(key)
	content, renderErr := r.render("before after", "private-thought-中文")
	if err != nil || !ok || renderErr != nil || !bytes.Equal(content, []byte(layoutNativeFixture)) {
		t.Fatalf("signed checkpoint did not restore layout: %s %v %v", content, err, renderErr)
	}
	foreign := newMistralLayouts([]byte("different-secret"))
	if foreign.importRecords(exported) == nil {
		t.Fatal("foreign-secret checkpoint accepted")
	}
	tampered := append([]mistralLayoutRecord(nil), exported...)
	tampered[0] = cloneMistralLayout(tampered[0])
	tampered[0].Blocks[0].Bytes++
	if newMistralLayouts([]byte("synthetic-private-secret")).importRecords(tampered) == nil {
		t.Fatal("tampered layout accepted")
	}
	if len(exported) != 1 || len(exported[0].Blocks) != 3 {
		t.Fatal("checkpoint was mutated through shared slices")
	}
	if _, err := r.render("changed answer", "private-thought-中文"); !errors.Is(err, errMistralLayout) {
		t.Fatal("incomplete text consumption did not fail")
	}
	bad := cloneMistralLayout(r)
	bad.Blocks[0].Bytes = 1
	if _, err := bad.render("中文after", "private-thought-中文"); !errors.Is(err, errMistralLayout) {
		t.Fatal("UTF-8 split accepted")
	}
}

func TestMistralLayoutLimitsTTLAndAmbiguity(t *testing.T) {
	s := newMistralLayouts([]byte("synthetic-private-secret"))
	now := time.Now()
	s.now = func() time.Time { return now }
	scope := "synthetic-scope"
	recordLayoutFixture(s, scope, layoutNativeFixture)
	key := s.textKey(scope, "before after", "private-thought-中文")
	now = now.Add(23 * time.Hour)
	if _, ok, err := s.get(key); !ok || err != nil {
		t.Fatal("active layout expired too soon")
	}
	now = now.Add(25 * time.Hour)
	if _, _, err := s.get(key); !errors.Is(err, errMistralLayout) {
		t.Fatal("known expired layout silently guessed")
	}
	// Different native orders with identical visible strings are unknowable
	// without client assistance; preserve an ambiguity marker, never last wins.
	recordLayoutFixture(s, scope, `[{"type":"thinking","thinking":[{"type":"text","text":"private-thought-中文"}],"closed":true},{"type":"text","text":"before after"}]`)
	if _, _, err := s.get(key); !errors.Is(err, errMistralLayout) {
		t.Fatal("same-text different-layout silently selected")
	}
	log := recordLayoutFixture(s, "oversized", `[`+strings.TrimSuffix(strings.Repeat(`{"type":"text","text":"x"},`, mistralLayoutParts+1), ",")+`]`)
	if log.CaptureUnavailable != 1 {
		t.Fatal("layout node budget not enforced")
	}
	if r, hit, err := s.get(s.textKey("oversized", strings.Repeat("x", mistralLayoutParts+1), "")); err != nil || hit || r.State != "unavailable" {
		t.Fatal("missing descriptor was guessed/restored or blocked existing compatibility")
	}
	// Ten thousand transport fragments are one logical text run, not an
	// unbounded descriptor per SSE frame. No body accumulation in capture.
	c := s.capture("long-stream", &mistralLayoutLog{})
	for i := 0; i < 10000; i++ {
		c.observe([]byte(`{"choices":[{"delta":{"content":[{"type":"text","text":"x"}]}}]}`), true)
	}
	c.observe([]byte("[DONE]"), true)
	r, ok, err := s.get(s.textKey("long-stream", strings.Repeat("x", 10000), ""))
	if err != nil || !ok || len(r.Blocks) != 1 || r.Blocks[0].Bytes != 10000 {
		t.Fatal("SSE continuation grew per-frame state or lost bytes")
	}
	limited := newMistralLayouts([]byte("synthetic-private-secret"))
	limited.maxCount = 2
	limited.maxBytes = 2048
	for i := 0; i < 8; i++ {
		recordLayoutFixture(limited, fmt.Sprint(i), layoutNativeFixture)
	}
	if len(limited.entries) > 2 || limited.bytes > limited.maxBytes {
		t.Fatal("LRU count/byte budget exceeded")
	}
	if _, hit, err := limited.get(limited.textKey("0", "before after", "private-thought-中文")); hit || err != nil {
		t.Fatal("evicted record resurrected")
	}
}

func TestMistralLayoutNoClientFieldsUnknownAndAmbiguousHTTP(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		content := layoutNativeFixture
		if e.count() == 2 {
			content = `[{"type":"thinking","thinking":[{"type":"text","text":"private-thought-中文"}],"closed":true},{"type":"text","text":"before after"}]`
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":"stop"}]}`, content)
	})
	initial := `{"model":"zai-glm-5-3","messages":[{"role":"user","content":"original user"}]}`
	for i := 0; i < 2; i++ {
		if status, _ := e.post(t, "/v1/chat/completions", initial, map[string]string{"Session-Id": "local-ambiguous"}); status != 200 {
			t.Fatal("first response failed")
		}
	}
	body := `{"model":"zai-glm-5-3","messages":[{"role":"user","content":"original user"},{"role":"assistant","content":"before after","reasoning_content":"private-thought-中文"},{"role":"user","content":"next"}]}`
	status, reply := e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "local-ambiguous"})
	if status != 400 || e.count() != 2 || !strings.Contains(reply, "mistral_layout_unavailable") {
		t.Fatalf("ambiguous history sent upstream or hidden: %d %s", status, reply)
	}
	// A genuinely native history is authoritative and needs no guessed cache.
	native := `{"model":"zai-glm-5-3","messages":[{"role":"user","content":"original user"},{"role":"assistant","content":` + layoutNativeFixture + `}]}`
	if status, _ := e.post(t, "/v1/chat/completions", native, map[string]string{"Session-Id": "local-ambiguous"}); status != 200 || e.count() != 3 {
		t.Fatal("native authoritative history rejected")
	}
	// Imported/older/edited history has no recoverable source layout. Keep
	// prior compatibility but explicitly expose unknown, never claim restored.
	req, _ := http.NewRequest(http.MethodPost, e.front.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer mistral-client")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session-Id", "different-session")
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("X-Mistral-Layout"), "legacy_unknown=1") || strings.Contains(resp.Header.Get("X-Mistral-Layout"), "restored=1") {
		t.Fatal("unknown history silently advertised as lossless")
	}
}

func TestMistralLayoutNormalServerRestartAndPrivacy(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":"stop"}]}`, layoutNativeFixture)
	})
	initial := `{"model":"zai-glm-5-3","messages":[{"role":"user","content":"original question"}]}`
	if status, _ := e.post(t, "/v1/chat/completions", initial, map[string]string{"Session-Id": "synthetic-restart-session"}); status != 200 {
		t.Fatal("initial HTTP response failed")
	}
	e.s.requests.Wait()
	secret := bytes.Clone(e.s.secret)
	if err := e.s.saveMetadata(true); err != nil {
		t.Fatal(err)
	}
	wire, err := os.ReadFile(filepath.Join(e.s.dataDir, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"private-thought-中文", "before ", "synthetic-restart-session"} {
		if bytes.Contains(wire, []byte(private)) {
			t.Fatal("metadata stored source content/identity")
		}
	}
	next, err := New(e.s.dataDir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { next.stopPerch(); next.logs.Close() })
	if !bytes.Equal(secret, next.secret) {
		t.Fatal("layout checkpoint rotated routingSecret")
	}
	front := httptest.NewServer(next.Handler())
	t.Cleanup(front.Close)
	body := `{"model":"zai-glm-5-3","messages":[{"role":"user","content":"original question"},{"role":"assistant","content":"before after","reasoning_content":"private-thought-中文"},{"role":"user","content":"next question"}]}`
	req, _ := http.NewRequest(http.MethodPost, front.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer mistral-client")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session-Id", "synthetic-restart-session")
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("X-Mistral-Layout"), "restored=1") || e.count() != 2 {
		t.Fatal("normal restart did not restore layout through real HTTP")
	}
	var root struct {
		Messages []struct{ Content json.RawMessage }
	}
	_ = json.Unmarshal([]byte(e.call(1).body), &root)
	if len(root.Messages) != 3 || !bytes.Equal(root.Messages[1].Content, []byte(layoutNativeFixture)) {
		t.Fatal("restarted Server sent different native order")
	}
}

func TestMistralLayoutStreamFlushAndNoPartialCheckpoint(t *testing.T) {
	e := newMistralEnv(t)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":[{\"type\":\"text\",\"text\":\"first fragment\"}]}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "event: error\ndata: {\"error\":{\"message\":\"synthetic failure\"}}\n\ndata: [DONE]\n\n")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, e.front.URL+"/v1/chat/completions", strings.NewReader(`{"model":"zai-glm-5-3","stream":true,"messages":[{"role":"user","content":"local question"}]}`))
	req.Header.Set("Authorization", "Bearer mistral-client")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session-Id", "local-partial")
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal("layout capture delayed first-event flush", err)
	}
	defer resp.Body.Close()
	first, err := readEvent(bufio.NewReader(resp.Body))
	if err != nil || !bytes.Contains(first, []byte("first fragment")) {
		t.Fatal("first event not flushed")
	}
	once.Do(func() { close(release) })
	io.Copy(io.Discard, resp.Body)
	e.s.requests.Wait()
	if len(e.s.mistralLayouts.export()) != 0 {
		t.Fatal("failed/partial stream became a complete replay layout")
	}
}

func TestMistralLayoutPlainBoundaryDoesNotDropFields(t *testing.T) {
	for _, content := range []string{
		`[{"type":"text","text":"answer","annotations":[{"type":"url_citation","url":"https://example.invalid/original"}]}]`,
		`[{"type":"text","text":"first","text":"second"}]`,
		`[{"type":"thinking","thinking":[{"type":"text","text":"first","\u0074ext":"second"}]}]`,
		`[{"type":"thinking","thinking":[],"signature":"actual-provider-signature"}]`,
		`[{"type":"image_url","image_url":"original-image"}]`,
	} {
		if _, _, ok := mistralLayoutPlain([]byte(content)); ok {
			t.Fatalf("layout matching would collapse meaningful/ambiguous fields: %s", content)
		}
	}
}

func TestMistralLayoutFallbackIdentityRoundTrip(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":"stop"}]}`, layoutNativeFixture)
	})
	initial := `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":"same fallback question"}]}`
	if status, _ := e.post(t, "/v1/chat/completions", initial, nil); status != 200 {
		t.Fatal("initial fallback request failed")
	}
	body := `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":"same fallback question"},{"role":"assistant","content":"before after","reasoning_content":"private-thought-中文"},{"role":"user","content":"next question"}]}`
	status, _ := e.post(t, "/v1/chat/completions", body, nil)
	if status != 200 || e.count() != 2 || e.call(0).header.Get("X-Affinity") != e.call(1).header.Get("X-Affinity") || !strings.Contains(e.call(1).body, `"content":`+layoutNativeFixture) {
		t.Fatal("layout changed fallback HMAC/cache hint or failed to restore")
	}
}

func TestMistralLayoutConcurrentCaptureExport(t *testing.T) {
	s := newMistralLayouts([]byte("synthetic-private-secret"))
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				scope := fmt.Sprint(i)
				recordLayoutFixture(s, scope, layoutNativeFixture)
				s.get(s.textKey(scope, "before after", "private-thought-中文"))
				s.export()
			}
		}(i)
	}
	wg.Wait()
	if len(s.export()) != 12 {
		t.Fatal("concurrent state lost or duplicate-key conflict invented")
	}
}
