package server

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"runtime/debug"
	"time"

	"account-pool-balancer/internal/config"
)

var errMistralBufferLimit = errors.New("Mistral gateway byte budget exhausted")

type mistralBuffer struct {
	server *Server
	cfg    *config.Config
	bytes  int64
	data   []byte
}

func (s *Server) mistralBufferLimit(cfg *config.Config) int64 {
	limit := int64(cfg.Mistral.EffectivePerformance().MaxBufferedMB) << 20
	if s.cfg != nil {
		limit = min(limit, int64(s.cfg.Get().Mistral.EffectivePerformance().MaxBufferedMB)<<20)
	}
	if soft := debug.SetMemoryLimit(-1); soft > 0 {
		limit = min(limit, max(1<<20, soft/3))
	}
	return limit
}
func (b *mistralBuffer) reserve(n int64) bool {
	limit := b.server.mistralBufferLimit(b.cfg)
	for {
		used := b.server.mistralBuffered.Load()
		if n < 0 || n > limit-used {
			return false
		}
		if b.server.mistralBuffered.CompareAndSwap(used, used+n) {
			b.bytes += n
			return true
		}
	}
}
func (b *mistralBuffer) release() { b.server.mistralBuffered.Add(-b.bytes); b.bytes = 0; b.data = nil }
func (b *mistralBuffer) append(p []byte, maxBytes int64) error {
	need := len(b.data) + len(p)
	if int64(need) > maxBytes {
		return errEventTooLarge
	}
	if need > cap(b.data) {
		next := max(256, max(need, cap(b.data)*2))
		if int64(next) > maxBytes {
			next = int(maxBytes)
		}
		if !b.reserve(int64(next)) {
			return errMistralBufferLimit
		}
		data := make([]byte, len(b.data), next)
		copy(data, b.data)
		old := int64(cap(b.data))
		b.server.mistralBuffered.Add(-old)
		b.bytes -= old
		b.data = data
	}
	b.data = append(b.data, p...)
	return nil
}
func (s *Server) readMistralBody(w http.ResponseWriter, r *http.Request, cfg *config.Config) ([]byte, *mistralBuffer, error) {
	b := &mistralBuffer{server: s, cfg: cfg}
	limit := int64(cfg.Routing.MaxBodyMB) << 20
	if r.ContentLength > limit {
		return nil, b, &http.MaxBytesError{Limit: limit}
	}
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(time.Duration(cfg.Mistral.EffectivePerformance().UploadTimeoutSec) * time.Second))
	complete := false
	defer func() {
		if complete {
			_ = rc.SetReadDeadline(time.Time{})
		} else {
			// Do not clear an expired deadline before net/http's unread-body
			// cleanup, which would otherwise block sending the local rejection.
			_ = rc.SetReadDeadline(time.Now())
			if r.ProtoMajor < 2 {
				w.Header().Set("Connection", "close")
			}
		}
	}()
	rd := http.MaxBytesReader(w, r.Body, limit)
	if r.ContentLength >= 0 {
		if !b.reserve(r.ContentLength) {
			return nil, b, errMistralBufferLimit
		}
		b.data = make([]byte, int(r.ContentLength))
		nread, err := io.ReadFull(rd, b.data)
		if err == nil {
			var extra [1]byte
			n, e := rd.Read(extra[:])
			if n != 0 {
				err = errTHBodyLength
			} else if e != nil && e != io.EOF {
				err = e
			}
		}
		complete = err == nil
		return b.data[:nread], b, err
	}
	var chunk [32 << 10]byte
	for {
		n, err := rd.Read(chunk[:])
		if n > 0 {
			if e := b.append(chunk[:n], limit); e != nil {
				return nil, b, e
			}
		}
		if err == io.EOF {
			complete = true
			return b.data, b, nil
		}
		if err != nil {
			return nil, b, err
		}
	}
}
func (s *Server) readMistralResponse(rd io.Reader, cfg *config.Config) ([]byte, func(), error) {
	b := &mistralBuffer{server: s, cfg: cfg}
	var chunk [32 << 10]byte
	for {
		n, err := rd.Read(chunk[:])
		if n > 0 {
			if e := b.append(chunk[:n], 512<<20); e != nil {
				b.release()
				return nil, func() {}, e
			}
		}
		if err == io.EOF {
			return b.data, b.release, nil
		}
		if err != nil {
			b.release()
			return nil, func() {}, err
		}
	}
}
func (s *Server) readMistralEvent(rd *bufio.Reader, cfg *config.Config) ([]byte, func(), error) {
	b := &mistralBuffer{server: s, cfg: cfg}
	for {
		line, err := rd.ReadSlice('\n')
		if e := b.append(line, maxEventBytes); e != nil {
			b.release()
			return nil, func() {}, e
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return b.data, b.release, err
		}
		if len(line) == 1 || len(line) == 2 && line[0] == '\r' {
			if len(b.data) == len(line) {
				b.data = b.data[:0]
				continue
			}
			return b.data, b.release, nil
		}
	}
}
func mistralDetailSelected(cfg *config.Config, rid string) bool {
	p := cfg.Mistral.EffectivePerformance().DetailSamplePercent
	if p >= 100 {
		return true
	}
	if p <= 0 {
		return false
	}
	var h uint32 = 2166136261
	for i := 0; i < len(rid); i++ {
		h = (h ^ uint32(rid[i])) * 16777619
	}
	return h%100 < uint32(p)
}

func mistralResourceFailure(res *attemptResult) *attemptResult {
	res.kind, res.status, res.terminal, res.category = outFailed, 502, "resource_limit", "gateway-buffer-limit"
	res.body = []byte(`{"error":{"type":"api_error","code":"gateway_response_limit","message":"gateway response byte budget exceeded; request was not replayed"}}`)
	res.contentType = "application/json"
	res.evaluated = true
	return res
}
