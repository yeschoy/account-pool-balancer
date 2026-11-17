package factory

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

var ErrToolAdaptation = errors.New("Factory tool adaptation failed")

const maxToolBytes = 4 << 20
const maxFrameBytes = 8 << 20

type toolBlock struct {
	initial      any
	initialBytes int
	json         strings.Builder
}
type toolReader struct {
	source        io.ReadCloser
	scanner       *bufio.Scanner
	names         ToolNames
	blocks        map[string]*toolBlock
	seen          map[string]bool
	pending       []byte
	err           error
	argumentBytes int
}

// UnwrapStream only buffers arguments of composed-schema tools, not the whole
// answer. Text/reasoning, ordinary tools, ids, event metadata and usage stream
// through unchanged. Closing the adapter closes the original upstream body.
func UnwrapStream(source io.ReadCloser, names ToolNames) io.ReadCloser {
	if len(names) == 0 {
		return source
	}
	s := bufio.NewScanner(source)
	s.Buffer(make([]byte, 64<<10), maxFrameBytes)
	s.Split(splitFrame)
	return &toolReader{source: source, scanner: s, names: names, blocks: map[string]*toolBlock{}, seen: map[string]bool{}}
}
func (r *toolReader) Close() error { return r.source.Close() }
func (r *toolReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 && r.err == nil {
		if !r.scanner.Scan() {
			r.err = r.scanner.Err()
			if r.err == nil && len(r.blocks) > 0 {
				r.err = errors.New("incomplete wrapped tool arguments")
			}
			if r.err == nil {
				r.err = io.EOF
			} else {
				r.err = errors.Join(ErrToolAdaptation, r.err)
			}
			break
		}
		out, err := r.frame(append([]byte(nil), r.scanner.Bytes()...))
		if err != nil {
			r.err = errors.Join(ErrToolAdaptation, err)
			break
		}
		r.pending = out
	}
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	return 0, r.err
}

func frameBoundary(b []byte) (int, int) {
	best, length := -1, 0
	for _, sep := range []string{"\r\n\r\n", "\r\n\n", "\n\r\n", "\n\n", "\r\r"} {
		if i := bytes.Index(b, []byte(sep)); i >= 0 && (best < 0 || i < best) {
			best, length = i, len(sep)
		}
	}
	return best, length
}
func splitFrame(data []byte, eof bool) (int, []byte, error) {
	if i, n := frameBoundary(data); i >= 0 {
		return i + n, data[:i+n], nil
	}
	if eof && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
func (r *toolReader) frame(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8 in tool stream")
	}
	// The shared SSE relay consumes LF/CRLF lines. Canonicalize the legal
	// CR-only dialect before returning frames, including unchanged events.
	if bytes.Contains(raw, []byte("\r")) && !bytes.Contains(raw, []byte("\n")) {
		raw = bytes.ReplaceAll(raw, []byte("\r"), []byte("\n"))
	}
	end := []byte{}
	if i, n := frameBoundary(raw); i >= 0 {
		end, raw = raw[i:i+n], raw[:i]
	}
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n"), "\n")
	data := []string{}
	for _, line := range lines {
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	original := func() []byte { return append(raw, end...) }
	if len(data) == 0 {
		return original(), nil
	}
	payload := strings.Join(data, "\n")
	if payload == "[DONE]" {
		if len(r.blocks) > 0 {
			return nil, errors.New("incomplete wrapped tool arguments")
		}
		return original(), nil
	}
	var event object
	if decode([]byte(payload), &event) != nil || event == nil {
		return nil, errors.New("invalid tool event JSON")
	}
	replace := func() ([]byte, error) {
		encoded, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		out := []string{}
		written := false
		for _, line := range lines {
			if !strings.HasPrefix(line, "data:") {
				out = append(out, line)
			} else if !written {
				out = append(out, "data: "+string(encoded))
				written = true
			}
		}
		eol := "\n"
		if bytes.Contains(raw, []byte("\r\n")) {
			eol = "\r\n"
		}
		return append([]byte(strings.Join(out, eol)), end...), nil
	}
	index := ""
	if n, ok := event["index"].(json.Number); ok {
		if i, err := n.Int64(); err == nil && i >= 0 {
			index = n.String()
		}
	}
	typ := text(event["type"])
	if typ == "content_block_start" {
		b := obj(event["content_block"])
		if b["type"] == "tool_use" && r.names[text(b["name"])] {
			if index == "" || r.seen[index] || len(r.seen) >= 1024 {
				return nil, errors.New("invalid or duplicate wrapped tool block")
			}
			initial, err := json.Marshal(b["input"])
			if err != nil || len(initial) > maxToolBytes || r.argumentBytes+len(initial) > 16<<20 {
				return nil, errors.New("wrapped tool arguments exceed buffer limit")
			}
			r.argumentBytes += len(initial)
			r.seen[index] = true
			r.blocks[index] = &toolBlock{initial: b["input"], initialBytes: len(initial)}
			b["input"] = object{}
			return replace()
		}
	}
	if b := r.blocks[index]; b != nil {
		switch typ {
		case "content_block_delta":
			d := obj(event["delta"])
			if d["type"] != "input_json_delta" {
				return nil, errors.New("invalid wrapped tool delta")
			}
			s, ok := d["partial_json"].(string)
			if !ok {
				return nil, errors.New("invalid tool argument delta")
			}
			if b.json.Len()+len(s) > maxToolBytes || r.argumentBytes+len(s) > 16<<20 {
				return nil, errors.New("wrapped tool arguments exceed buffer limit")
			}
			b.json.WriteString(s)
			r.argumentBytes += len(s)
			return nil, nil
		case "content_block_stop":
			input := b.initial
			if b.json.Len() > 0 {
				if decode([]byte(b.json.String()), &input) != nil {
					return nil, errors.New("invalid wrapped tool argument JSON")
				}
			}
			v, err := unwrap(input)
			if err != nil {
				return nil, err
			}
			arg, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			r.argumentBytes -= b.json.Len() + b.initialBytes
			delete(r.blocks, index)
			delta, _ := json.Marshal(object{"type": "content_block_delta", "index": event["index"], "delta": object{"type": "input_json_delta", "partial_json": string(arg)}})
			return append(append([]byte("event: content_block_delta\ndata: "), append(delta, []byte("\n\n")...)...), original()...), nil
		}
	}
	if typ == "error" {
		clear(r.blocks)
		r.argumentBytes = 0
	}
	if typ == "message_stop" && len(r.blocks) > 0 {
		return nil, errors.New("incomplete wrapped tool arguments")
	}
	return original(), nil
}
