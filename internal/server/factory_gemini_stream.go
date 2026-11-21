package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"account-pool-balancer/internal/opencode"
)

// geminiToChatStream relays a Gemini SSE stream to a Chat client as the frames
// arrive. The buffered compatibility path has to wait for the whole answer, so
// its first token equals the total duration; this path keeps the upstream's
// real time-to-first-token.
func (s *Server) geminiToChatStream(w http.ResponseWriter, r *http.Request, resp *http.Response, res *attemptResult, attempt int, start time.Time) *attemptResult {
	rec := res.goRecord
	fail := func(err error, wroteAny bool) *attemptResult {
		if r.Context().Err() != nil {
			res.kind = outCancelled
			return res
		}
		res.kind, res.status, res.terminal, res.category = outFailed, 502, "compatibility", "compatibility"
		res.contentType = "application/json"
		msg, _ := json.Marshal(map[string]string{"type": "api_error", "code": "upstream_compatibility_error", "message": err.Error()})
		if wroteAny {
			// Headers and chunks are already on the wire; report the failure in
			// the stream the client is reading.
			_, _ = w.Write([]byte("data: " + string(msg) + "\n\n"))
			_ = http.NewResponseController(w).Flush()
			res.body = nil
			return res
		}
		res.body = msg
		return res
	}

	stream := opencode.NewGeminiChatStream(opencode.ChatID(rec.RequestID), rec.Model, time.Now().Unix(), rec.factoryPlan.chatUsage, rec.factoryPlan.toolAliases)
	h := w.Header()
	h.Set("X-Compatibility-Mode", "stream")
	h.Set("X-Attempts", strconv.Itoa(attempt))
	setUpstreamRequestID(h, resp.Header, "")
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")

	var wrote bool
	flusher := http.NewResponseController(w)
	write := func(b []byte) error {
		if len(b) == 0 {
			return nil
		}
		if !wrote {
			wrote = true
			w.WriteHeader(200)
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		return flusher.Flush()
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	var data bytes.Buffer
	for scanner.Scan() {
		if r.Context().Err() != nil {
			res.kind = outCancelled
			return res
		}
		line := scanner.Bytes()
		switch {
		case len(line) == 0:
			if data.Len() == 0 {
				continue
			}
			payload := bytes.TrimSpace(data.Bytes())
			data.Reset()
			if bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			out, finished, err := stream.FeedEvent(payload)
			if err != nil {
				var streamErr *opencode.StreamError
				if errors.As(err, &streamErr) && streamErr.BeforeContent && !wrote {
					res.kind, res.status, res.terminal, res.category = outFailed, 502, "upstream_envelope", "upstream"
					res.body, res.contentType = streamErr.Payload, "application/json"
					return res
				}
				return fail(err, wrote)
			}
			if err := write(out); err != nil {
				res.kind = outCancelled
				return res
			}
			if finished {
				break
			}
		case bytes.HasPrefix(line, []byte("data:")):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(bytes.TrimPrefix(line[len("data:"):], []byte(" ")))
		}
		if stream.Started() && res.ttft == nil {
			ms := time.Since(start).Milliseconds()
			res.ttft = &ms
		}
	}
	if err := scanner.Err(); err != nil {
		return fail(errors.New("Gemini compatibility stream read failed or event exceeds 2 MiB"), wrote)
	}
	if !stream.Started() {
		return fail(errors.New("Gemini compatibility response missing finishReason and produced no content"), wrote)
	}
	if err := write(stream.Finish()); err != nil {
		res.kind = outCancelled
		return res
	}
	in, out, cached, reasoning, known := stream.Usage()
	if known {
		res.usage = usage{in: &in, out: &out}
		res.usage.cached = &cached
		res.usage.reasoning = &reasoning
	}
	res.kind, res.status, res.result, res.terminal = outDelivered, 200, "success", "success"
	return res
}

var _ = strings.HasPrefix
