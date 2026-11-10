package server

import (
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"hash"
	"io"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"strings"
	"sync"

	"account-pool-balancer/internal/config"
)

type cacheAuditBodyRead struct {
	Bytes           int    `json:"bytes"`
	Tag             string `json:"tag"`
	Complete        bool   `json:"complete"`
	MatchesExpected bool   `json:"matchesExpected"`
	ReadError       bool   `json:"readError,omitempty"`
}

type cacheAuditConnection struct {
	ID        string `json:"id,omitempty"`
	Untracked bool   `json:"untracked,omitempty"`
	Reused    bool   `json:"reused"`
	WasIdle   bool   `json:"wasIdle"`
	IdleMs    int64  `json:"idleMs"`
}

type cacheAuditAttemptLog struct {
	AttemptIndex           int                        `json:"attemptIndex"`
	AccountID              string                     `json:"accountId"`
	SourceTag              string                     `json:"sourceTag"`
	TargetTag              string                     `json:"targetTag"`
	AffinityTag            string                     `json:"affinityTag,omitempty"`
	Expected               cacheBodyFingerprint       `json:"expected"`
	Preparation            cacheFingerprintComparison `json:"preparation"`
	Previous               *cachePreviousComparison   `json:"previous,omitempty"`
	Connections            []cacheAuditConnection     `json:"connections"`
	ConnectionEvents       int                        `json:"connectionEvents"`
	TLSHandshakes          int                        `json:"tlsHandshakes"`
	TLSResumed             bool                       `json:"tlsResumed"`
	WriteSuccesses         int                        `json:"writeSuccesses"`
	WriteFailures          int                        `json:"writeFailures"`
	BodyReads              []cacheAuditBodyRead       `json:"bodyReads"`
	BodyReadInstances      int                        `json:"bodyReadInstances"`
	EventsTruncated        bool                       `json:"eventsTruncated,omitempty"`
	Sent                   bool                       `json:"sent"`
	Status                 int                        `json:"status"`
	Terminal               string                     `json:"terminal,omitempty"`
	UpstreamRequestID      string                     `json:"upstreamRequestId,omitempty"`
	TraceRedacted          bool                       `json:"traceRedacted,omitempty"`
	RequestHTTP            *cacheAuditHTTP            `json:"requestHTTP,omitempty"`
	RequestHeadersObserved bool                       `json:"requestHeadersObserved,omitempty"`
	ResponseHTTP           *cacheAuditHTTP            `json:"responseHTTP,omitempty"`
	UpstreamProtocol       string                     `json:"upstreamProtocol,omitempty"`
	TLSALPN                string                     `json:"tlsALPN,omitempty"`
	Usage                  json.RawMessage            `json:"usage,omitempty"`
	UsageBytes             int                        `json:"usageBytes,omitempty"`
	UsageTag               string                     `json:"usageTag,omitempty"`
	UsageTruncated         bool                       `json:"usageTruncated,omitempty"`
	UsageSanitized         bool                       `json:"usageSanitized,omitempty"`
}

type cacheAuditReadState struct {
	mu  sync.Mutex
	h   hash.Hash
	n   int
	err bool
}

type cacheAuditReader struct {
	io.ReadCloser
	state *cacheAuditReadState
}

func (r *cacheAuditReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	r.state.mu.Lock()
	r.state.h.Write(p[:n])
	r.state.n += n
	r.state.err = r.state.err || err != nil && err != io.EOF
	r.state.mu.Unlock()
	return n, err
}

type cacheAuditAttempt struct {
	mu         sync.Mutex
	request    *mistralAuditRequest
	log        cacheAuditAttemptLog
	reads      []*cacheAuditReadState
	cfg        *config.Config
	identities []string // request-local only; never copied into a log/baseline
}

