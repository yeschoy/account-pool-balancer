package logstore

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLogQueueByteBudgetAndFlushRelease(t *testing.T) {
	lim := limits()
	lim.MaxQueuedBytes = 16
	st, err := Open(t.TempDir(), func() Limits { return lim })
	if err != nil {
		t.Fatal(err)
	}
	st.Append("requests", time.Now(), map[string]any{"requestId": "synthetic", "body": strings.Repeat("x", 200)})
	st.Close()
	if st.Dropped() != 1 || st.QueuedBytes() != 0 || st.QueueLength() != 0 {
		t.Fatal("byte budget not enforced/released")
	}
	lim.MaxQueuedBytes = 64 << 10
	st, err = Open(t.TempDir(), func() Limits { return lim })
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		st.Append("requests", time.Now(), map[string]any{"requestId": "synthetic", "body": "small"})
	}
	st.Close()
	if st.QueuedBytes() != 0 || st.QueueLength() != 0 {
		t.Fatal("drain leaked bytes")
	}
}
func TestConcurrentLogByteReservationsStayBounded(t *testing.T) {
	st := &Store{}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 500; n++ {
				if st.reserveQueue(7, 64) {
					if st.QueuedBytes() > 64 {
						t.Error("overspent queue byte budget")
					}
					st.queued.Add(-7)
				}
			}
		}()
	}
	wg.Wait()
	if st.QueuedBytes() != 0 {
		t.Fatal("reservation leak")
	}
}
