package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

// Factory refuses a request that declares a tool named read, glob, grep or
// bash; the name is renamed to droid's own and the reply is renamed back.
func TestAliasToolNames(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4.1-flash-1017","messages":[` +
		`{"role":"user","content":"hi"},` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"c1","content":"x"}],` +
		`"tool_choice":{"type":"function","function":{"name":"read"}},` +
		`"tools":[{"type":"function","function":{"name":"read","description":"Read a file."}},` +
		`{"type":"function","function":{"name":"bash","description":"Run a command."}},` +
		`{"type":"function","function":{"name":"custom_tool","description":"kept"}}]}`)
	out, aliases := AliasToolNames(body)
	if len(aliases) != 2 {
		t.Fatalf("aliases = %v", aliases)
	}
	if aliases["Read"] != "read" || aliases["Execute"] != "bash" {
		t.Fatalf("aliases = %v", aliases)
	}
	if strings.Contains(string(out), `"name":"read"`) || strings.Contains(string(out), `"name":"bash"`) {
		t.Fatalf("refused name still declared: %s", out)
	}
	if !strings.Contains(string(out), `"name":"custom_tool"`) {
		t.Fatalf("accepted tool renamed: %s", out)
	}
	// History and tool_choice use the same name as the declaration.
	var m map[string]any
	if json.Unmarshal(out, &m) != nil {
		t.Fatal("not JSON")
	}
	call := m["messages"].([]any)[1].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if call["name"] != "Read" {
		t.Fatalf("history not renamed: %v", call["name"])
	}

	// A body that declares no refused name is untouched.
	clean := []byte(`{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"ok"}}]}`)
	if got, a := AliasToolNames(clean); len(a) != 0 || string(got) != string(clean) {
		t.Fatalf("clean body changed: %s", got)
	}
}

func TestRestoreToolNames(t *testing.T) {
	reply := []byte(`{"choices":[{"message":{"tool_calls":[{"function":{"name":"Read","arguments":"{\"name\":\"Read\"}"}}]}}]}`)
	out, changed := RestoreToolNames(reply, map[string]string{"Read": "read"})
	if !changed {
		t.Fatal("reply not restored")
	}
	if !strings.Contains(string(out), `"name":"read"`) {
		t.Fatalf("call name not restored: %s", out)
	}
	// Arguments are the model's data: a "name" inside them stays as it was.
	if !strings.Contains(string(out), `\\\"name\\\":\\\"Read\\\"`) && !strings.Contains(string(out), `"name\":\"Read\"`) {
		t.Fatalf("arguments rewritten: %s", out)
	}
}

func TestRestoreToolNamesPreservesGeminiNumbers(t *testing.T) {
	reply := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Read","args":{"n":9007199254740993}}}]}}]}`)
	out, changed := RestoreToolNames(reply, map[string]string{"Read": "read"})
	if !changed || !strings.Contains(string(out), `"name":"read"`) || !strings.Contains(string(out), `"n":9007199254740993`) {
		t.Fatalf("restoring the tool name changed the arguments: %s", out)
	}
}

// Responses declares tools flat ({"type":"function","name":...}), not nested
// under "function"; both shapes must be renamed.
func TestAliasToolNamesResponsesShape(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","input":[],"tools":[{"type":"function","name":"read","description":"Read a file.","parameters":{"type":"object"}}]}`)
	out, aliases := AliasToolNames(body)
	if aliases["Read"] != "read" {
		t.Fatalf("aliases = %v", aliases)
	}
	if strings.Contains(string(out), `"name":"read"`) || !strings.Contains(string(out), `"name":"Read"`) {
		t.Fatalf("flat declaration not renamed: %s", out)
	}
}

// Rename-all mode renames every tool Factory has not been measured to accept,
// because the refused list grows: todo_write was refused while our list held
// only the older names. droid's own names and mcp__ tools are kept.
func TestAliasToolNamesRenamingAll(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"tools":[` +
		`{"type":"function","function":{"name":"todo_write","description":"d"}},` +
		`{"type":"function","function":{"name":"custom_helper","description":"d"}},` +
		`{"type":"function","function":{"name":"Read","description":"d"}},` +
		`{"type":"function","function":{"name":"mcp__rea__analyze","description":"d"}}]}`)
	// mcp__rea__analyze is an MCP name, which Factory refuses as well, so three
	// names are renamed here; mcp__test-style names and droid's own are kept.
	out, aliases := AliasToolNamesRenamingAll(body)
	if len(aliases) != 3 {
		t.Fatalf("aliases = %v", aliases)
	}
	if strings.Contains(string(out), `"name":"todo_write"`) || strings.Contains(string(out), `"name":"custom_helper"`) {
		t.Fatalf("unmeasured names kept: %s", out)
	}
	if !strings.Contains(string(out), `"name":"Read"`) {
		t.Fatalf("droid name renamed: %s", out)
	}
	// Restoring by alias returns the caller's names.
	alias := ""
	for a, original := range aliases {
		if original == "todo_write" {
			alias = a
		}
	}
	if alias == "" {
		t.Fatalf("no alias for todo_write: %v", aliases)
	}
	restored, changed := RestoreToolNames([]byte(`{"calls":[{"name":"`+alias+`"}]}`), aliases)
	if !changed || !strings.Contains(string(restored), "todo_write") {
		t.Fatalf("restore = %s", restored)
	}
}

// MCP tool names (mcp__server__tool) are refused by Factory while plain double
// underscores are not; the caller's names must come back in the reply.
func TestAliasToolNamesMCP(t *testing.T) {
	body := []byte(`{"model":"m","messages":[],"tools":[` +
		`{"type":"function","function":{"name":"mcp__rea__comment","description":"d"}},` +
		`{"type":"function","function":{"name":"mcp__test","description":"d"}}]}`)
	out, aliases := AliasToolNames(body)
	if len(aliases) != 1 {
		t.Fatalf("aliases = %v", aliases)
	}
	var alias string
	for a, original := range aliases {
		if original != "mcp__rea__comment" {
			t.Fatalf("aliases = %v", aliases)
		}
		alias = a
	}
	if mcpToolNameRE.MatchString(alias) || strings.ContainsAny(alias, "-.") {
		t.Fatalf("alias still refused: %q", alias)
	}
	if !strings.Contains(string(out), `"name":"mcp__test"`) {
		t.Fatalf("accepted mcp name renamed: %s", out)
	}
	restored, changed := RestoreToolNames([]byte(`{"calls":[{"name":"`+alias+`"}]}`), aliases)
	if !changed || !strings.Contains(string(restored), "mcp__rea__comment") {
		t.Fatalf("restore = %s", restored)
	}
	// The same name always gets the same alias.
	if _, again := AliasToolNames(body); len(again) != 1 {
		t.Fatal("no aliases on the second pass")
	} else {
		for a := range again {
			if a != alias {
				t.Fatalf("unstable alias: %q vs %q", a, alias)
			}
		}
	}
}
