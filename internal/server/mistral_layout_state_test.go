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
	active, ok, err := s.get(key)
	if !ok || err != nil {
		t.Fatal("active layout expired too soon")
	}
	s.refresh(active)
	now = now.Add(2 * time.Hour)
	if _, ok, err := s.get(key); !ok || err != nil {
		t.Fatal("verified replay did not renew TTL")
	}
	now = now.Add(23 * time.Hour)
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

func TestMistralLayoutUntrustedHistoryFallsBackOverHTTP(t *testing.T) {
	const model = "zai-glm-5-3"
	const nativeNext = `[{"type":"text","text":"fresh "},{"type":"thinking","thinking":[{"type":"text","text":"new thought"}]},{"type":"text","text":"reply"}]`
	const fallbackContent = `[{"thinking":[{"text":"private-thought-中文","type":"text"}],"type":"thinking"},{"text":"before after","type":"text"}]`
	const tools = `[ {"type":"function","function":{"name":"run","parameters":{"type":"object","default":9007199254740993}}} ]`
	const toolCalls = `[{"id":"call-1","type":"function","function":{"name":"run","arguments":"{}"}}]`
	const history = `{"role":"user","content":"original user"},{"role":"assistant","content":"before after","reasoning_content":"private-thought-中文","tool_calls":` + toolCalls + `},{"role":"tool","tool_call_id":"call-1","content":"result"},{"role":"user","content":"continue"}`
	for _, mode := range []string{"ambiguous", "expired", "bad-descriptor", "unavailable", "missing"} {
		t.Run(mode, func(t *testing.T) {
			e := newMistralEnv(t)
			s := e.s.mistralLayouts
			h := http.Header{"Session-Id": {"fallback-session"}}
			cfg := e.s.cfg.Get().ForProvider("mistral")
			ck := config.ClientKey{ID: "client1", Group: "mistral", KeyHash: config.HashKey("mistral-client")}
			ident := e.s.mistralIdentity(cfg, "mistral\x00client1", h, nil)
			scope := s.scope(cfg, ck, ident, model, "/v1/chat/completions")
			key := s.textKey(scope, "before after", "private-thought-中文")
			if mode != "missing" {
				recordLayoutFixture(s, scope, layoutNativeFixture)
				switch mode {
				case "ambiguous":
					recordLayoutFixture(s, scope, fallbackContent)
				case "expired", "bad-descriptor", "unavailable":
					s.mu.Lock()
					el := s.entries[key]
					r := cloneMistralLayout(el.Value.(mistralLayoutRecord))
					switch mode {
					case "expired":
						r.Expires = time.Now().Add(-time.Hour)
					case "bad-descriptor":
						r.Blocks[0].Bytes = 0
					case "unavailable":
						r.State, r.Blocks = "unavailable", nil
					}
					el.Value = r
					s.mu.Unlock()
				}
			}
			checkpoints := s.export()
			e.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"choices":[{"message":{"content":%s},"finish_reason":"stop"}]}`, nativeNext)
			})
			send := func(msgs string, wantCount int) (*http.Response, requestLog) {
				t.Helper()
				body := fmt.Sprintf(`{"model":%q,"messages":[%s],"tools":%s,"prompt_cache_key":"same-cache"}`, model, msgs, tools)
				req, _ := http.NewRequest(http.MethodPost, e.front.URL+"/v1/chat/completions", strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer mistral-client")
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Session-Id", "fallback-session")
				resp, err := e.front.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				reply, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 || e.count() != wantCount {
					t.Fatalf("continue failed or retried: status=%d attempts=%d reply=%s err=%v", resp.StatusCode, e.count(), reply, err)
				}
				if strings.Contains(resp.Header.Get("X-Conversation-Layout"), "fallback") || resp.Header.Get("Retry-After") != "" {
					t.Fatal("public response exposed internal fallback or suggested retry")
				}
				e.s.requests.Wait()
				page, err := e.s.logs.Query("requests", map[string]string{"requestId": resp.Header.Get("X-Request-Id")}, time.Time{}, time.Time{}, 10, 0)
				var rec requestLog
				if err != nil || len(page.Items) != 1 || json.Unmarshal(page.Items[0], &rec) != nil || rec.MistralLayout == nil || len(rec.Attempts) != 1 {
					t.Fatal("missing admin-only layout evidence or incorrect attempt count")
				}
				return resp, rec
			}
			assertUpload := func(idx int, wantOld, wantNew string) {
				t.Helper()
				call := e.call(idx)
				var root struct {
					Messages []struct {
						Content   json.RawMessage
						ToolCalls json.RawMessage `json:"tool_calls"`
					}
					Tools json.RawMessage
				}
				if json.Unmarshal([]byte(call.body), &root) != nil || len(root.Messages) < 4 || string(root.Messages[1].Content) != wantOld || string(root.Messages[1].ToolCalls) != toolCalls || string(root.Messages[2].Content) != `"result"` || string(root.Tools) != tools || call.header.Get("X-Affinity") != "same-cache" {
					t.Fatalf("history order/text/tools/cache changed: %s", call.body)
				}
				if wantNew != "" && (len(root.Messages) != 6 || string(root.Messages[4].Content) != wantNew) {
					t.Fatal("new native reply wasn't preserved for the next turn")
				}
			}
			resp, rec := send(history, 1)
			if rec.MistralLayout.Fallback != 1 || rec.MistralLayout.Restored != 0 || rec.MistralLayout.Captured != 1 || !strings.Contains(resp.Header.Get("X-Conversation-Layout"), "restored=0") {
				t.Fatalf("fallback was not labeled distinctly: %+v", rec.MistralLayout)
			}
			assertUpload(0, fallbackContent, "")
			after := s.export()
			if len(after) != len(checkpoints)+1 || (len(checkpoints) == 1 && after[0].MAC != checkpoints[0].MAC) {
				t.Fatal("fallback was stored as an original layout or overwrote the old checkpoint")
			}
			_, rec = send(history+`,{"role":"assistant","content":"fresh reply","reasoning_content":"new thought"},{"role":"user","content":"next"}`, 2)
			if rec.MistralLayout.Fallback != 1 || rec.MistralLayout.Restored != 1 {
				t.Fatalf("mixed history did not use fallback and exact recovery independently: %+v", rec.MistralLayout)
			}
			assertUpload(1, fallbackContent, nativeNext)
			nativeHistory := strings.Replace(history, `"content":"before after","reasoning_content":"private-thought-中文"`, `"content":`+layoutNativeFixture, 1)
			_, rec = send(nativeHistory, 3)
			if rec.MistralLayout.Native != 1 || rec.MistralLayout.Fallback != 0 {
				t.Fatal("authoritative native history was treated as fallback")
			}
			assertUpload(2, layoutNativeFixture, "")
		})
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
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("X-Conversation-Layout"), "restored=1") || e.count() != 2 {
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
				if r, ok, err := s.get(s.textKey(scope, "before after", "private-thought-中文")); ok && err == nil {
					s.refresh(r)
				}
				s.export()
			}
		}(i)
	}
	wg.Wait()
	if len(s.export()) != 12 {
		t.Fatal("concurrent state lost or duplicate-key conflict invented")
	}
}
