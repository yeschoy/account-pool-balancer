package server

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"account-pool-balancer/internal/config"
)

// Detail records (logs.detail) are built off the request path. Building one
// walks and redacts the whole request body (thBodyPreview), runs the secret
// regexes over up to the body cap and JSON-redacts the captured response; on a
// long agent conversation (≈1 MB Gemini bodies from Amp) that is tens of
// milliseconds of CPU. finish runs before the handler returns, and the client
// only sees the end of a response (the last chunk of a stream, or the whole
// buffered body of a short JSON reply) once the handler has returned, so a
// synchronous build delayed every request with detail logging on.
//
// The queue is bounded by job count and by the bytes the jobs keep alive. A
// full queue drops the detail record (counted in logs dropped) instead of
// blocking the request: a diagnostic record must never slow the link it
// describes. The request log itself is still written synchronously.
const (
	detailWorkers  = 2
	detailMaxJobs  = 256
	detailMaxBytes = 256 << 20
)

// detailInline lets package tests keep legacy immediate detail-log reads
// deterministic while opting individual servers into the real background path.
// Production leaves this hook nil.
var detailInline func(*Server) bool

type detailJob struct {
	cost  int64
	build func()
}

type detailQueue struct {
	mu      sync.Mutex
	started bool
	closed  bool
	ch      chan detailJob
	queued  atomic.Int64
	pending sync.WaitGroup // submitted jobs not yet finished
	workers sync.WaitGroup
}

// deferDetail runs build in the background detail workers. cost is the number
// of bytes build keeps referenced (request body, captured response). It
// reports false when the record was dropped because the queue is full or the
// server is shutting down; build is not run in that case.
func (s *Server) deferDetail(cost int64, build func()) bool {
	if detailInline != nil && detailInline(s) {
		build()
		return true
	}
	return s.enqueueDetail(cost, build)
}

func (s *Server) enqueueDetail(cost int64, build func()) bool {
	q := &s.details
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		s.noteDetailDropped()
		return false
	}
	if !q.started {
		q.started = true
		q.ch = make(chan detailJob, detailMaxJobs)
		for range detailWorkers {
			q.workers.Add(1)
			go q.run(s.noteDetailDropped)
		}
	}
	if cost < 0 {
		cost = 0
	}
	if q.queued.Load()+cost > detailMaxBytes {
		s.noteDetailDropped()
		return false
	}
	// Reserve before publishing: a worker may complete as soon as it receives
	// the job, and queued bytes must never transiently become negative.
	q.queued.Add(cost)
	q.pending.Add(1)
	select {
	case q.ch <- detailJob{cost: cost, build: build}:
		return true
	default:
		q.queued.Add(-cost)
		q.pending.Done()
		s.noteDetailDropped()
		return false
	}
}

func (q *detailQueue) run(noteDropped func()) {
	defer q.workers.Done()
	for j := range q.ch {
		func() {
			defer func() {
				if v := recover(); v != nil {
					noteDropped()
					log.Printf("detail log build panicked: %v", v)
				}
				q.queued.Add(-j.cost)
				q.pending.Done()
			}()
			j.build()
		}()
	}
}

// waitDetails blocks until every submitted detail build has finished.
func (s *Server) waitDetails() { s.details.pending.Wait() }

// closeDetails finishes the queued builds and refuses new ones. It runs before
// the log store closes, because a build appends to it.
func (s *Server) closeDetails() {
	q := &s.details
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	if q.started {
		close(q.ch)
	}
	q.mu.Unlock()
	q.workers.Wait()
}

func (s *Server) noteDetailDropped() {
	if s.logs != nil {
		s.logs.NoteDropped()
	}
}

// detailInput is what a completed request's detail record is built from. It
// owns no request-local mutable state: headers are clones, respBody is the
// capture writer's copy, and reqBody is the handler's own read (never a pooled
// buffer when the build is deferred).
type detailInput struct {
	cfg        *config.Config
	d          *detailLog
	max        int
	reqHeader  http.Header
	respHeader http.Header
	reqBody    []byte
	respBody   []byte
	respCut    bool
	identities []string
}

// buildDetail redacts and appends one completion detail record.
func (s *Server) buildDetail(in detailInput) {
	cfg, d, provider := in.cfg, in.d, in.d.Provider
	d.RequestHeaders = maskHeaders(cfg, in.reqHeader)
	d.ResponseHeaders = maskHeaders(cfg, in.respHeader)
	redactSession := func() {
		for _, name := range cfg.Routing.SessionHeaders {
			for key := range d.RequestHeaders {
				if strings.EqualFold(key, name) {
					d.RequestHeaders[key] = []string{"[REDACTED]"}
				}
			}
		}
	}
	capturedBody := in.reqBody
	if provider == config.ProviderOpenCodeGo {
		d.RequestHeaders = maskHeaders(cfg, goPrivateHeaders(in.reqHeader))
		redactSession()
		d.ResponseHeaders = maskHeaders(cfg, goPrivateHeaders(in.respHeader))
		capturedBody = goPrivateBody(in.reqBody)
	}
	if provider == config.ProviderTokenHarbor || provider == config.ProviderMistral || config.NativeGateway(provider) {
		d.RequestHeaders = maskHeaders(cfg, thPrivateHeaders(in.reqHeader))
		redactSession()
		d.ResponseHeaders = maskHeaders(cfg, thPrivateHeaders(in.respHeader))
		var cut bool
		capturedBody, cut = thBodyPreview(in.reqBody, in.identities, in.max)
		d.RequestBodyTruncated = d.RequestBodyTruncated || cut
	}
	var reqCut bool
	d.RequestBody, reqCut = maskBody(cfg, capturedBody, in.max)
	d.RequestBodyTruncated = d.RequestBodyTruncated || reqCut
	respBody := in.respBody
	if provider == config.ProviderOpenCodeGo {
		respBody = goPrivateBody(respBody)
	} else if provider == config.ProviderTokenHarbor || provider == config.ProviderMistral || config.NativeGateway(provider) {
		respBody = thPrivateBody(respBody, in.identities)
	}
	d.ResponseBody, _ = maskBody(cfg, respBody, len(respBody))
	d.ResponseBodyTruncated = in.respCut
	s.logs.Append("details", d.TS, d)
}
