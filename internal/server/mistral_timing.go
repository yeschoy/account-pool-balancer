package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
)

// Durations and milestones use microseconds. A missing milestone is distinct
// from one that was observed in less than a microsecond. Only the numbers and
// fixed enums below reach request summaries; trace state owns no addresses.
type mistralRequestTiming struct {
	UploadUs    *int64 `json:"uploadUs,omitempty"`
	PrepareUs   *int64 `json:"prepareUs,omitempty"`
	AdmissionUs *int64 `json:"admissionUs,omitempty"`
	InputBytes  *int   `json:"inputBytes,omitempty"`
}

type mistralAttemptTimingLog struct {
	// HTTP start and successful downstream flush milestones are relative to
	// handler entry. Connection, upload complete and headers are relative to
	// HTTP start; ConnectionWaitUs is the last GetConn -> GotConn duration.
	HTTPStartUs        *int64 `json:"httpStartUs,omitempty"`
	ConnectionReadyUs  *int64 `json:"connectionReadyUs,omitempty"`
	ConnectionWaitUs   *int64 `json:"connectionWaitUs,omitempty"`
	ConnectionReused   *bool  `json:"connectionReused,omitempty"`
	RequestWrittenUs   *int64 `json:"requestWrittenUs,omitempty"`
	ResponseHeadersUs  *int64 `json:"responseHeadersUs,omitempty"`
	OutboundBytes      *int   `json:"outboundBytes,omitempty"`
	FirstEventReadUs   *int64 `json:"firstEventReadUs,omitempty"`
	FirstEventFlushUs  *int64 `json:"firstEventFlushUs,omitempty"`
	FirstThinkingUs    *int64 `json:"firstThinkingUs,omitempty"`
	FirstToolUs        *int64 `json:"firstToolUs,omitempty"`
	FirstTextUs        *int64 `json:"firstTextUs,omitempty"`
	LastEventReadUs    *int64 `json:"lastEventReadUs,omitempty"`
	LastEventFlushUs   *int64 `json:"lastEventFlushUs,omitempty"`
	TerminalFlushUs    *int64 `json:"terminalFlushUs,omitempty"`
	MaxWriteFlushUs    *int64 `json:"maxWriteFlushUs,omitempty"`
	ProtocolTerminal   string `json:"protocolTerminal,omitempty"`
	OutputProbeSkipped bool   `json:"outputProbeSkipped,omitempty"`
}

type mistralAttemptTiming struct {
	requestStart time.Time
	log          mistralAttemptTimingLog // handler-owned, never mutated by callbacks
	pending      mistralOutput

	mu                         sync.Mutex // trace callbacks can outlive Do
	called, getConn, gotConn   time.Time
	connWaitFrom, wroteRequest time.Time
	reused                     bool
}

func newMistralAttemptTiming(start time.Time, path string) *mistralAttemptTiming {
	t := &mistralAttemptTiming{requestStart: start}
	if path == "/v1/chat/completions" || path == "/v1/responses" {
		t.pending = mistralThinking | mistralTool | mistralText
	}
	return t
}

func (t *mistralAttemptTiming) attach(trace *httptrace.ClientTrace) {
	trace.GetConn = func(string) {
		at := time.Now()
		t.mu.Lock()
		t.getConn = at
		t.mu.Unlock()
	}
	trace.GotConn = func(info httptrace.GotConnInfo) {
		at := time.Now()
		t.mu.Lock()
		t.gotConn, t.connWaitFrom, t.reused = at, t.getConn, info.Reused
		t.wroteRequest = time.Time{} // transport may replace a stale connection
		t.mu.Unlock()
	}
	wrote := trace.WroteRequest
	trace.WroteRequest = func(info httptrace.WroteRequestInfo) {
		if info.Err == nil {
			at := time.Now()
			t.mu.Lock()
			t.wroteRequest = at
			t.mu.Unlock()
		}
		if wrote != nil {
			wrote(info) // preserves the existing sent/RPM/binding boundary
		}
	}
}

func (t *mistralAttemptTiming) begin(at time.Time, size int) {
	if t != nil {
		t.called = at // only the handler and its snapshot read this field
		t.log.HTTPStartUs = mistralElapsed(t.requestStart, at)
		t.log.OutboundBytes = &size
	}
}

func (t *mistralAttemptTiming) headers(at time.Time) {
	if t != nil {
		t.log.ResponseHeadersUs = mistralElapsed(t.called, at)
	}
}

func (t *mistralAttemptTiming) snapshot() *mistralAttemptTimingLog {
	if t == nil || t.called.IsZero() {
		return nil
	}
	out := t.log
	t.mu.Lock()
	out.ConnectionReadyUs = mistralElapsed(t.called, t.gotConn)
	out.ConnectionWaitUs = mistralElapsed(t.connWaitFrom, t.gotConn)
	if out.ConnectionReadyUs != nil {
		reused := t.reused
		out.ConnectionReused = &reused
	}
	out.RequestWrittenUs = mistralElapsed(t.called, t.wroteRequest)
	t.mu.Unlock()
	return &out
}

func mistralElapsed(start, end time.Time) *int64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil
	}
	us := end.Sub(start).Microseconds()
	return &us
}

const mistralProbeLimit = 64 << 10

type mistralOutput uint8

const (
	mistralThinking mistralOutput = 1 << iota
	mistralTool
	mistralText
)

