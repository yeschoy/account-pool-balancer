package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

var errMistralContent = errors.New("Mistral Chat content cannot be represented as text and reasoning_content without loss")

// mistralChatResponse adapts only native Chat content lists. RawMessage preserves
// tool arguments, token counts and unknown fields; ordinary string chunks retain
// their original wire bytes. Responses/FIM and other providers never call it.
func mistralChatResponse(body []byte, stream bool) ([]byte, error) {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return body, nil // errors and non-JSON events retain their native handling
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(root["choices"], &choices) != nil {
		return body, nil
	}
	field := "message"
	if stream {
		field = "delta"
	}
	changed := false
	for _, choice := range choices {
		var message map[string]json.RawMessage
		if json.Unmarshal(choice[field], &message) != nil {
			continue
		}
		content := bytes.TrimSpace(message["content"])
		if len(content) == 0 || content[0] != '[' {
			continue
		}
		var chunks []map[string]json.RawMessage
		if json.Unmarshal(content, &chunks) != nil {
			return nil, errMistralContent
		}
		var answer, reasoning strings.Builder
		hasThinking := false
		for _, chunk := range chunks {
			var typ string
			_ = json.Unmarshal(chunk["type"], &typ)
			switch typ {
			case "text":
				text, err := mistralTextChunk(chunk)
				if err != nil {
					return nil, err
				}
				answer.WriteString(text)
			case "thinking":
				// A signed/reference block needs native replay semantics. Do not
				// silently strip it to make an incompatible client appear healthy.
				for key, value := range chunk {
					switch key {
					case "type", "thinking", "closed":
					case "signature":
						if !bytes.Equal(bytes.TrimSpace(value), []byte("null")) && !bytes.Equal(bytes.TrimSpace(value), []byte(`""`)) {
							return nil, errMistralContent
						}
					default:
						return nil, errMistralContent
					}
				}
				var thoughts []map[string]json.RawMessage
				if json.Unmarshal(chunk["thinking"], &thoughts) != nil || thoughts == nil {
					return nil, errMistralContent
				}
				for _, thought := range thoughts {
					text, err := mistralTextChunk(thought)
					if err != nil {
						return nil, err
					}
					reasoning.WriteString(text)
				}
				hasThinking = true
			default:
				return nil, errMistralContent
			}
		}
		if hasThinking {
			var existing string
			if raw := message["reasoning_content"]; len(raw) > 0 && json.Unmarshal(raw, &existing) != nil {
				return nil, errMistralContent
			}
			message["reasoning_content"], _ = json.Marshal(existing + reasoning.String())
		}
		message["content"], _ = json.Marshal(answer.String())
		choice[field], _ = json.Marshal(message)
		changed = true
	}
	if !changed {
		return body, nil
	}
	root["choices"], _ = json.Marshal(choices)
	return json.Marshal(root)
}

func mistralTextChunk(chunk map[string]json.RawMessage) (string, error) {
	var typ, text string
	if json.Unmarshal(chunk["type"], &typ) != nil || typ != "text" || json.Unmarshal(chunk["text"], &text) != nil || bytes.Equal(bytes.TrimSpace(chunk["text"]), []byte("null")) {
		return "", errMistralContent
	}
	for key := range chunk {
		if key != "type" && key != "text" {
			return "", errMistralContent
		}
	}
	return text, nil
}

// Normalize a single complete event, never the full stream. Keep SSE metadata,
// comments, delimiters and terminal events; replace only the data lines.
func mistralChatEvent(event []byte) ([]byte, error) {
	_, data, _ := sseFields(event)
	normalized, err := mistralChatResponse(data, true)
	if err != nil || bytes.Equal(data, normalized) {
		return event, err
	}
	var out bytes.Buffer
	written := false
	for _, line := range bytes.SplitAfter(event, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("data:")) {
			out.Write(line)
			continue
		}
		if written {
			continue
		}
		out.WriteString("data: ")
		out.Write(normalized)
		switch {
		case bytes.HasSuffix(line, []byte("\r\n")):
			out.WriteString("\r\n")
		case bytes.HasSuffix(line, []byte("\n")):
			out.WriteByte('\n')
		}
		written = true
	}
	return out.Bytes(), nil
}

func mistralResponseFailure(res *attemptResult) *attemptResult {
	res.kind, res.status, res.terminal, res.category = outFailed, http.StatusBadGateway, "compatibility", "compatibility"
	res.contentType = "application/json"
	res.body, _ = json.Marshal(map[string]any{"error": map[string]string{"type": "api_error", "code": "mistral_content_unsupported", "message": errMistralContent.Error()}})
	return res
}
