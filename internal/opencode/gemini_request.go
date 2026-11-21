package opencode

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// Gemini is used by the Factory bridge only, not by OpenCode discovery or its
// endpoint allowlist. The bridge does not accept Gemini as a downstream format.
const Gemini Protocol = "generate"

// ConvertGeminiRequest reuses the strict Chat/Responses/Messages decoder. The
// resulting body is a GenerateContentRequest, independent of downstream SSE.
func ConvertGeminiRequest(from Protocol, body []byte) (wire []byte, stream, chatUsage bool, err error) {
	var m object
	if decodeJSON(body, &m) != nil || m == nil {
		return nil, false, false, errors.New("invalid compatibility request JSON")
	}
	if from == Responses {
		if m["store"] == false {
			delete(m, "store") // Gemini conversion never stores a response for continuation.
		}
		if text := obj(m["text"]); len(text) == 1 {
			if format := obj(text["format"]); len(format) == 1 && format["type"] == "text" {
				delete(m, "text") // Plain text is the default; schema/verbosity settings are not.
			}
		}
		normalizeGeminiResponsesHistory(m)
	}
	if (from == Chat || from == Responses) && m["parallel_tool_calls"] == true {
		delete(m, "parallel_tool_calls") // Gemini may also return multiple calls.
	}
	if from == Chat {
		if m["n"] == json.Number("1") {
			delete(m, "n") // Gemini defaults to a single candidate.
		}
		if options := obj(m["stream_options"]); options != nil && options["include_usage"] != nil {
			var ok bool
			chatUsage, ok = options["include_usage"].(bool)
			if !ok || m["stream"] != true {
				return nil, false, false, unsupported("stream_options.include_usage without boolean stream options")
			}
		}
	}
	r, err := decodeRequest(from, m)
	if err != nil {
		return nil, false, false, err
	}
	if from == Messages && r.max == nil {
		return nil, false, false, unsupported("Messages requires explicit max_tokens")
	}
	out, err := encodeGeminiRequest(r)
	if err != nil {
		return nil, false, false, err
	}
	wire, err = json.Marshal(out)
	return wire, r.stream, chatUsage, err
}

// Accept inline replay of completed Responses output items, not output item
// references. Keep call_id, arguments, tool output and content untouched.
func normalizeGeminiResponsesHistory(m object) {
	for _, raw := range arr(m["input"]) {
		item := obj(raw)
		switch item["type"] {
		case "message", "function_call", "function_call_output":
			if item["status"] != nil && item["status"] != "completed" {
				continue // The strict decoder rejects non-completed history.
			}
			if _, ok := item["id"].(string); ok {
				delete(item, "id") // An inline item's label, not its call_id.
			}
			delete(item, "status")
		}
		for _, raw := range arr(item["content"]) {
			block := obj(raw)
			if annotations := arr(block["annotations"]); block["type"] == "output_text" && annotations != nil && len(annotations) == 0 {
				delete(block, "annotations")
			}
		}
	}
}

