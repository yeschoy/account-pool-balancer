package searchgateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type roundResult struct {
	response object
	output   []json.RawMessage
	status   string
	usage    json.RawMessage
}

func (g *Gateway) round(r *http.Request, route Route, p plan, re *roundEmitter) (roundResult, error) {
	resp, err := g.request(r, route, "/v1/responses", marshal(p.body))
	if err != nil {
		return roundResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return roundResult{}, upstream("upstream_http_error")
	}
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		b, err := io.ReadAll(io.LimitReader(resp.Body, g.cfg.MaxResponseBytes+1))
		if err != nil || int64(len(b)) > g.cfg.MaxResponseBytes {
			return roundResult{}, upstream("upstream_response_limit")
		}
		o, err := decodeObject(b)
		if err != nil {
			return roundResult{}, upstream("invalid_upstream_response")
		}
		return parseRound(o)
	}
	reader := bufio.NewReader(io.LimitReader(resp.Body, g.cfg.MaxResponseBytes+1))
	for {
		event, data, err := readEvent(reader, 1<<20)
		if err != nil {
			return roundResult{}, upstream("upstream_response_incomplete")
		}
		if len(data) == 0 {
			continue
		}
		o, err := decodeObject(data)
		if err != nil {
			return roundResult{}, upstream("invalid_upstream_event")
		}
		typ := str(o["type"])
		if typ == "" {
			typ = event
		}
		if event != "" && event != "message" && event != typ {
			return roundResult{}, upstream("conflicting_upstream_event")
		}
		if typ == "response.completed" || typ == "response.incomplete" {
			terminal, err := decodeObject(o["response"])
			if err != nil {
				return roundResult{}, upstream("invalid_upstream_terminal")
			}
			result, err := parseRound(terminal)
			if err != nil {
				return result, err
			}
			if (typ == "response.incomplete") != (result.status == "incomplete") {
				return result, upstream("conflicting_upstream_terminal")
			}
			return result, nil
		}
		re.e.begin()
		if err = re.frame(typ, o); err != nil {
			return roundResult{}, err
		}
	}
}
func parseRound(o object) (roundResult, error) {
	result := roundResult{response: o, status: str(o["status"]), usage: o["usage"]}
	if result.status != "completed" && result.status != "incomplete" {
		return result, upstream("upstream_response_incomplete")
	}
	a, err := array(o["output"])
	if err != nil {
		return result, upstream("invalid_upstream_output")
	}
	if len(a) > 4096 {
		return result, upstream("upstream_output_limit")
	}
	for _, raw := range a {
		if _, err := decodeObject(raw); err != nil {
			return result, upstream("invalid_output_item")
		}
	}
	result.output = a
	return result, nil
}
func readEvent(r *bufio.Reader, max int) (string, []byte, error) {
	var data bytes.Buffer
	var name string
	size := 0
	for {
		var line []byte
		for {
			part, err := r.ReadSlice('\n')
			size += len(part)
			if size > max {
				return "", nil, io.ErrShortBuffer
			}
			line = append(line, part...)
			if err == bufio.ErrBufferFull {
				continue
			}
			if err != nil {
				return "", nil, err
			}
			break
		}
		line = bytes.TrimSuffix(bytes.TrimSuffix(line, []byte("\n")), []byte("\r"))
		if len(line) == 0 {
			return name, bytes.TrimSuffix(data.Bytes(), []byte("\n")), nil
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			name = strings.TrimSpace(string(line[6:]))
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			v := line[5:]
			if len(v) > 0 && v[0] == ' ' {
				v = v[1:]
			}
			data.Write(v)
			data.WriteByte('\n')
		}
	}
}
