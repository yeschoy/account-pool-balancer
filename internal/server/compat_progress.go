package server

import (
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// progressBody tracks whether a buffered upstream read is making progress.
// Protocol conversion has to buffer the whole reply, so without this a gateway
// that accepts the call and then stalls holds the request until the overall
// timeout — minutes of waiting for an answer that will never arrive.
type progressBody struct {
	rc     io.ReadCloser
	saw    atomic.Bool
	lastMs atomic.Int64
	once   sync.Once
}

func newProgressBody(rc io.ReadCloser) *progressBody {
	p := &progressBody{rc: rc}
	p.lastMs.Store(time.Now().UnixMilli())
	return p
}

func (p *progressBody) Read(b []byte) (int, error) {
	n, err := p.rc.Read(b)
	if n > 0 {
		p.saw.Store(true)
		p.lastMs.Store(time.Now().UnixMilli())
	}
	return n, err
}

func (p *progressBody) Close() error {
	var err error
	p.once.Do(func() { err = p.rc.Close() })
	return err
}

// guardCompatibilityRead watches a buffered read and calls onStall when the
// first byte does not arrive within firstByte, or the stream goes quiet for
// idle. The returned stop function ends the watch.
func guardCompatibilityRead(body io.ReadCloser, firstByte, idle time.Duration, onStall func()) (*progressBody, func()) {
	p := newProgressBody(body)
	done := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(done) }) }
	if firstByte <= 0 {
		firstByte = 120 * time.Second
	}
	if idle <= 0 {
		idle = 360 * time.Second
	}
	start := time.Now()
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if !p.saw.Load() {
					if time.Since(start) > firstByte {
						onStall()
						return
					}
					continue
				}
				last := time.UnixMilli(p.lastMs.Load())
				if time.Since(last) > idle {
					onStall()
					return
				}
			}
		}
	}()
	return p, stop
}
