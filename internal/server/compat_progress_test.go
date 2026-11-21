package server

import (
	"errors"
	"io"
	"testing"
	"time"
)

type stallReader struct{ closed chan struct{} }

func (s *stallReader) Read([]byte) (int, error) {
	<-s.closed
	return 0, io.EOF
}
func (s *stallReader) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

// A gateway that accepts and never sends a byte must be reported as stalled,
// because conversion cannot start before the first bytes arrive.
func TestGuardCompatibilityReadStalls(t *testing.T) {
	stalled := make(chan time.Time, 1)
	body := &stallReader{closed: make(chan struct{})}
	_, stop := guardCompatibilityRead(body, 20*time.Millisecond, time.Second, func() { stalled <- time.Now() })
	defer stop()
	select {
	case <-stalled:
	case <-time.After(2 * time.Second):
		t.Fatal("stall not reported")
	}
	body.Close()
}

// A reader that keeps producing bytes is not reported.
func TestGuardCompatibilityReadNoStall(t *testing.T) {
	body := io.NopCloser(&trickleReader{})
	stalled := make(chan struct{}, 1)
	_, stop := guardCompatibilityRead(body, 30*time.Millisecond, 60*time.Millisecond, func() { stalled <- struct{}{} })
	defer stop()
	time.Sleep(250 * time.Millisecond)
	select {
	case <-stalled:
		t.Fatal("progressing read reported as stalled")
	default:
	}
}

type trickleReader struct{ n int }

func (r *trickleReader) Read(b []byte) (int, error) {
	time.Sleep(10 * time.Millisecond)
	r.n++
	b[0] = 'x'
	return 1, errors.New("progress")
}