func (r *mistralAuditRequest) attempt(req *http.Request, trace *httptrace.ClientTrace, cfg *config.Config, acct config.Account, body []byte, index int, identities []string) *cacheAuditAttempt {
	if r == nil {
		return nil
	}
	if len(r.log.Attempts) >= mistralAuditAttempts {
		r.log.AttemptsTruncated = true
		return nil
	}
	a := r.audit
	fingerprint := a.fingerprint(body, r.prepared.view.Sequence, r.prepared)
	c := &cacheAuditAttempt{request: r, cfg: cfg, identities: append(append([]string(nil), identities...), acct.RuntimeKey), log: cacheAuditAttemptLog{AttemptIndex: index, AccountID: acct.ID, Expected: fingerprint.view, Preparation: compareCacheFingerprints(r.prepared, fingerprint), Connections: []cacheAuditConnection{}, BodyReads: []cacheAuditBodyRead{}}}
	headers := req.Header.Clone()
	headers.Set("Host", req.URL.Host)
	headers.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
	c.log.RequestHTTP = c.auditHeaders(headers)
	wroteHeader := trace.WroteHeaderField
	trace.WroteHeaderField = func(name string, values []string) {
		c.recordHeader(name, values)
		if wroteHeader != nil {
			wroteHeader(name, values)
		}
	}
	c.log.TargetTag = a.tag("target", []byte(req.URL.String()))
	c.log.SourceTag = a.tag("source", []byte(acct.ID), []byte(acct.RuntimeKey), []byte(acct.ProxyURL), []byte(req.URL.Scheme+"://"+req.URL.Host))
	if affinities := req.Header.Values("X-Affinity"); len(affinities) > 0 {
		values := make([][]byte, len(affinities))
		for i, value := range affinities {
			values[i] = []byte(value)
		}
		c.log.AffinityTag = a.tag("affinity-values", values...)
	}
	if r.historyKey != "" {
		a.mu.Lock()
		c.log.Previous = previousCacheComparison(a.wire[r.historyKey], fingerprint, true)
		a.remember(a.wire, r.historyKey, cacheAuditBaseline{requestID: r.requestID, accountID: acct.ID, attempt: index, done: r.done, prepared: fingerprint})
		a.mu.Unlock()
	}
	// Preserve transport replay semantics: observe every original/recreated
	// body rather than disabling GetBody or changing HTTP/2/connection policy.
	req.Body = c.wrap(req.Body)
	if get := req.GetBody; get != nil {
		req.GetBody = func() (io.ReadCloser, error) {
			body, err := get()
			if err != nil {
				return body, err
			}
			return c.wrap(body), nil
		}
	}
	// Compose hooks instead of replacing the admission/MarkSent callbacks.
	gotConn, wrote, tlsDone := trace.GotConn, trace.WroteRequest, trace.TLSHandshakeDone
	trace.GotConn = func(info httptrace.GotConnInfo) {
		id := a.connectionID(info.Conn)
		c.mu.Lock()
		c.log.ConnectionEvents++
		if len(c.log.Connections) < mistralAuditEvents {
			c.log.Connections = append(c.log.Connections, cacheAuditConnection{ID: id, Untracked: id == "", Reused: info.Reused, WasIdle: info.WasIdle, IdleMs: info.IdleTime.Milliseconds()})
		} else {
			c.log.EventsTruncated = true
		}
		c.mu.Unlock()
		if gotConn != nil {
			gotConn(info)
		}
	}
	trace.WroteRequest = func(info httptrace.WroteRequestInfo) {
		c.mu.Lock()
		if info.Err == nil {
			c.log.WriteSuccesses++
		} else {
			c.log.WriteFailures++
		}
		c.mu.Unlock()
		if wrote != nil {
			wrote(info)
		}
	}
	trace.TLSHandshakeDone = func(state tls.ConnectionState, err error) {
		c.mu.Lock()
		c.log.TLSHandshakes++
		c.log.TLSResumed = c.log.TLSResumed || state.DidResume
		c.mu.Unlock()
		if tlsDone != nil {
			tlsDone(state, err)
		}
	}
	return c
}

func (c *cacheAuditAttempt) wrap(body io.ReadCloser) io.ReadCloser {
	if body == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log.BodyReadInstances++
	if len(c.reads) >= mistralAuditEvents {
		c.log.EventsTruncated = true
		return body
	}
	s := &cacheAuditReadState{h: c.request.audit.bodyHash(c.log.Expected.Bytes)}
	c.reads = append(c.reads, s)
	return &cacheAuditReader{ReadCloser: body, state: s}
}

func (c *cacheAuditAttempt) finish(res *attemptResult) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.log
	// Clone the event slice: HTTP trace may still report a failed asynchronous
	// request write. The log is a snapshot, never shared mutable callback data.
	out.Connections = append([]cacheAuditConnection{}, out.Connections...)
	if out.RequestHTTP != nil {
		snapshot := *out.RequestHTTP
		snapshot.Headers = make(map[string]cacheAuditHeader, len(out.RequestHTTP.Headers))
		for name, header := range out.RequestHTTP.Headers {
			snapshot.Headers[name] = header
		}
		out.RequestHTTP = &snapshot
	}
	out.Sent, out.Status, out.Terminal = res.sent.Load(), res.upstreamStatus, res.terminal
	id := upstreamRequestID(res.header, config.ProviderMistral)
	private := c.cfg != nil && redactSecrets(c.cfg, id) != id
	for _, value := range c.identities {
		private = private || value != "" && strings.Contains(id, value)
	}
	if private {
		out.TraceRedacted = true
	} else {
		out.UpstreamRequestID = id
	}
	for _, s := range c.reads {
		s.mu.Lock()
		tag := hex.EncodeToString(s.h.Sum(nil)[:16])
		read := cacheAuditBodyRead{Bytes: s.n, Tag: tag, Complete: s.n == out.Expected.Bytes, MatchesExpected: s.n == out.Expected.Bytes && tag == out.Expected.Tag, ReadError: s.err}
		s.mu.Unlock()
		out.BodyReads = append(out.BodyReads, read)
	}
	c.request.log.Attempts = append(c.request.log.Attempts, out)
}
