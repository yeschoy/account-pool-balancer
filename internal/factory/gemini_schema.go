package factory

import (
	"encoding/json"
	"math"
	"strconv"
)

// GeminiToolSchemaKeys are the JSON-Schema keys Google's generateContent
// parameters accept. Google's Schema message has no place for the draft
// keywords a JSON-Schema generator emits ($schema, additionalProperties,
// definitions, …), and a request carrying one is refused with 400 "Unknown
// name \"$schema\"". Real agent tool schemas carry them, so the Gemini route
// keeps only these keys.
var GeminiToolSchemaKeys = map[string]bool{
	"type": true, "format": true, "title": true, "description": true,
	"nullable": true, "default": true, "enum": true, "items": true,
	"minItems": true, "maxItems": true, "properties": true, "required": true,
	"anyOf": true, "oneOf": true, "allOf": true, "propertyOrdering": true,
	"minimum": true, "maximum": true, "minLength": true, "maxLength": true,
	"pattern": true, "example": true,
}

// SanitizeGeminiTools trims every functionDeclaration parameter schema to the
// keys Google accepts and reports whether anything changed. Non-schema fields
// of the declaration (name, description) are untouched.
func SanitizeGeminiTools(body []byte) ([]byte, bool) {
	var m object
	if decode(body, &m) != nil || m == nil {
		return body, false
	}
	tools, ok := m["tools"].([]any)
	if !ok {
		return body, false
	}
	changed := false
	for _, raw := range tools {
		tool := obj(raw)
		if tool == nil {
			continue
		}
		for _, draw := range list(tool["functionDeclarations"]) {
			decl := obj(draw)
			if decl == nil {
				continue
			}
			if schema, ok := decl["parameters"].(map[string]any); ok {
				if sanitizeGeminiSchema(schema) {
					changed = true
				}
				// Google requires a function declaration's parameters to be an
				// object schema: a composition-only or scalar root is refused
				// with "parameters schema should be of type OBJECT".
				if enforceGeminiObjectSchema(schema) {
					changed = true
				}
			}
		}
		// Some clients wrap the same declarations per function.
		if fn := obj(tool["function"]); fn != nil {
			if schema, ok := fn["parameters"].(map[string]any); ok {
				if sanitizeGeminiSchema(schema) {
					changed = true
				}
			}
		}
	}
	if !changed {
		return body, false
	}
	out, err := marshal(m)
	if err != nil {
		return body, false
	}
	return out, true
}

// sanitizeGeminiSchema drops unknown keys in place and recurses.
func sanitizeGeminiSchema(schema map[string]any) bool {
	// Google takes a composition as the whole schema: a schema carrying any_of
	// next to another field is refused ("When using any_of, it must be the
	// only field set"). oneOf/allOf are not part of Google's Schema, so they
	// travel as anyOf after their variants are trimmed.
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		variants, ok := schema[key].([]any)
		if !ok {
			continue
		}
		changed := false
		for _, raw := range variants {
			if sub, ok := raw.(map[string]any); ok && sanitizeGeminiSchema(sub) {
				changed = true
			}
		}
		for other := range schema {
			if other != key {
				delete(schema, other)
				changed = true
			}
		}
		if key != "anyOf" {
			delete(schema, key)
			schema["anyOf"] = variants
			changed = true
		}
		return changed
	}
	changed := false
	for key := range schema {
		if GeminiToolSchemaKeys[key] {
			continue
		}
		delete(schema, key)
		changed = true
	}
	for _, key := range []string{"properties"} {
		if props, ok := schema[key].(map[string]any); ok {
			for _, raw := range props {
				if sub, ok := raw.(map[string]any); ok && sanitizeGeminiSchema(sub) {
					changed = true
				}
			}
		}
	}
	// Draft JSON Schema allows a list of types ("type": ["string","null"]);
	// Google's Schema takes one enum value and refuses the list. A nullable
	// union becomes that type plus nullable, and any other union keeps its
	// first non-null member so the request still describes the argument.
	if types, ok := schema["type"].([]any); ok {
		var names []string
		nullable := false
		for _, raw := range types {
			name, _ := raw.(string)
			if name == "null" {
				nullable = true
				continue
			}
			if name != "" {
				names = append(names, name)
			}
		}
		switch {
		case len(names) == 1:
			schema["type"] = names[0]
			if nullable {
				schema["nullable"] = true
			}
		case len(names) > 1:
			schema["type"] = names[0]
			if nullable {
				schema["nullable"] = true
			}
		default:
			delete(schema, "type")
		}
		changed = true
	}
	// Draft JSON Schema allows a list of schemas for tuple validation; Google
	// takes one Schema, and a list there is refused ("Proto field is not
	// repeating, cannot start list").
	switch items := schema["items"].(type) {
	case map[string]any:
		if sanitizeGeminiSchema(items) {
			changed = true
		}
	case []any:
		if len(items) == 0 {
			delete(schema, "items")
			changed = true
		} else if first, ok := items[0].(map[string]any); ok {
			if sanitizeGeminiSchema(first) {
				changed = true
			}
			schema["items"] = first
			changed = true
		} else {
			delete(schema, "items")
			changed = true
		}
	}
	// Google requires a schema carrying items to declare the ARRAY type; a
	// union whose first member was another type is refused outright.
	if _, hasItems := schema["items"]; hasItems {
		if typ, _ := schema["type"].(string); typ != "array" {
			schema["type"] = "array"
			changed = true
		}
	}
	// Google refuses a schema without a type ("didn't specify the schema type
	// field"). A variant that only carries constraints is named after what
	// those constraints describe.
	if _, ok := schema["type"]; !ok {
		if _, composed := schema["anyOf"]; !composed {
			schema["type"] = inferGeminiType(schema)
			changed = true
		}
	}
	if normalizeGeminiEnum(schema) {
		changed = true
	}
	return changed
}

