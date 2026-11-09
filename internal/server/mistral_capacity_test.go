package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func capacityPerformance(t *testing.T, e *mistralEnv, change func(*config.MistralPerformance)) {
	t.Helper()
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		p := c.Mistral.EffectivePerformance()
		change(&p)
		c.Mistral.Performance = &p
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestMistralCapacitySettingsAPIIsolationAndRestart(t *testing.T) {
	e := newMistralEnv(t)
	before := e.s.cfg.Get()
	w := httptest.NewRecorder()
	e.s.apiSettings(w, httptest.NewRequest("GET", "/api/settings?provider=mistral", nil))
	var response struct {
		Settings settingsDoc `json:"settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	doc := response.Settings
	n := 100000
	doc.Mistral.BindingCapacity = &n
	p := config.DefaultMistral().EffectivePerformance()
	p.MaxBufferedMB = 64
	p.MaxWaitingRequests = 8
	p.DetailSamplePercent = 25
	doc.Mistral.Performance = &p
	doc.Routing.DirectMaxConns = 7
	doc.Routing.ProxyMaxConns = 3
	save := func(doc settingsDoc, provider string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(doc)
		w := httptest.NewRecorder()
		e.s.apiSaveSettings(w, httptest.NewRequest("PUT", "/api/settings?provider="+provider, bytes.NewReader(raw)))
		return w
	}
	if w = save(doc, "mistral"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	check := func() {
		t.Helper()
		c := e.s.cfg.Get()
		if c.Mistral.BindingCapacity == nil || *c.Mistral.BindingCapacity != 100000 || c.Mistral.EffectivePerformance() != p || c.ForProvider(config.ProviderMistral).Routing.DirectMaxConns != 7 {
			t.Fatal("settings lost")
		}
		if !reflect.DeepEqual(before.Routing, c.Routing) || !reflect.DeepEqual(before.TokenHarbor, c.TokenHarbor) || before.Quota != c.Quota {
			t.Fatal("changed another pool")
		}
		if cap, ind := e.s.pool.BindingCapacityFor(config.ProviderMistral); cap != 100000 || !ind {
			t.Fatal("saved setting not applied")
		}
	}
	check()
	if err := e.s.Reload(); err != nil {
		t.Fatal(err)
	}
	check()
	restored, err := New(e.s.dataDir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.logs.Close()
	if cap, ind := restored.pool.BindingCapacityFor(config.ProviderMistral); cap != 100000 || !ind {
		t.Fatal("restart not configured")
	}
	// Omitted fields from older clients preserve existing independent settings.
	doc.Revision = e.s.cfg.Get().Revision
	doc.Mistral.BindingCapacity = nil
	doc.Mistral.Performance = nil
	if w = save(doc, "mistral"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	check()
	stable := e.s.cfg.Get()
	bad := -1
	doc.Revision = stable.Revision
	doc.Mistral.BindingCapacity = &bad
	if w = save(doc, "mistral"); w.Code != 400 || e.s.cfg.Get() != stable {
		t.Fatal("invalid setting published")
	}
	// Another provider cannot overwrite Mistral-only settings through an echo.
	doc.Revision = stable.Revision
	doc.Routing = before.Routing
	doc.Quota = before.Quota
	bad = 2
	if w = save(doc, "opendesign"); w.Code != 200 {
		t.Fatal(w.Body)
	}
	check()
	w = httptest.NewRecorder()
	e.s.apiOverview(w, httptest.NewRequest("GET", "/api/overview?provider=mistral", nil))
	var overview map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &overview)
	if overview["bindingLimit"] != float64(100000) || overview["bindingIndependent"] != true || overview["sharedBindingLimit"] != float64(4096) {
		t.Fatal("overview mislabeled capacity", w.Body)
	}
}
func TestMistralTransportUsesIndependentEffectiveSettings(t *testing.T) {
	e := newMistralEnv(t)
	root := e.s.transport("")
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		r := c.Routing
		r.DirectMaxConns = 7
		r.ProxyMaxConns = 3
		c.Mistral.Routing = &r
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tr := e.s.mistralHTTPClient(e.s.cfg.Get(), "").Transport.(*http.Transport)
	proxy := e.s.mistralHTTPClient(e.s.cfg.Get(), "http://127.0.0.1:9").Transport.(*http.Transport)
	defer tr.CloseIdleConnections()
	defer proxy.CloseIdleConnections()
	if tr.MaxConnsPerHost != 7 || proxy.MaxConnsPerHost != 3 || root == tr || e.s.transport("") != root {
		t.Fatal("transport settings leaked/ignored")
	}
	_, err = e.s.cfg.Update(-1, func(c *config.Config) error { c.Mistral.Routing.DirectMaxConns = 9; return nil })
	if err != nil {
		t.Fatal(err)
	}
	next := e.s.mistralHTTPClient(e.s.cfg.Get(), "").Transport.(*http.Transport)
	defer next.CloseIdleConnections()
	if next == tr || next.MaxConnsPerHost != 9 || e.s.transport("") != root {
		t.Fatal("config did not replace only Mistral idle pool")
	}
}
func TestMistralBudgetRejectsBeforeUpstreamAndReleases(t *testing.T) {
	e := newMistralEnv(t)
	capacityPerformance(t, e, func(p *config.MistralPerformance) { p.MaxBufferedMB = 1 })
	body := `{"model":"m","prompt_cache_key":"fixed","messages":[{"role":"user","content":"` + strings.Repeat("x", 1100<<10) + `"}]}`
	if status, _ := e.post(t, "/v1/chat/completions", body, nil); status != 429 || e.count() != 0 || e.s.mistralBuffered.Load() != 0 {
		t.Fatal("oversize byte budget spent upstream or leaked")
	}
	// A missing cache key causes a second retained prepared body: charge it too.
	body = `{"model":"m","messages":[{"role":"user","content":"` + strings.Repeat("x", 700<<10) + `"}]}`
	if status, _ := e.post(t, "/v1/chat/completions", body, nil); status != 429 || e.count() != 0 || e.s.mistralBuffered.Load() != 0 {
		t.Fatal("prepared body escaped budget")
	}
}
func TestMistralAggregateBudgetConcurrencyAndRecovery(t *testing.T) {
	e := newMistralEnv(t)
	capacityPerformance(t, e, func(p *config.MistralPerformance) { p.MaxBufferedMB = 1 })
	arrived := make(chan struct{}, 1)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		arrived <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	})
	body := `{"model":"m","prompt_cache_key":"fixed","messages":[{"role":"user","content":"` + strings.Repeat("x", 700<<10) + `"}]}`
	done := make(chan int, 1)
	go func() { status, _ := e.post(t, "/v1/chat/completions", body, nil); done <- status }()
	select {
	case <-arrived:
	case <-time.After(3 * time.Second):
		t.Fatal("first request not admitted")
	}
	if status, _ := e.post(t, "/v1/chat/completions", body, nil); status != 429 || e.count() != 1 {
		t.Fatal("aggregate budget not enforced")
	}
	close(release)
	if <-done != 200 {
		t.Fatal("admitted request failed")
	}
	if e.s.mistralBuffered.Load() != 0 {
		t.Fatal("retained input leaked")
	}
}
func TestMistralChunkedAndEventBudgetRelease(t *testing.T) {
	e := newMistralEnv(t)
	capacityPerformance(t, e, func(p *config.MistralPerformance) { p.MaxBufferedMB = 1 })
	cfg := e.s.cfg.Get()
	r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(strings.Repeat("x", 1024)))
	r.ContentLength = -1
	body, b, err := e.s.readMistralBody(httptest.NewRecorder(), r, cfg)
	if err != nil || len(body) != 1024 {
		t.Fatal(err)
	}
	b.release()
	r = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(strings.Repeat("x", 2<<20)))
	r.ContentLength = -1
	_, b, err = e.s.readMistralBody(httptest.NewRecorder(), r, cfg)
	b.release()
	if !errors.Is(err, errMistralBufferLimit) {
		t.Fatal("chunked budget", err)
	}
	_, release, err := e.s.readMistralEvent(bufio.NewReader(strings.NewReader("data: "+strings.Repeat("x", 2<<20)+"\n\n")), cfg)
	release()
	if !errors.Is(err, errMistralBufferLimit) || e.s.mistralBuffered.Load() != 0 {
		t.Fatal("event budget leaked", err)
	}
	_, release, err = e.s.readMistralResponse(strings.NewReader(strings.Repeat("x", 2<<20)), cfg)
	release()
	if !errors.Is(err, errMistralBufferLimit) || e.s.mistralBuffered.Load() != 0 {
		t.Fatal("response budget leaked", err)
	}
}
func TestMistralResponseBudgetNeverReplaysOrBlamesAccount(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			e := newMistralEnv(t)
			capacityPerformance(t, e, func(p *config.MistralPerformance) { p.MaxBufferedMB = 1 })
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
					w.(http.Flusher).Flush()
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", strings.Repeat("x", 2<<20))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}]}`, strings.Repeat("x", 2<<20))
			})
			status, body := e.post(t, "/v1/chat/completions", fmt.Sprintf(`{"model":"m","stream":%t,"messages":[]}`, stream), nil)
			want := 502
			if stream {
				want = 200
			}
			if status != want || !strings.Contains(body, "gateway_response_limit") || strings.Contains(body, "[DONE]") || e.count() != 1 {
				t.Fatal("response limit replay/terminal", status, e.count())
			}
			if e.s.mistralBuffered.Load() != 0 {
				t.Fatal("response/event budget leaked")
			}
			for _, v := range e.s.pool.Views(e.s.cfg.Get()) {
				if v.Samples != 0 {
					t.Fatal("local memory limit degraded account health")
				}
			}
		})
	}
}

