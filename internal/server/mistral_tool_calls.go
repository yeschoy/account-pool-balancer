package server

import "bytes"

// mistralDeepSeekToolCalls consumes only the observed Gemini replay signature
// duplicated in two fields that native DeepSeek Chat rejects. Keep the tool
// call/result and any metadata not proved to be this exact foreign pair.
func mistralDeepSeekToolCalls(message *mistralWireObject) error {
	var calls []byte
	for _, f := range message.fields {
		if f.key == "tool_calls" && len(f.value) > 0 && f.value[0] == '[' &&
			(bytes.Contains(f.value, []byte(`"thoughtSignature"`)) || bytes.Contains(f.value, []byte(`\u`))) {
			if err := message.unique("tool_calls"); err != nil {
				return err
			}
			calls = f.value
			break
		}
	}
	if calls == nil {
		return nil
	}

	var out bytes.Buffer
	last := 0
	for i := skipSpace(calls, 1); calls[i] != ']'; {
		end := skipValue(calls, i)
		if call := scanMistralObject(calls[i:end]); call != nil {
			changed, err := omitMistralGeminiToolSignature(call)
			if err != nil {
				return err
			}
			if changed {
				if last == 0 {
					out.Grow(len(calls))
				}
				out.Write(calls[last:i])
				out.Write(call.render())
				last = end
			}
		}
		i = skipSpace(calls, end)
		if calls[i] == ',' {
			i = skipSpace(calls, i+1)
		}
	}
	if last != 0 {
		out.Write(calls[last:])
		message.set("tool_calls", out.Bytes())
	}
	return nil
}

func omitMistralGeminiToolSignature(call *mistralWireObject) (bool, error) {
	signature, extraRaw := call.get("signature"), call.get("extra_content")
	if signature == nil || extraRaw == nil {
		return false, nil
	}
	if err := call.unique("signature", "extra_content", "type"); err != nil {
		return false, err
	}
	if typ := call.get("type"); typ != nil && mistralString(typ) != "function" {
		return false, nil
	}
	extra := scanMistralObject(extraRaw)
	if extra == nil {
		return false, nil
	}
	if err := extra.unique("google"); err != nil {
		return false, err
	}
	google := scanMistralObject(extra.get("google"))
	if google == nil {
		return false, nil
	}
	if err := google.unique("thoughtSignature"); err != nil {
		return false, err
	}
	if len(extra.fields) != 1 || len(google.fields) != 1 {
		return false, nil
	}
	if sig := mistralString(signature); sig == "" || sig != mistralString(google.get("thoughtSignature")) {
		return false, nil
	}
	call.set("signature", nil)
	call.set("extra_content", nil)
	return true, nil
}