// enforceGeminiObjectSchema makes a function declaration's parameter root an
// object schema. Google checks the root type and refuses anything else, while a
// client may describe a single-argument tool with a composition or a scalar.
func enforceGeminiObjectSchema(schema map[string]any) bool {
	changed := false
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if _, ok := schema[key]; ok {
			delete(schema, key)
			changed = true
		}
	}
	if typ, _ := schema["type"].(string); typ != "object" {
		schema["type"] = "object"
		changed = true
	}
	return changed
}

// normalizeGeminiEnum makes every enum member match the declared type. Google
// answers a mismatched member with 400 "Invalid value at
// …parameters.properties[0].value.enum[0] (TYPE_STRING)": its Schema takes an
// enum of the field's own type, so an integer/bool member of a string enum —
// or null, which nullable already expresses — has to become a string or go.
func normalizeGeminiEnum(schema map[string]any) bool {
	members, ok := schema["enum"].([]any)
	if !ok || len(members) == 0 {
		return false
	}
	// Google's Schema declares enum as "repeated string": a numeric or boolean
	// member is refused with 400 "Invalid value at …enum[0] (TYPE_STRING)" even
	// when the field itself is integer or boolean. Measured against the gateway
	// on 2026-11-22: integer enum [1,2,3] is refused, ["1","2","3"] is accepted.
	changed := false
	out := make([]any, 0, len(members))
	for _, raw := range members {
		if text, ok := raw.(string); ok {
			out = append(out, text)
			continue
		}
		changed = true
		switch v := raw.(type) {
		case json.Number:
			out = append(out, v.String())
		case float64:
			out = append(out, formatEnumNumber(v))
		case bool:
			out = append(out, strconv.FormatBool(v))
		case nil:
			// null belongs to nullable, not to the enum
		default:
			// An object or array cannot be an enum member at all.
		}
	}
	if !changed {
		return false
	}
	if len(out) == 0 {
		delete(schema, "enum")
		return true
	}
	schema["enum"] = out
	return true
}

// formatEnumNumber prints a numeric enum member the way JSON would have, so an
// integer stays "3" rather than "3.000000".
func formatEnumNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func inferGeminiType(schema map[string]any) string {
	if _, ok := schema["properties"]; ok {
		return "object"
	}
	if _, ok := schema["items"]; ok {
		return "array"
	}
	for _, key := range []string{"pattern", "minLength", "maxLength", "minItems", "maxItems"} {
		if _, ok := schema[key]; ok {
			return "string"
		}
	}
	for _, key := range []string{"minimum", "maximum", "multipleOf"} {
		if _, ok := schema[key]; ok {
			return "number"
		}
	}
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		allNumbers := true
		for _, v := range enum {
			switch v.(type) {
			case json.Number, float64:
			default:
				allNumbers = false
			}
			if !allNumbers {
				break
			}
		}
		if allNumbers {
			return "number"
		}
	}
	return "string"
}