func encodeGeminiRequest(r request) (object, error) {
	out := object{"model": r.model}
	var contents, system []any
	var systemRole string
	pending := map[string]string{}
	seenCalls := map[string]bool{}
	for _, msg := range r.messages {
		if msg.role == "system" || msg.role == "developer" {
			// Gemini has only one instruction channel. A leading developer-only
			// prompt is representable, but never collapse different priorities.
			if len(contents) > 0 || systemRole != "" && systemRole != msg.role {
				return nil, unsupported("interleaved instructions or mixed system/developer roles")
			}
			systemRole = msg.role
			for _, p := range msg.parts {
				if p.kind != "text" {
					return nil, unsupported("non-text system instruction")
				}
				system = append(system, object{"text": p.text})
			}
			continue
		}
		if len(contents) == 0 && msg.role != "user" {
			return nil, unsupported("Gemini history must start with user content")
		}
		role := "user"
		if msg.role == "assistant" {
			role = "model"
			if len(pending) > 0 && obj(contents[len(contents)-1])["role"] != "model" {
				return nil, unsupported("assistant turn before pending tool results")
			}
		}
		var parts []any
		for _, p := range msg.parts {
			switch p.kind {
			case "text":
				if msg.role == "tool" || len(pending) > 0 && role != "model" {
					return nil, unsupported("text before pending tool results")
				}
				parts = append(parts, object{"text": p.text})
			case "image":
				if msg.role != "user" || len(pending) > 0 {
					return nil, unsupported("image outside a user content turn")
				}
				data, err := geminiImage(p.url)
				if err != nil {
					return nil, err
				}
				parts = append(parts, object{"inlineData": data})
			case "call":
				var args object
				if seenCalls[p.id] || decodeJSON([]byte(p.args), &args) != nil || args == nil {
					return nil, unsupported("duplicate tool ID or non-object function arguments")
				}
				seenCalls[p.id], pending[p.id] = true, p.name
				parts = append(parts, object{"functionCall": object{"id": p.id, "name": p.name, "args": args}})
			case "result":
				name := pending[p.id]
				if role != "user" || name == "" {
					return nil, unsupported("tool result without matching call")
				}
				delete(pending, p.id)
				parts = append(parts, object{"functionResponse": object{"id": p.id, "name": name, "response": object{"output": p.text}}})
			default:
				return nil, unsupported("Gemini request content")
			}
		}
		if len(parts) == 0 {
			return nil, unsupported("empty Gemini turn")
		}
		if n := len(contents); n > 0 && obj(contents[n-1])["role"] == role {
			last := obj(contents[n-1])
			last["parts"] = append(arr(last["parts"]), parts...)
		} else {
			contents = append(contents, object{"role": role, "parts": parts})
		}
	}
	if len(contents) == 0 || obj(contents[len(contents)-1])["role"] != "user" || len(pending) > 0 {
		return nil, unsupported("Gemini history must end with user content or complete tool results")
	}
	out["contents"] = contents
	if len(system) > 0 {
		out["systemInstruction"] = object{"parts": system}
	}

	generation := object{}
	if r.max != nil {
		maxTokens, ok := geminiInt(r.max)
		if !ok || maxTokens <= 0 || maxTokens > 1<<31-1 {
			return nil, unsupported("max output tokens")
		}
		generation["maxOutputTokens"] = r.max
	}
	for key, value := range (object{"temperature": r.temperature, "topP": r.topP}) {
		if value != nil {
			n, ok := value.(json.Number)
			f, err := n.Float64()
			if !ok || err != nil || f < 0 || key == "topP" && f > 1 || key == "temperature" && f > 2 {
				return nil, unsupported(key)
			}
			generation[key] = value
		}
	}
	if r.stop != nil {
		sequences := arr(r.stop)
		if s, ok := r.stop.(string); ok {
			sequences = []any{s}
		}
		if sequences == nil || len(sequences) > 5 {
			return nil, unsupported("Gemini stop sequences")
		}
		for _, v := range sequences {
			if str(v) == "" {
				return nil, unsupported("empty or non-text stop sequence")
			}
		}
		generation["stopSequences"] = sequences
	}
	if len(generation) > 0 {
		out["generationConfig"] = generation
	}
	if err := encodeGeminiTools(out, r); err != nil {
		return nil, err
	}
	return out, nil
}

func encodeGeminiTools(out object, r request) error {
	var declarations []any
	names := map[string]bool{}
	for _, f := range r.tools {
		if f.strict != nil && f.strict != false || obj(f.schema) == nil || names[f.name] {
			return unsupported("strict, duplicate or non-object Gemini tool declaration")
		}
		names[f.name] = true
		declarations = append(declarations, object{"name": f.name, "description": f.description, "parameters": f.schema})
	}
	if len(declarations) > 0 {
		out["tools"] = []any{object{"functionDeclarations": declarations}}
	}
	if r.choice == "" {
		return nil
	}
	choice := object{"mode": "AUTO"}
	switch r.choice {
	case "none":
		choice["mode"] = "NONE"
	case "required", "named":
		if len(names) == 0 || r.choice == "named" && !names[r.named] {
			return unsupported("tool_choice without matching Gemini declaration")
		}
		choice["mode"] = "ANY"
		if r.choice == "named" {
			choice["allowedFunctionNames"] = []string{r.named}
		}
	}
	if len(names) > 0 {
		out["toolConfig"] = object{"functionCallingConfig": choice}
	}
	return nil
}

func geminiImage(url string) (object, error) {
	mime, data, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ";base64,")
	if !strings.HasPrefix(url, "data:") || !ok || data == "" {
		return nil, unsupported("Gemini requires inline base64 images")
	}
	switch mime {
	case "image/png", "image/jpeg", "image/webp", "image/heic", "image/heif":
	default:
		return nil, unsupported("Gemini image MIME type")
	}
	if _, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(data))); err != nil {
		return nil, unsupported("invalid image base64")
	}
	return object{"mimeType": mime, "data": data}, nil
}
