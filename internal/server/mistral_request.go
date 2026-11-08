package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// mistralWireObject is a view over already validated JSON. Replacement values
// are raw JSON; nil removes a field. Never decode unrelated numbers or collapse
// duplicate keys through a map. Each retained field owns its preceding comma,
// so consecutive removals cannot overlap (including at the end of an object).
type mistralWireField struct {
	key            string
	start, vs, end int
	value          []byte
	changed        bool
}

type mistralWireObject struct {
	body      []byte
	fields    []mistralWireField
	additions [][]byte
	end       int
}

func scanMistralObject(body []byte) *mistralWireObject {
	open := skipSpace(body, 0)
	if open >= len(body) || body[open] != '{' {
		return nil
	}
	o := &mistralWireObject{body: body}
	i := skipSpace(body, open+1)
	for body[i] != '}' {
		ke := skipString(body, i)
		var key string
		_ = json.Unmarshal(body[i:ke], &key)
		vs := skipSpace(body, skipSpace(body, ke)+1)
		ve := skipValue(body, vs)
		o.fields = append(o.fields, mistralWireField{key: key, start: i, vs: vs, end: ve, value: body[vs:ve]})
		i = skipSpace(body, ve)
		if body[i] == ',' {
			i = skipSpace(body, i+1)
		}
	}
	o.end = i
	return o
}

func (o *mistralWireObject) get(key string) []byte {
	for i := len(o.fields) - 1; i >= 0; i-- {
		if o.fields[i].key == key {
			return o.fields[i].value
		}
	}
	return nil
}

func (o *mistralWireObject) unique(keys ...string) error {
	for _, key := range keys {
		seen := false
		for _, f := range o.fields {
			if f.key == key {
				if seen {
					return fmt.Errorf("Mistral Chat duplicate %s is ambiguous; provide it once", key)
				}
				seen = true
			}
		}
	}
	return nil
}

func (o *mistralWireObject) set(key string, value []byte) {
	found := false
	for i := range o.fields {
		if o.fields[i].key == key {
			o.fields[i].value, o.fields[i].changed = value, true
			found = true
		}
	}
	if !found && value != nil {
		name, _ := json.Marshal(key)
		o.additions = append(o.additions, append(append(name, ':'), value...))
	}
}

func (o *mistralWireObject) render() []byte {
	changed := len(o.additions) != 0
	for _, f := range o.fields {
		changed = changed || f.changed
	}
	if !changed {
		return o.body
	}
	var out bytes.Buffer
	prefix, tail := o.end, o.end
	if len(o.fields) > 0 {
		prefix, tail = o.fields[0].start, o.fields[len(o.fields)-1].end
	}
	out.Write(o.body[:prefix])
	kept := false
	for i, f := range o.fields {
		if f.changed && f.value == nil {
			continue
		}
		if kept {
			// The separator before this field contains no other field bytes.
			out.Write(o.body[o.fields[i-1].end:f.start])
		}
		out.Write(o.body[f.start:f.vs])
		out.Write(f.value)
		kept = true
	}
	for _, field := range o.additions {
		if kept {
			out.WriteByte(',')
		}
		out.Write(field)
		kept = true
	}
	out.Write(o.body[tail:])
	return out.Bytes()
}

