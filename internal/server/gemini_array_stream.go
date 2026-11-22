package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"account-pool-balancer/internal/config"
)

// relayGeminiArray serves a :streamGenerateContent caller that did not ask for
// alt=sse. Google answers that call with one JSON array, and buffering the whole
// answer to build it would hide every chunk until the end. The array is opened
// immediately and each upstream chunk is appended as it arrives, so the bytes
// the client reads are the same JSON Array<GenerateContentResponse> while the
// delivery stays incremental.
func (s *Server) relayGeminiArray(w http.ResponseWriter, r *http.Request, cfg *config.Config, resp *http.Response, res *attemptResult, cancel func(), timedOut *atomic.Bool, attemptNo int, start time.Time) *attemptResult {
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var data bytes.Buffer
	readEvent := func() ([]byte, bool, error) {
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				if data.Len() == 0 {
					continue
				}
				payload := bytes.TrimSpace(data.Bytes())
				data.Reset()
				return payload, true, nil
			}
			if bytes.HasPrefix(line, []byte("data:")) {
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.Write(bytes.TrimPrefix(line[len("data:"):], []byte(" ")))
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, false, err
		}
		if data.Len() > 0 {
			payload := bytes.TrimSpace(data.Bytes())
			data.Reset()
			return payload, true, nil
		}
		return nil, false, io.EOF
	}

	first, ok, err := readEvent()
	if err != nil && err != io.EOF {
		res.kind = outCancelled
		if r.Context().Err() != nil {
			return res
		}
		res.kind, res.status, res.terminal, res.category = outFailed, 502, "network", "network"
		return res
	}
	if timedOut.Load() {
		res.kind, res.status, res.terminal, res.category = outFailed, 504, "timeout", "timeout"
		return res
	}
	if !ok {
		res.kind, res.status, res.terminal, res.category = outFailed, 502, "network", "network"
		return res
	}
	if payloadErr, st := geminiArrayEnvelope(first); payloadErr {
		res.kind, res.status, res.body, res.contentType = outFailed, st, first, "application/json"
		res.terminal, res.category = "upstream_envelope", "upstream"
		return res
	}

	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Attempts", strconv.Itoa(attemptNo))
	setUpstreamRequestID(h, resp.Header, "")
	rc := http.NewResponseController(w)
	write := func(b []byte) bool {
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	w.WriteHeader(http.StatusOK)
	if !write(append(append([]byte("["), first...), '\n')) {
		res.kind = outCancelled
		return res
	}
	ttft := time.Since(start).Milliseconds()
	res.ttft = &ttft
	res.streamEvents++
	s.scanGeminiArrayChunk(first, res)

	terminal := geminiTerminal(first)
	idle := time.NewTimer(time.Duration(cfg.Routing.IdleSec) * time.Second)
	defer idle.Stop()
	for !terminal {
		type readResult struct {
			payload []byte
			ok      bool
			err     error
		}
		ch := make(chan readResult, 1)
		go func() {
			p, ok, err := readEvent()
			ch <- readResult{p, ok, err}
		}()
		var payload []byte
		var got bool
		select {
		case rr := <-ch:
			if rr.err != nil && rr.err != io.EOF {
				timedOut.Store(true)
				cancel()
				_ = write([]byte("]\n"))
				res.result, res.terminal, res.category = "failed", "network", "network"
				return res
			}
			payload, got = rr.payload, rr.ok
		case <-idle.C:
			timedOut.Store(true)
			cancel()
			_ = write([]byte("]\n"))
			res.result, res.terminal, res.category = "failed", "timeout", "timeout"
			return res
		case <-r.Context().Done():
			cancel()
			res.kind = outCancelled
			return res
		}
		if !got {
			break
		}
		if len(payload) == 0 {
			continue
		}
		idle.Reset(time.Duration(cfg.Routing.IdleSec) * time.Second)
		if !write(append(append([]byte(","), payload...), '\n')) {
			res.kind = outCancelled
			return res
		}
		res.streamEvents++
		s.scanGeminiArrayChunk(payload, res)
		terminal = geminiTerminal(payload)
	}
	if !write([]byte("]\n")) {
		res.kind = outCancelled
		return res
	}
	if terminal {
		res.kind, res.status, res.result, res.terminal = outDelivered, http.StatusOK, "success", "success"
		return res
	}
	// The array is closed so the client can parse what it has, but the upstream
	// never sent a finish reason: the answer is incomplete, not a success.
	res.kind, res.status, res.result, res.terminal, res.category = outDelivered, http.StatusOK, "failed", "incomplete_stream", "incomplete_stream"
	return res
}

// geminiArrayEnvelope reports whether a chunk is an upstream error envelope and
// which status it deserves.
func geminiArrayEnvelope(payload []byte) (bool, int) {
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return false, 0
	}
	if m["error"] != nil || m["type"] == "error" {
		return true, 502
	}
	return false, 0
}

// scanGeminiArrayChunk records usage and restores the caller's tool names in a
// chunk that is about to be written.
func (s *Server) scanGeminiArrayChunk(payload []byte, res *attemptResult) {
	if !bytes.Contains(payload, []byte(`"usageMetadata"`)) && !bytes.Contains(payload, []byte(`"functionCall"`)) {
		return
	}
	var m map[string]any
	if json.Unmarshal(payload, &m) != nil {
		return
	}
	scanUsage(m, &res.usage)
}
