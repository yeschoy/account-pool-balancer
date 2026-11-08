package server

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// Responses SDKs may put the event name only in data.type. Do not mistake
// output text mentioning a terminal type, or Chat's [DONE], for completion.
func mistralResponsesTerminal(event string, data []byte) bool {
	if event != "response.completed" && event != "response.incomplete" &&
		!bytes.Contains(data, []byte("response.completed")) && !bytes.Contains(data, []byte("response.incomplete")) && !bytes.Contains(data, []byte(`\u`)) {
		return false
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return false
	}
	if !json.Valid(data) {
		return false
	}
	frame := scanMistralObject(data)
	if frame.unique("type") != nil {
		return false
	}
	typ := event
	if raw := frame.get("type"); raw != nil {
		typ = ""
		if json.Unmarshal(raw, &typ) != nil || typ == "" {
			return false
		}
	}
	if event != "" && event != "message" && event != typ {
		return false
	}
	return typ == "response.completed" || typ == "response.incomplete"
}

func mistralResponsesTruncated(res *attemptResult) *attemptResult {
	res.kind, res.result, res.status = outFailed, "failed", http.StatusBadGateway
	res.terminal, res.category, res.contentType = "incomplete_stream", "network", "application/json"
	res.body = []byte(`{"type":"error","error":{"type":"api_error","code":"mistral_response_incomplete","message":"Mistral Responses stream ended before a terminal event; the request was not replayed"}}`)
	return res
}
