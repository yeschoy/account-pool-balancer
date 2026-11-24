package server

import "bytes"

// Only exact DeepSeek Chat calls this compatibility boundary. Strip the
// standard Anthropic performance hint and the observed user-text 1h variant;
// neither sets a Mistral cache TTL. Unknown hints and meaningful assistant
// annotations stay native. Complete a known empty slot without deleting it.
func mistralDeepSeekTextChunks(message *mistralWireObject, assistant bool) error {
	content := message.get("content")
	if len(content) == 0 || content[0] != '[' {
		return nil
	}
	if !assistant && !bytes.Contains(content, []byte(`"cache_control"`)) && !bytes.Contains(content, []byte(`\u`)) {
		return nil
	}
	if err := message.unique("content"); err != nil {
		return err
	}
	var out bytes.Buffer
	changed, last := false, 0
	for i := skipSpace(content, 1); content[i] != ']'; {
		end := skipValue(content, i)
		chunk := scanMistralObject(content[i:end])
		if chunk != nil && mistralString(chunk.get("type")) == "text" {
			if err := chunk.unique("type", "text", "cache_control"); err != nil {
				return err
			}
			removedHint := false
			if hint := scanMistralObject(chunk.get("cache_control")); hint != nil && mistralString(hint.get("type")) == "ephemeral" {
				if err := hint.unique("type"); err != nil {
					return err
				}
				consume := len(hint.fields) == 1
				if !consume && !assistant {
					if err := hint.unique("ttl"); err != nil {
						return err
					}
					consume = len(hint.fields) == 2 && mistralString(hint.get("ttl")) == "1h"
				}
				if consume {
					chunk.set("cache_control", nil)
					removedHint = true
				}
			}
			fields := len(chunk.fields)
			if removedHint {
				fields--
			}
			if assistant {
				if err := chunk.unique("annotations"); err != nil {
					return err
				}
				annotations := chunk.get("annotations")
				if bytes.Equal(annotations, []byte("null")) || (len(annotations) >= 2 && annotations[0] == '[' && skipSpace(annotations, 1) == len(annotations)-1) {
					// Exact-model evidence: even annotations:[] is forbidden on a
					// native text chunk. Never discard real citations or metadata.
					chunk.set("annotations", nil)
					fields--
				}
			}
			if assistant && fields == 1 && chunk.get("text") == nil {
				chunk.set("text", []byte(`""`))
			}
			prepared := chunk.render()
			if !bytes.Equal(prepared, content[i:end]) {
				out.Write(content[last:i])
				out.Write(prepared)
				last, changed = end, true
			}
		}
		i = skipSpace(content, end)
		if content[i] == ',' {
			i = skipSpace(content, i+1)
		}
	}
	if changed {
		out.Write(content[last:])
		message.set("content", out.Bytes())
	}
	return nil
}