// prepareMistralRequest runs after strict JSON/model validation and original
// identity selection, but before account/RPM acquisition. Only the exact
// DeepSeek Chat model consumes generic parameters; other paths remain native.
func prepareMistralRequest(body []byte, path, fingerprint string) ([]byte, string, error) {
	if path != "/v1/chat/completions" && path != "/v1/responses" {
		return body, "", nil
	}
	o := scanMistralObject(body)
	deepSeek := path == "/v1/chat/completions" && mistralString(o.get("model")) == "deepseek-v4-1-flash-1017"
	if deepSeek {
		if err := adaptMistralDeepSeekParameters(o); err != nil {
			return nil, "", err
		}
	}
	if path == "/v1/chat/completions" {
		for i := range o.fields {
			if o.fields[i].key != "messages" {
				continue
			}
			messages, changed, err := mistralChatMessages(o.fields[i].value, deepSeek)
			if err != nil {
				return nil, "", err
			}
			if changed {
				o.fields[i].value, o.fields[i].changed = messages, true
			}
		}
	}
	generated := ""
	if o.get("prompt_cache_key") == nil && fingerprint != "" {
		generated = "apb-mistral-" + fingerprint
		key, _ := json.Marshal(generated)
		o.set("prompt_cache_key", key)
	}
	return o.render(), generated, nil
}

func mistralString(raw []byte) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func adaptMistralDeepSeekParameters(o *mistralWireObject) error {
	// Keep the existing stateless error contract, including duplicate store.
	if err := o.unique("store"); err != nil {
		return errors.New("Mistral Chat is stateless: duplicate store is not supported")
	}
	if store := o.get("store"); store != nil {
		if !bytes.Equal(store, []byte("false")) && !bytes.Equal(store, []byte("null")) {
			return errors.New("Mistral Chat is stateless: omit store or use false/null; store=true is not supported")
		}
		o.set("store", nil)
	}
	if err := o.unique("messages", "thinking", "enable_thinking", "reasoning_effort", "reasoning", "seed", "random_seed", "logit_bias", "verbosity", "user", "safety_identifier", "prompt_cache_retention", "prompt_cache_key", "metadata"); err != nil {
		return err
	}
	if raw := o.get("thinking"); raw != nil {
		thinking := scanMistralObject(raw)
		if thinking == nil || len(thinking.fields) != 1 || thinking.fields[0].key != "type" {
			return errors.New("Mistral Chat thinking requires only type=enabled/disabled; budgets and unknown structures cannot be preserved")
		}
		kind := mistralString(thinking.get("type"))
		if kind != "enabled" && kind != "disabled" {
			return errors.New("Mistral Chat thinking.type must be enabled or disabled")
		}
		o.set("thinking", nil)
	}
	if raw := o.get("enable_thinking"); raw != nil {
		if !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
			return errors.New("Mistral Chat enable_thinking must be boolean")
		}
		o.set("enable_thinking", nil)
	}
	nativeEffort := o.get("reasoning_effort")
	effortRaw := mistralDeepSeekEffort(nativeEffort)
	if raw := o.get("reasoning"); raw != nil {
		reasoning := scanMistralObject(raw)
		if reasoning != nil {
			if err := reasoning.unique("effort"); err != nil {
				return err
			}
		}
		if reasoning == nil || len(reasoning.fields) != 1 || reasoning.fields[0].key != "effort" {
			return errors.New("Mistral DeepSeek Chat reasoning requires only effort; unknown structures and additional fields cannot be preserved")
		}
		alias := mistralDeepSeekEffort(reasoning.get("effort"))
		if nativeEffort != nil && mistralString(effortRaw) != mistralString(alias) {
			return errors.New("Mistral DeepSeek Chat reasoning.effort conflicts with explicit reasoning_effort")
		}
		if nativeEffort == nil {
			effortRaw = alias
		}
		o.set("reasoning", nil)
	}
	// The operator's supported tiers are low/high. Missing or unsupported
	// effort always becomes high; compatibility switches cannot emit none.
	// Write once: a missing native field is appended, not updated in-place.
	if !bytes.Equal(effortRaw, nativeEffort) {
		o.set("reasoning_effort", effortRaw)
	}
	if seed := o.get("seed"); seed != nil {
		if !bytes.Equal(seed, []byte("null")) {
			n, valid := mistralSeed(seed)
			if !valid {
				return errors.New("Mistral Chat seed must be an integer or null; reproducibility cannot be silently discarded")
			}
			if native := o.get("random_seed"); native != nil {
				other, ok := mistralSeed(native)
				if !ok || n.Cmp(other) != 0 {
					return errors.New("Mistral Chat seed conflicts with explicit random_seed; neither value was overwritten")
				}
			} else {
				o.set("random_seed", seed)
			}
		}
		o.set("seed", nil)
	}
	if bias := o.get("logit_bias"); bias != nil {
		empty := bytes.Equal(bias, []byte("null"))
		if obj := scanMistralObject(bias); obj != nil && len(obj.fields) == 0 {
			empty = true
		}
		if !empty {
			return errors.New("Mistral DeepSeek Chat does not support nonempty logit_bias; requested token bias was not discarded")
		}
		o.set("logit_bias", nil)
	}
	if v := o.get("verbosity"); v != nil {
		if !bytes.Equal(v, []byte("null")) && mistralString(v) != "medium" {
			return errors.New("Mistral DeepSeek Chat does not support explicit verbosity; only null/default medium may be omitted")
		}
		o.set("verbosity", nil)
	}
	for _, key := range []string{"user", "safety_identifier"} {
		if v := o.get(key); v != nil {
			if !bytes.Equal(v, []byte("null")) && v[0] != '"' {
				return fmt.Errorf("Mistral Chat %s must be a string or null; it is only a compatibility label, not an upstream tracking/safety feature", key)
			}
			o.set(key, nil)
		}
	}
	if v := o.get("prompt_cache_retention"); v != nil {
		if !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte(`""`)) {
			return errors.New("Mistral DeepSeek Chat cannot guarantee prompt_cache_retention; explicit retention is not supported")
		}
		o.set("prompt_cache_retention", nil)
	}
	return nil
}

