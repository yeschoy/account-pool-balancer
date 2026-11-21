package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

// Agent tool schemas carry draft keywords Google refuses; only the accepted
// ones may reach the gateway.
func TestSanitizeGeminiTools(t *testing.T) {
	body := []byte(`{"tools":[{"functionDeclarations":[{"name":"read","description":"d","parameters":` +
		`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","additionalProperties":false,` +
		`"properties":{"path":{"type":"string","description":"p","$comment":"x",` +
		`"items":{"type":"string","additionalProperties":true}}},` +
		`"required":["path"],"strict":false}}]}]}`)
	out, changed := SanitizeGeminiTools(body)
	if !changed {
		t.Fatal("nothing sanitized")
	}
	for _, bad := range []string{"$schema", "additionalProperties", "strict", "$comment"} {
		if strings.Contains(string(out), bad) {
			t.Fatalf("%s kept: %s", bad, out)
		}
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		t.Fatal("not JSON")
	}
	params := m["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "object" || params["required"] == nil {
		t.Fatalf("accepted keys lost: %v", params)
	}
	if _, ok := params["properties"].(map[string]any)["path"]; !ok {
		t.Fatal("properties lost")
	}
	// A body with accepted keys only is untouched.
	clean := []byte(`{"tools":[{"functionDeclarations":[{"name":"x","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}]}]}`)
	if got, ch := SanitizeGeminiTools(clean); ch || string(got) != string(clean) {
		t.Fatalf("clean body changed: %s", got)
	}
}

// A nullable type union becomes one type plus nullable; Google refuses a list.
func TestSanitizeGeminiTypeUnion(t *testing.T) {
	body := []byte(`{"tools":[{"functionDeclarations":[{"name":"x","parameters":{"type":"object","properties":{` +
		`"a":{"type":["string","null"]},"b":{"type":["null","object","array"]},"c":{"type":["null"]}}}}]}]}`)
	out, changed := SanitizeGeminiTools(body)
	if !changed {
		t.Fatal("nothing sanitized")
	}
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		t.Fatal("not JSON")
	}
	props := m["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)
	a := props["a"].(map[string]any)
	if a["type"] != "string" || a["nullable"] != true {
		t.Fatalf("a = %v", a)
	}
	if b := props["b"].(map[string]any); b["type"] != "object" {
		t.Fatalf("b = %v", b)
	}
	// A nullable-only union has no member left, so the type is inferred rather
	// than dropped: Google refuses a schema without one.
	if typ := props["c"].(map[string]any)["type"]; typ != "string" {
		t.Fatalf("c = %v", props["c"])
	}
}