func (t *mistralAttemptTiming) recordWrite(start, end time.Time) {
	us := end.Sub(start).Microseconds()
	if t.log.MaxWriteFlushUs == nil {
		t.log.MaxWriteFlushUs = &us
	} else if us > *t.log.MaxWriteFlushUs {
		*t.log.MaxWriteFlushUs = us
	}
}

// Called only after successful Write+Flush of an upstream event. Do not retain
// data or classify error, heartbeat or lifecycle frames as model output.
func (t *mistralAttemptTiming) flushed(at time.Time, event string, data, original, outgoing []byte, path string, responsesTerminal, isErr bool) {
	elapsed := at.Sub(t.requestStart).Microseconds()
	if t.log.FirstEventFlushUs == nil {
		first := elapsed
		t.log.FirstEventFlushUs = &first
	}
	if t.log.LastEventFlushUs == nil {
		t.log.LastEventFlushUs = new(int64)
	}
	*t.log.LastEventFlushUs = elapsed
	terminal := ""
	if responsesTerminal {
		terminal = "responses_terminal"
	} else if path != "/v1/responses" && bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		terminal = "done"
	}
	if terminal != "" && t.log.TerminalFlushUs == nil {
		t.log.ProtocolTerminal = terminal
		t.log.TerminalFlushUs = &elapsed
	}
	if isErr || terminal != "" || t.pending == 0 {
		return
	}
	adapted := len(original) != len(outgoing) || len(original) > 0 && &original[0] != &outgoing[0]
	if len(data) > mistralProbeLimit || adapted && len(outgoing) > mistralProbeLimit {
		t.log.OutputProbeSkipped = true
		t.pending = 0 // a later match can no longer establish a first
		return
	}
	if adapted {
		// Byte-changing Chat adaptation happened; inspect the emitted data,
		// rather than assuming the native thinking/content arrays reach clients.
		event, data, _ = sseFields(outgoing)
	}
	observed := mistralOutputKind(path, event, data, t.pending)
	if observed&mistralThinking != 0 {
		t.log.FirstThinkingUs = &elapsed
	}
	if observed&mistralTool != 0 {
		t.log.FirstToolUs = &elapsed
	}
	if observed&mistralText != 0 {
		t.log.FirstTextUs = &elapsed
	}
	t.pending &^= observed
}

func mistralOutputKind(path, event string, data []byte, pending mistralOutput) mistralOutput {
	nonblank := func(s string) bool { return strings.TrimSpace(s) != "" }
	if path == "/v1/responses" {
		// Named events or SDK data-only frames. Once a category is measured,
		// the hint avoids decoding its subsequent token frames.
		if !bytes.Contains(data, []byte(`\u`)) &&
			!(pending&mistralText != 0 && (event == "response.output_text.delta" || bytes.Contains(data, []byte("response.output_text.delta")))) &&
			!(pending&mistralThinking != 0 && (strings.HasPrefix(event, "response.reasoning_") || bytes.Contains(data, []byte("response.reasoning_")))) &&
			!(pending&mistralTool != 0 && (event == "response.output_item.added" || event == "response.function_call_arguments.delta" || bytes.Contains(data, []byte("response.output_item.added")) || bytes.Contains(data, []byte("response.function_call_arguments.delta")))) {
			return 0
		}
		var frame struct {
			Type  string                                 `json:"type"`
			Delta string                                 `json:"delta"`
			Item  struct{ Type, Name, Arguments string } `json:"item"`
		}
		if json.Unmarshal(data, &frame) != nil || (event != "" && event != "message" && frame.Type != "" && event != frame.Type) {
			return 0
		}
		if frame.Type == "" {
			frame.Type = event
		}
		switch frame.Type {
		case "response.output_text.delta":
			if nonblank(frame.Delta) {
				return pending & mistralText
			}
		case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
			if nonblank(frame.Delta) {
				return pending & mistralThinking
			}
		case "response.function_call_arguments.delta":
			if frame.Delta != "" {
				return pending & mistralTool
			}
		case "response.output_item.added":
			if frame.Item.Type == "function_call" && (frame.Item.Name != "" || frame.Item.Arguments != "") {
				return pending & mistralTool
			}
		}
		return 0
	}
	if path != "/v1/chat/completions" || (!bytes.Contains(data, []byte(`\u`)) &&
		!(pending&mistralText != 0 && bytes.Contains(data, []byte(`"content"`))) &&
		!(pending&mistralThinking != 0 && bytes.Contains(data, []byte(`"reasoning_content"`))) &&
		!(pending&mistralTool != 0 && (bytes.Contains(data, []byte(`"tool_calls"`)) || bytes.Contains(data, []byte(`"function_call"`))))) {
		return 0
	}
	type function struct{ Name, Arguments string }
	var frame struct {
		Choices []struct {
			Delta struct {
				Content      string   `json:"content"`
				Reasoning    string   `json:"reasoning_content"`
				FunctionCall function `json:"function_call"`
				ToolCalls    []struct {
					Function function `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &frame) != nil {
		return 0
	}
	var observed mistralOutput
	for _, choice := range frame.Choices {
		d := choice.Delta
		if nonblank(d.Content) {
			observed |= mistralText
		}
		if nonblank(d.Reasoning) {
			observed |= mistralThinking
		}
		if d.FunctionCall.Name != "" || d.FunctionCall.Arguments != "" {
			observed |= mistralTool
		}
		for _, tool := range d.ToolCalls {
			if tool.Function.Name != "" || tool.Function.Arguments != "" {
				observed |= mistralTool
				break
			}
		}
	}
	return observed & pending
}