func mistralDeepSeekEffort(raw []byte) []byte {
	switch mistralString(raw) {
	case "low", "high":
		return raw
	default:
		return []byte(`"high"`)
	}
}

func mistralSeed(raw []byte) (*big.Int, bool) {
	// JSON was validated already. Do not round through float64 or accept a
	// fractional/exponential representation that would require rewriting it.
	if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) || bytes.ContainsAny(raw, ".eE") {
		return nil, false
	}
	return new(big.Int).SetString(string(raw), 10)
}

func mistralChatMessages(raw []byte, developer bool) ([]byte, bool, error) {
	if len(raw) == 0 || raw[0] != '[' {
		return raw, false, nil // native validation owns unrelated malformed messages
	}
	var out bytes.Buffer
	changed, last := false, 0
	for i := skipSpace(raw, 1); raw[i] != ']'; {
		end := skipValue(raw, i)
		message := scanMistralObject(raw[i:end])
		if message != nil {
			role := mistralString(message.get("role"))
			if developer || message.get("reasoning_content") != nil {
				if err := message.unique("role"); err != nil {
					return nil, false, err
				}
			}
			if developer && role == "developer" {
				message.set("role", []byte(`"system"`))
			}
			if developer && role == "user" {
				if err := mistralUserImages(message); err != nil {
					return nil, false, err
				}
			}
			if role == "assistant" {
				reasoning := message.get("reasoning_content")
				// The exact DeepSeek Chat endpoint also rejects the string
				// reasoning alias used by some clients. Preserve its thought
				// through the same native chunks, never by simply deleting it.
				if alias := message.get("reasoning"); developer && alias != nil {
					if err := message.unique("reasoning", "reasoning_content", "content"); err != nil {
						return nil, false, err
					}
					var aliased, canonical *string
					if json.Unmarshal(alias, &aliased) != nil {
						return nil, false, errors.New("Mistral DeepSeek assistant reasoning must be a string or null")
					}
					if reasoning != nil && json.Unmarshal(reasoning, &canonical) != nil {
						return nil, false, errors.New("Mistral assistant reasoning_content must be a string or null")
					}
					if aliased != nil && *aliased != "" && canonical != nil && *canonical != "" && *aliased != *canonical {
						return nil, false, errors.New("Mistral DeepSeek assistant reasoning conflicts with reasoning_content; neither thought was discarded")
					}
					if reasoning == nil || canonical == nil || *canonical == "" {
						reasoning = alias
					}
					message.set("reasoning", nil)
				}
				if reasoning != nil {
					if err := message.unique("content", "reasoning_content"); err != nil {
						return nil, false, err
					}
					if err := mistralAssistantReasoning(message, reasoning); err != nil {
						return nil, false, err
					}
				}
			}
			prepared := message.render()
			if !bytes.Equal(prepared, raw[i:end]) {
				out.Write(raw[last:i])
				out.Write(prepared)
				last, changed = end, true
			}
		}
		i = skipSpace(raw, end)
		if raw[i] == ',' {
			i = skipSpace(raw, i+1)
		}
	}
	if !changed {
		return raw, false, nil
	}
	out.Write(raw[last:])
	return out.Bytes(), true, nil
}

