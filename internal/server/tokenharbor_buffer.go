package server

import (
	"errors"
	"io"
	"net/http"
	"time"
)

var errTHBufferLimit = errors.New("TokenHarbor request buffer budget exhausted")
var errTHBodyLength = errors.New("request Content-Length mismatch")

func (s *Server) reserveTHBuffer(n int64) bool {
	if n < 0 || n > s.thBufferLimit {
		return false
	}
	for {
		used := s.thBuffered.Load()
		if n > s.thBufferLimit-used {
			return false
		}
		if s.thBuffered.CompareAndSwap(used, used+n) {
			return true
		}
	}
}

// Limits retained native input bytes across long-running streams. Known
// lengths allocate once; chunked uploads reserve a conservative read budget,
// then shrink to actual capacity. Refusal is before any upstream request.
func (s *Server) readTHBody(w http.ResponseWriter, r *http.Request, maxBytes int64) ([]byte, func(), error) {
	noop := func() {}
	if r.ContentLength > maxBytes {
		return nil, noop, &http.MaxBytesError{Limit: maxBytes}
	}
	charge := r.ContentLength
	if charge < 0 {
		charge = 3 * maxBytes
	}
	if !s.reserveTHBuffer(charge) {
		return nil, noop, errTHBufferLimit
	}
	release := func() { s.thBuffered.Add(-charge) }
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(60 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	reader := http.MaxBytesReader(w, r.Body, maxBytes)
	var body []byte
	var err error
	if r.ContentLength >= 0 {
		body = make([]byte, int(r.ContentLength))
		_, err = io.ReadFull(reader, body)
		if err == nil {
			var extra [1]byte
			n, e := reader.Read(extra[:])
			if n != 0 {
				err = errTHBodyLength
			} else if e != nil && e != io.EOF {
				err = e
			}
		}
	} else {
		body, err = io.ReadAll(reader)
		if err == nil {
			actual := int64(cap(body))
			if actual > charge {
				if !s.reserveTHBuffer(actual - charge) {
					err = errTHBufferLimit
				} else {
					charge = actual
				}
			} else {
				s.thBuffered.Add(actual - charge)
				charge = actual
			}
		}
	}
	if err != nil {
		release()
		return nil, noop, err
	}
	return body, release, nil
}
