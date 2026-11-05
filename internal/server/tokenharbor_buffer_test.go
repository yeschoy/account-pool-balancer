package server

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTHBufferBudgetReleaseKnownChunkedAndReadFailure(t *testing.T) {
	s := &Server{thBufferLimit: 512}
	for _, mode := range []string{"known", "chunked", "short"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader("123456789"))
		if mode == "chunked" {
			r.ContentLength = -1
		}
		if mode == "short" {
			r.ContentLength = 20
		}
		body, release, err := s.readTHBody(httptest.NewRecorder(), r, 64)
		if mode == "short" {
			if err == nil {
				t.Fatal("short upload accepted")
			}
		} else if err != nil || string(body) != "123456789" {
			t.Fatal("read failed", mode, err)
		}
		if err == nil && s.thBuffered.Load() != int64(cap(body)) {
			t.Fatal("retained capacity not charged", mode, s.thBuffered.Load(), cap(body))
		}
		release()
		if s.thBuffered.Load() != 0 {
			t.Fatal("buffer charge leaked", mode, s.thBuffered.Load())
		}
	}
	s.thBufferLimit = 8
	r := httptest.NewRequest("POST", "/", strings.NewReader("123456789"))
	_, _, err := s.readTHBody(httptest.NewRecorder(), r, 64)
	if !errors.Is(err, errTHBufferLimit) || s.thBuffered.Load() != 0 {
		t.Fatal("shared budget not enforced before allocation")
	}
}
func TestTHBufferRejectsBeforeUpstream(t *testing.T) {
	e := newTHEnv(t)
	e.s.thBufferLimit = 8
	status, body, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[]}`, nil)
	if status != 429 || !strings.Contains(body, "gateway_buffer_limit") || e.count() != 0 || e.s.thBuffered.Load() != 0 {
		t.Fatal("budget refusal forwarded or leaked", status, body)
	}
}
