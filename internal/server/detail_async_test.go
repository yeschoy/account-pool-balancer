package server

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/logstore"
)

func newDetailTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := logstore.Open(t.TempDir(), func() logstore.Limits {
		return logstore.Limits{RetentionDays: 7, MaxTotalBytes: 1 << 30, MaxRecords: map[string]int{"details": 500}}
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{logs: st}
	t.Cleanup(func() { s.closeDetails(); st.Close() })
	return s
}

// A queued build must not run on the caller: the request returns while the
// record is still being built, and the record appears afterwards.
func TestDetailQueueRunsOffCaller(t *testing.T) {
	s := newDetailTestServer(t)
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	done := make(chan struct{})
	start := time.Now()
	if !s.enqueueDetail(10, func() { <-release; close(done) }) {
		t.Fatal("job refused")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("enqueue blocked on the build")
	}
	unblock()
	s.waitDetails()
	select {
	case <-done:
	default:
		t.Fatal("waitDetails returned before the build finished")
	}
}

// A full queue sheds the record and counts it instead of blocking.
func TestDetailQueueDropsWhenFull(t *testing.T) {
	s := newDetailTestServer(t)
	block := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(block) })
	defer unblock()
	var started sync.WaitGroup
	started.Add(detailWorkers)
	for range detailWorkers {
		s.enqueueDetail(1, func() { started.Done(); <-block })
	}
	started.Wait()
	for range detailMaxJobs {
		if !s.enqueueDetail(1, func() {}) {
			t.Fatal("queue refused below its job bound")
		}
	}
	before := s.logs.Dropped()
	start := time.Now()
	if s.enqueueDetail(1, func() { t.Error("dropped job ran") }) {
		t.Fatal("full queue accepted a job")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("full queue blocked")
	}
	if s.enqueueDetail(detailMaxBytes, func() { t.Error("oversized job ran") }) {
		t.Fatal("byte bound not enforced")
	}
	if got := s.logs.Dropped() - before; got != 2 {
		t.Fatalf("dropped = %d, want 2", got)
	}
	unblock()
	s.waitDetails()
	if q := s.details.queued.Load(); q != 0 {
		t.Fatalf("queued bytes leaked: %d", q)
	}
}

// Shutdown runs every accepted build, then refuses new ones so nothing
// appends to a closed log store; a panicking build does not kill a worker.
func TestDetailQueueCloseDrains(t *testing.T) {
	s := newDetailTestServer(t)
	ran := 0
	var mu sync.Mutex
	s.enqueueDetail(1, func() { panic("boom") })
	for range 10 {
		s.enqueueDetail(1, func() { mu.Lock(); ran++; mu.Unlock() })
	}
	s.closeDetails()
	if ran != 10 {
		t.Fatalf("ran = %d, want 10", ran)
	}
	if s.logs.Dropped() != 1 {
		t.Fatal("panicking build was not counted as dropped")
	}
	if s.enqueueDetail(1, func() { t.Error("ran after close") }) {
		t.Fatal("closed queue accepted a job")
	}
}

func TestDetailQueueByteBound(t *testing.T) {
	s := newDetailTestServer(t)
	block := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(block) })
	defer unblock()
	if !s.enqueueDetail(detailMaxBytes, func() { <-block }) {
		t.Fatal("job at the byte limit was refused")
	}
	if s.enqueueDetail(1, func() { t.Error("over-budget job ran") }) {
		t.Fatal("byte budget accepted excess despite spare channel capacity")
	}
	unblock()
	s.waitDetails()
	if !s.enqueueDetail(detailMaxBytes, func() {}) {
		t.Fatal("completed build did not release the byte budget")
	}
	s.closeDetails()
	if got := s.details.queued.Load(); got != 0 {
		t.Fatalf("queued bytes leaked: %d", got)
	}
}

func TestDetailQueueConcurrentClose(t *testing.T) {
	s := newDetailTestServer(t)
	var accepted, ran atomic.Int64
	var submitters sync.WaitGroup
	for range 16 {
		submitters.Add(1)
		go func() {
			defer submitters.Done()
			for range 100 {
				if s.enqueueDetail(1024, func() { ran.Add(1) }) {
					accepted.Add(1)
				}
			}
		}()
	}
	s.closeDetails()
	submitters.Wait()
	if got, want := ran.Load(), accepted.Load(); got != want {
		t.Fatalf("shutdown ran %d of %d accepted builds", got, want)
	}
	if got := s.details.queued.Load(); got != 0 {
		t.Fatalf("queued bytes after shutdown: %d", got)
	}
}

func TestFactoryDetailAsyncPrivacyAndDrain(t *testing.T) {
	e := newFactoryEnv(t)
	useAsyncDetails(t, e.s)
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Logs.Detail = false
		c.Factory.DetailLog = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Occupy both workers: completion must return without waiting for any
	// detail work, then shutdown must drain and privately persist the record.
	block := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(block) })
	defer unblock()
	var started sync.WaitGroup
	started.Add(detailWorkers)
	for range detailWorkers {
		e.s.enqueueDetail(0, func() { started.Done(); <-block })
	}
	started.Wait()
	body := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"metadata":{"session_id":"private-async-session"}}`
	rsp, out := e.do(t, "/v1beta/models/gemini-4-neon:generateContent", body, map[string]string{"X-Session-Id": "private-async-session", "X-Amp-Thread-Id": "T-async"})
	if rsp.StatusCode != 200 {
		t.Fatal(rsp.StatusCode, out)
	}
	filters := map[string]string{"requestId": rsp.Header.Get("X-Request-Id")}
	page, _ := e.s.logs.Query("details", filters, time.Time{}, time.Time{}, 10, 0)
	if len(page.Items) != 0 {
		t.Fatal("detail was built inline despite blocked workers")
	}
	unblock()
	e.s.closeDetails()
	page, _ = e.s.logs.Query("details", filters, time.Time{}, time.Time{}, 10, 0)
	if len(page.Items) != 1 {
		t.Fatalf("shutdown lost completion detail: %d", len(page.Items))
	}
	var detail detailLog
	if err := json.Unmarshal(page.Items[0], &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Status != 200 || !strings.Contains(detail.RequestBody, "hello") || !strings.Contains(detail.ResponseBody, "OK") {
		t.Fatal("async detail lost request/response data")
	}
	for _, secret := range []string{"private-async-session", "downstream-ms", "fk-mock_factory_key_1"} {
		if strings.Contains(string(page.Items[0]), secret) {
			t.Fatal("async detail leaked private identity or credential")
		}
	}
}