// A schema carrying items must declare the ARRAY type.
func TestSanitizeGeminiItemsImpliesArray(t *testing.T) {
	body, err := json.Marshal(map[string]any{"tools": []any{map[string]any{"functionDeclarations": []any{
		map[string]any{"name": "x", "parameters": map[string]any{"type": "object", "properties": map[string]any{
			"a": map[string]any{"type": []any{"boolean", "array"}, "items": map[string]any{"type": "string"}},
			"b": map[string]any{"items": map[string]any{"type": "string"}},
		}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	out, changed := SanitizeGeminiTools(body)
	if !changed {
		t.Fatalf("nothing sanitized: %s", body)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	props := m["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)
	for _, key := range []string{"a", "b"} {
		if typ := props[key].(map[string]any)["type"]; typ != "array" {
			t.Fatalf("%s type = %v", key, typ)
		}
	}
}

// A composition must be the schema's only field, and oneOf travels as anyOf.
func TestSanitizeGeminiComposition(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"tools": []any{map[string]any{"functionDeclarations": []any{
		map[string]any{"name": "x", "parameters": map[string]any{"type": "object", "properties": map[string]any{
			"a": map[string]any{"type": "object", "description": "d", "anyOf": []any{map[string]any{"type": "string"}}},
			"b": map[string]any{"type": "object", "oneOf": []any{map[string]any{"type": "number"}}},
		}}},
	}}}})
	out, changed := SanitizeGeminiTools(body)
	if !changed {
		t.Fatal("nothing sanitized")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	props := m["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)
	a := props["a"].(map[string]any)
	if len(a) != 1 || a["anyOf"] == nil {
		t.Fatalf("a = %v", a)
	}
	b := props["b"].(map[string]any)
	if len(b) != 1 || b["anyOf"] == nil {
		t.Fatalf("b = %v", b)
	}
}

// Google rejects an enum member whose type differs from the field's: an
// integer in a string enum is 400 "(TYPE_STRING)". Members are coerced or
// dropped so the declaration is accepted.
func TestSanitizeGeminiEnumTypes(t *testing.T) {
	body, err := json.Marshal(map[string]any{"tools": []any{map[string]any{"functionDeclarations": []any{
		map[string]any{"name": "x", "parameters": map[string]any{"type": "object", "properties": map[string]any{
			"a": map[string]any{"type": "string", "enum": []any{float64(1), "two", true, nil}},
			"b": map[string]any{"type": "integer", "enum": []any{float64(1), "2", "x"}},
			"c": map[string]any{"enum": []any{float64(1), float64(2)}},
		}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	out, changed := SanitizeGeminiTools(body)
	if !changed {
		t.Fatal("nothing normalized")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	props := m["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)["parameters"].(map[string]any)["properties"].(map[string]any)
	a := props["a"].(map[string]any)
	if enum := a["enum"].([]any); len(enum) != 3 || enum[0] != "1" || enum[1] != "two" || enum[2] != "true" {
		t.Fatalf("string enum = %v", enum)
	}
	b := props["b"].(map[string]any)
	if enum := b["enum"].([]any); len(enum) != 3 || enum[0] != "1" || enum[1] != "2" || enum[2] != "x" {
		t.Fatalf("integer enum = %v", enum)
	}
	c := props["c"].(map[string]any)
	if enum, ok := c["enum"].([]any); !ok || len(enum) != 2 || enum[0] != "1" || c["type"] != "number" {
		t.Fatalf("inferred enum = %v type=%v", enum, c["type"])
	}
}

// Google requires a declaration's parameters root to be an object schema; a
// composition-only or scalar root is refused with "should be of type OBJECT".
func TestSanitizeGeminiParametersAreObject(t *testing.T) {
	body, err := json.Marshal(map[string]any{"tools": []any{map[string]any{"functionDeclarations": []any{
		map[string]any{"name": "a", "parameters": map[string]any{"anyOf": []any{map[string]any{"type": "string"}}}},
		map[string]any{"name": "b", "parameters": map[string]any{"type": "string"}},
		map[string]any{"name": "c", "parameters": map[string]any{"properties": map[string]any{"x": map[string]any{"type": "string"}}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	out, changed := SanitizeGeminiTools(body)
	if !changed {
		t.Fatal("nothing enforced")
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	decls := m["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)
	for _, raw := range decls {
		d := raw.(map[string]any)
		params := d["parameters"].(map[string]any)
		if params["type"] != "object" {
			t.Fatalf("%s parameters type = %v", d["name"], params["type"])
		}
		if _, ok := params["anyOf"]; ok {
			t.Fatalf("%s kept a composition at the root", d["name"])
		}
	}
}

// An enum on an array or object schema is refused by Google; it is dropped.
func TestSanitizeGeminiEnumOnNonScalar(t *testing.T) {
	body, err := json.Marshal(map[string]any{"tools": []any{map[string]any{"functionDeclarations": []any{
		map[string]any{"name": "x", "parameters": map[string]any{"type": "object", "properties": map[string]any{
			"list":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "enum": []any{"a", "b"}},
			"thing": map[string]any{"type": "object", "enum": []any{"a"}},
		}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	out, changed := SanitizeGeminiTools(body)
	if !changed {
		t.Fatal("nothing dropped")
	}
	if strings.Contains(string(out), `"enum"`) {
		t.Fatalf("enum kept on a non-scalar: %s", out)
	}
}
