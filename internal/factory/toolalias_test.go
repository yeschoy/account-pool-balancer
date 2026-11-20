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
	out, aliases := AliasToolNamesRenamingAll(body)
	if len(aliases) != 2 {
		t.Fatalf("aliases = %v", aliases)
	}
	if strings.Contains(string(out), `"name":"todo_write"`) || strings.Contains(string(out), `"name":"custom_helper"`) {
		t.Fatalf("unmeasured names kept: %s", out)
	}
	if !strings.Contains(string(out), `"name":"Read"`) || !strings.Contains(string(out), `"name":"mcp__rea__analyze"`) {
		t.Fatalf("accepted names renamed: %s", out)
	}
	// Restoring by alias returns the caller's names.
	var reply []byte = []byte(`{"calls":[{"name":"` + func() string {
		for a := range aliases {
			return a
		}
		return ""
	}() + `"}]}`)
	restored, changed := RestoreToolNames(reply, aliases)
	if !changed || !(strings.Contains(string(restored), "todo_write") || strings.Contains(string(restored), "custom_helper")) {
		t.Fatalf("restore = %s", restored)
	}
}