// Mistral's native vision form accepts a string URL. Remove only empty
// NewAPI MimeType metadata, then unwrap URL-only objects; retain real options.
func mistralUserImages(message *mistralWireObject) error {
	content := message.get("content")
	if len(content) == 0 || content[0] != '[' {
		return nil
	}
	if err := message.unique("content"); err != nil {
		return err
	}
	var chunks []json.RawMessage
	_ = json.Unmarshal(content, &chunks)
	changed := false
	for i, raw := range chunks {
		chunk := scanMistralObject(raw)
		if chunk == nil || mistralString(chunk.get("type")) != "image_url" {
			continue
		}
		if err := chunk.unique("type", "image_url"); err != nil {
			return err
		}
		image := scanMistralObject(chunk.get("image_url"))
		if image == nil {
			continue // already native or unrelated malformed input: retain upstream validation
		}
		if err := image.unique("url", "MimeType"); err != nil {
			return err
		}
		fields := len(image.fields)
		mime := image.get("MimeType")
		if bytes.Equal(mime, []byte(`""`)) || bytes.Equal(mime, []byte("null")) {
			// NewAPI may leak this empty internal field; Mistral rejects it.
			image.set("MimeType", nil)
			fields--
			chunk.set("image_url", image.render())
			chunks[i], changed = chunk.render(), true
		}
		if fields != 1 || image.get("url") == nil {
			continue // never drop detail or any other requested option
		}
		url := image.get("url")
		if len(url) == 0 || url[0] != '"' {
			return errors.New("Mistral DeepSeek user image_url.url must be a string")
		}
		chunk.set("image_url", url)
		chunks[i], changed = chunk.render(), true
	}
	if changed {
		content, _ = json.Marshal(chunks)
		message.set("content", content)
	}
	return nil
}

func mistralAssistantReasoning(message *mistralWireObject, reasoning []byte) error {
	var text *string
	if json.Unmarshal(reasoning, &text) != nil {
		return errors.New("Mistral assistant reasoning_content must be a string or null")
	}
	if text != nil && *text != "" {
		thinking, _ := json.Marshal(map[string]any{
			"type":     "thinking",
			"thinking": []map[string]string{{"type": "text", "text": *text}},
		})
		chunks := []json.RawMessage{thinking}
		content := message.get("content")
		switch {
		case len(content) == 0 || bytes.Equal(content, []byte("null")):
		case content[0] == '"':
			chunk, _ := json.Marshal(map[string]string{"type": "text", "text": mistralString(content)})
			chunks = append(chunks, chunk)
		case content[0] == '[':
			var existing []json.RawMessage
			_ = json.Unmarshal(content, &existing)
			chunks = append(chunks, existing...)
		default:
			return errors.New("Mistral assistant content must be text, chunks or null to preserve reasoning")
		}
		content, _ = json.Marshal(chunks)
		message.set("content", content)
	}
	message.set("reasoning_content", nil)
	return nil
}
