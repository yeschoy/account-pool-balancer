package server

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"account-pool-balancer/internal/config"
)

type wireMessageLog struct {
	Headers       map[string][]string `json:"headers"`
	Body          string              `json:"body"`
	BodyBytes     int                 `json:"bodyBytes"`
	BodyTruncated bool                `json:"bodyTruncated"`
}

type upstreamWireLog struct {
	AttemptIndex int             `json:"attemptIndex"`
	AccountID    string          `json:"accountId"`
	Method       string          `json:"method"`
	Path         string          `json:"path"`
	Sent         bool            `json:"sent"`
	Status       int             `json:"status"`
	Terminal     string          `json:"terminal,omitempty"`
	Request      wireMessageLog  `json:"request"`
	Response     *wireMessageLog `json:"response,omitempty"`
}

// Only the current Mistral detail boundary is extended. Other providers retain
// their own detail switches and existing records. Capture is absent when off.
type upstreamDetailCapture struct {
	mu              sync.Mutex
	cfg             *config.Config
	log             upstreamWireLog
	requestHeaders  http.Header
	responseHeaders http.Header
	identities      []string
	body            captureWriter
}

func newMistralUpstreamDetail(cfg *config.Config, req *http.Request, rec *requestLog, body []byte, attemptNo int, identities []string) *upstreamDetailCapture {
	if rec.Provider != config.ProviderMistral {
		return nil
	}
	enabled := cfg.Logs.Detail
	if cfg.Mistral.DetailLog != nil {
		enabled = *cfg.Mistral.DetailLog
	}
	if !enabled {
		return nil
	}
	limit := min(cfg.Logs.DetailBodyKB, 64) << 10
	ids := append([]string(nil), identities...)
	ids = append(ids, thIdentityValues(req.Header, nil, cfg.Routing.SessionHeaders)...)
	c := &upstreamDetailCapture{cfg: cfg, requestHeaders: req.Header.Clone(), identities: ids,
		body: captureWriter{ResponseWriter: discardLogBody{}, max: limit},
		log:  upstreamWireLog{AttemptIndex: attemptNo - 1, AccountID: rec.AccountID, Method: req.Method, Path: req.URL.Path}}
	// Host/content length are request fields, not necessarily present in Header.
	// HTTP trace below adds the transport's actual serialized fields, including UA.
	c.requestHeaders.Set("Host", req.URL.Host)
	c.requestHeaders.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
	c.log.Request = c.preview(req.Header, body, len(body), false)
	return c
}

func (c *upstreamDetailCapture) recordHeader(key string, values []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestHeaders[http.CanonicalHeaderKey(key)] = append([]string(nil), values...)
}

func (c *upstreamDetailCapture) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body.Write(b)
}

func (c *upstreamDetailCapture) attach(resp *http.Response) {
	c.mu.Lock()
	c.log.Status = resp.StatusCode
	c.responseHeaders = resp.Header.Clone()
	c.identities = append(c.identities, thIdentityValues(resp.Header, nil, c.cfg.Routing.SessionHeaders)...)
	for _, cookie := range resp.Cookies() {
		if v, ok := validValue(cookie.Value); ok && len(v) >= 8 {
			c.identities = append(c.identities, v)
		}
	}
	c.mu.Unlock()
	// Tee only the bytes the relay reads. Close still reaches the actual body;
	// no extra read, goroutine, whole-response buffer or delayed frame flush.
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.TeeReader(resp.Body, c), resp.Body}
}

func (c *upstreamDetailCapture) privateHeaders(h http.Header) map[string][]string {
	out := maskHeaders(c.cfg, thPrivateHeaders(h))
	for key := range out {
		for _, name := range c.cfg.Routing.SessionHeaders {
			if strings.EqualFold(key, name) {
				out[key] = []string{"[REDACTED]"}
			}
		}
	}
	return out
}

func (c *upstreamDetailCapture) preview(h http.Header, body []byte, size int, truncated bool) wireMessageLog {
	private, cut := thBodyPreview(body, c.identities, c.body.max)
	text, maskCut := maskBody(c.cfg, private, c.body.max)
	return wireMessageLog{Headers: c.privateHeaders(h), Body: text, BodyBytes: size, BodyTruncated: truncated || cut || maskCut}
}

func (c *upstreamDetailCapture) finish(res *attemptResult) (upstreamWireLog, []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log.Sent, c.log.Terminal = res.sent.Load(), res.terminal
	c.log.Request.Headers = c.privateHeaders(c.requestHeaders)
	for _, id := range c.identities {
		c.log.Request.Body = strings.ReplaceAll(c.log.Request.Body, id, "[REDACTED]")
	}
	if c.log.Status != 0 {
		body, cut := c.body.body()
		partial := res.kind == outCancelled || res.terminal == "network" || res.terminal == "timeout" || res.terminal == "proxy" || res.upstreamStream && res.result != "success"
		preview := c.preview(c.responseHeaders, body, c.body.n, cut || res.bodyTruncated || partial)
		c.log.Response = &preview
	}
	return c.log, append([]string(nil), c.identities...)
}
