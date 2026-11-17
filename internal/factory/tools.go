// Package factory implements Factory wire-format adaptations without changing
// caller identity or prompt text. Tool-schema relocation follows the MIT
// magpie-community Factory plugin; see THIRD_PARTY_NOTICES.md.
package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
)

type object = map[string]any

func obj(v any) object  { m, _ := v.(map[string]any); return m }
func list(v any) []any  { a, _ := v.([]any); return a }
func text(v any) string { s, _ := v.(string); return s }
func decode(b []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("Factory: invalid JSON document")
	}
	return nil
}

// ToolNames belongs to one request only. Never infer wrapping from a returned
// argument named "arguments": that may be an ordinary caller-owned field.
type ToolNames map[string]bool

// WrapTools moves root schema compositions into a required argument envelope,
// relocates local document pointers and wraps previous calls/examples as well.
// Non-composed schemas and non-tool content are returned unchanged.
func WrapTools(body []byte) ([]byte, ToolNames, error) {
	var request object
	if err := decode(body, &request); err != nil || request == nil {
		return nil, nil, errors.New("Factory: invalid tool request JSON")
	}
	names := ToolNames{}
	if _, ok := request["tools"].([]any); !ok {
		return body, names, nil
	}
	if _, ok := request["messages"].([]any); !ok {
		return body, names, nil
	}
	seenNames := map[string]bool{}
	for _, raw := range list(request["tools"]) {
		t := obj(raw)
		name := text(t["name"])
		if name != "" && seenNames[name] {
			return nil, nil, errors.New("Factory: duplicate tool name")
		}
		if name != "" {
			seenNames[name] = true
		}
		schema := obj(t["input_schema"])
		if text(t["name"]) == "" || schema == nil {
			continue
		}
		composed := false
		for _, key := range []string{"anyOf", "oneOf", "allOf"} {
			if _, ok := schema[key]; ok {
				composed = true
			}
		}
		if !composed {
			continue
		}
		if err := rebaseSchema(schema, true, strings.Contains(text(schema["$schema"]), "draft-03") || strings.Contains(text(schema["$schema"]), "draft-04")); err != nil {
			return nil, nil, err
		}
		wrapped := object{"type": "object", "properties": object{"arguments": schema}, "required": []string{"arguments"}, "additionalProperties": false}
		if dialect, exists := schema["$schema"]; exists {
			wrapped["$schema"] = dialect
		}
		t["input_schema"] = wrapped
		if examples, ok := t["input_examples"].([]any); ok {
			for i, v := range examples {
				examples[i] = object{"arguments": v}
			}
		}
		names[text(t["name"])] = true
	}
	if len(names) == 0 {
		return body, names, nil
	}
	for _, raw := range list(request["messages"]) {
		m := obj(raw)
		if m["role"] != "assistant" {
			continue
		}
		for _, raw := range list(m["content"]) {
			b := obj(raw)
			if b["type"] == "tool_use" && names[text(b["name"])] {
				if input, exists := b["input"]; exists {
					b["input"] = object{"arguments": input}
				}
			}
		}
	}
	out, err := json.Marshal(request)
	return out, names, err
}

func rebaseSchema(schema object, rebase, legacy bool) error {
	if schema == nil {
		return nil
	}
	id, exists := schema["$id"]
	if !exists && legacy {
		id = schema["id"]
	}
	if s, ok := id.(string); ok && strings.SplitN(s, "#", 2)[0] != "" {
		rebase = false
	}
	if rebase {
		_, recursive := schema["$recursiveRef"]
		if schema["$recursiveAnchor"] == true || recursive {
			return errors.New("Factory: a recursive tool schema needs $id before it can be wrapped")
		}
		for _, key := range []string{"$ref", "$dynamicRef"} {
			ref := text(schema[key])
			if !strings.HasPrefix(ref, "#") {
				continue
			}
			pointer, err := url.PathUnescape(ref[1:])
			if err != nil {
				return errors.New("Factory: invalid tool schema reference")
			}
			if pointer == "" || strings.HasPrefix(pointer, "/") {
				schema[key] = "#/properties/arguments" + ref[1:]
			}
		}
	}
	// Only schema positions are visited; data in const/default/examples is not.
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies"} {
		for _, child := range obj(schema[key]) {
			if err := rebaseSchema(obj(child), rebase, legacy); err != nil {
				return err
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems", "additionalProperties", "unevaluatedProperties", "propertyNames", "contains", "items", "additionalItems", "unevaluatedItems", "not", "if", "then", "else", "contentSchema"} {
		if a, ok := schema[key].([]any); ok {
			for _, child := range a {
				if err := rebaseSchema(obj(child), rebase, legacy); err != nil {
					return err
				}
			}
		} else if err := rebaseSchema(obj(schema[key]), rebase, legacy); err != nil {
			return err
		}
	}
	return nil
}

func unwrap(input any) (any, error) {
	m := obj(input)
	if len(m) != 1 {
		return nil, errors.New("Factory: invalid wrapped tool arguments")
	}
	v, exists := m["arguments"]
	if !exists {
		return nil, errors.New("Factory: invalid wrapped tool arguments")
	}
	return v, nil
}

func UnwrapReply(body []byte, names ToolNames) ([]byte, error) {
	if len(names) == 0 {
		return body, nil
	}
	var message object
	if err := decode(body, &message); err != nil || message == nil {
		return nil, errors.New("Factory: invalid tool reply JSON")
	}
	changed := false
	for _, raw := range list(message["content"]) {
		b := obj(raw)
		if b["type"] != "tool_use" || !names[text(b["name"])] {
			continue
		}
		input, err := unwrap(b["input"])
		if err != nil {
			return nil, err
		}
		b["input"] = input
		changed = true
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(message)
}