func TestMistralUploadDeadlineRealSocket(t *testing.T) {
	e := newMistralEnv(t)
	capacityPerformance(t, e, func(p *config.MistralPerformance) { p.UploadTimeoutSec = 1 })
	conn, err := net.Dial("tcp", strings.TrimPrefix(e.front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: test\r\nAuthorization: Bearer mistral-client\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\n{")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 408 || e.count() != 0 {
		t.Fatal("slow upload not rejected before upstream", resp.StatusCode)
	}
}

type deadlineProbeWriter struct {
	h         http.Header
	deadlines []time.Time
	writes    int
}

func (w *deadlineProbeWriter) Header() http.Header { return w.h }
func (w *deadlineProbeWriter) WriteHeader(int)     {}
func (w *deadlineProbeWriter) SetWriteDeadline(t time.Time) error {
	w.deadlines = append(w.deadlines, t)
	return nil
}
func (w *deadlineProbeWriter) Write(b []byte) (int, error) {
	w.writes++
	if len(w.deadlines) == 0 || w.deadlines[len(w.deadlines)-1].IsZero() {
		return 0, errors.New("no deadline")
	}
	return 0, context.DeadlineExceeded
}
func (w *deadlineProbeWriter) Flush() {}
func TestMistralWriteDeadlineAndQueuedEventCancellation(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 50; i++ {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"mock\"}}]}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[]}`))
	req.Header.Set("Authorization", "Bearer mistral-client")
	w := &deadlineProbeWriter{h: http.Header{}}
	e.s.handleProxy(w, req)
	if w.writes != 1 || len(w.deadlines) < 2 || !w.deadlines[len(w.deadlines)-1].IsZero() || e.s.mistralBuffered.Load() != 0 {
		t.Fatal("write timeout/cancel left budgets", w.writes, e.s.mistralBuffered.Load())
	}
}
func TestMistralSamplingAndLazyIdentityPreserveSemantics(t *testing.T) {
	e := newMistralEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error { on := true; c.Mistral.DetailLog = &on; return nil })
	if err != nil {
		t.Fatal(err)
	}
	capacityPerformance(t, e, func(p *config.MistralPerformance) { p.DetailSamplePercent = 0 })
	if status, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil); status != 200 {
		t.Fatal(status)
	}
	d, _ := e.s.logs.Query("details", nil, time.Time{}, time.Time{}, 20, 0)
	q, _ := e.s.logs.Query("requests", nil, time.Time{}, time.Time{}, 20, 0)
	if len(d.Items) != 0 || len(q.Items) != 1 {
		t.Fatal("sampling suppressed summaries or captured bodies")
	}
	for _, raw := range []string{`{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`, `{"model":"m","input":"hello","instructions":"system"}`, `{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":"u"}]}`} {
		eager, err := parseRoutingBody([]byte(raw), true)
		if err != nil {
			t.Fatal(err)
		}
		lazy, err := parseRoutingBodyWithHistory([]byte(raw), true, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, headers := range []http.Header{{}, {"Session-Id": []string{"stable"}}} {
			a := e.s.mistralIdentity(e.s.cfg.Get(), "g", headers, eager)
			b := e.s.mistralIdentity(e.s.cfg.Get(), "g", headers, lazy)
			if a != b {
				t.Fatal("lazy history changed identity")
			}
		}
	}
}
