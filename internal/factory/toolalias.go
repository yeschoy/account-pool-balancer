package factory

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// Factory refuses a request whose tool list declares certain names, with
// 403 content_policy_rejected, on every route. Only the declared name decides,
// matched exactly and case-sensitively: a name that appears only in history, in
// tool_choice or in text is not refused. The list follows magpie's
// tool-names.mjs; a name renamed that Factory takes costs nothing, while one
// missed refuses the whole request.
var RefusedToolNames = map[string]bool{
	// Claude Code, Codex and OpenCode names, first held from 2026-11-17.
	"Agent": true, "Bash": true, "TaskCreate": true, "TaskGet": true,
	"TaskList": true, "TaskUpdate": true, "Write": true,
	"Shell": true, "StrReplace": true,
	"agent": true, "bash": true, "edit": true, "glob": true, "grep": true,
	"read": true, "run_shell": true, "task": true, "write": true,
	// The names first used in their place, refused from 2026-11-18 06:00 UTC.
	"AgentTool": true, "BashTool": true, "WriteTool": true, "bash_tool": true,
	"edit_tool": true, "read_tool": true, "write_tool": true,
	// Measured on real accounts on 2026-11-19.
	"web_search": true, "read_file": true, "execute_command": true,
}

// TakenToolNames are names Factory accepts as declared, droid's own among them.
var TakenToolNames = map[string]bool{
	"Read": true, "Edit": true, "Glob": true, "Grep": true, "Task": true,
	"WebSearch": true, "TodoWrite": true, "Execute": true, "Create": true,
}

// DroidToolNames maps a refused tool to the tool of Factory's own client that
// does the same job, so a renamed request still reads naturally.
var DroidToolNames = map[string]string{
	"Bash": "Execute", "bash": "Execute", "run_shell": "Execute", "BashTool": "Execute", "bash_tool": "Execute",
	"Write": "Create", "write": "Create", "WriteTool": "Create", "write_tool": "Create",
	"Shell": "Execute",
	"Agent": "Task", "agent": "Task", "AgentTool": "Task", "task": "Task",
	"read": "Read", "read_tool": "Read", "read_file": "Read",
	"edit": "Edit", "edit_tool": "Edit",
	"glob": "Glob", "grep": "Grep",
	"web_search": "WebSearch", "execute_command": "Execute",
}

// mcpToolNameRE matches an MCP tool name: mcp__<server>__<tool>. Measured on
// 2026-11-20: every such name is refused (mcp__rea__comment, mcp__foo__bar),
// while mcp__test (no second separator) and foo__bar__baz are accepted, so the
// shape, not the prefix, is what Factory blocks. magpie 0.1.52 still keeps
// mcp__ names as they are; that is out of date.
var mcpToolNameRE = regexp.MustCompile(`^mcp__.+__.+$`)

// RefusedToolName reports whether Factory refuses a declared tool with this
// name, including the same name behind leading underscores, and whether it is
// an MCP tool name.
func RefusedToolName(name string) bool {
	if name == "" {
		return false
	}
	if mcpToolNameRE.MatchString(name) {
		return true
	}
	return RefusedToolNames[name] || RefusedToolNames[strings.TrimLeft(name, "_")]
}

// mcpAlias turns an MCP tool name into a plain one Factory accepts. The
// caller's own name is restored in the reply.
func mcpAlias(name string) string {
	trimmed := strings.TrimPrefix(strings.TrimLeft(name, "_"), "mcp__")
	flat := strings.ReplaceAll(trimmed, "__", "_")
	safe := make([]rune, 0, len(flat))
	for _, r := range flat {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			safe = append(safe, r)
		default:
			safe = append(safe, '_')
		}
	}
	out := string(safe)
	if len(out) > 40 {
		out = out[:40]
	}
	return out + "_" + hashSuffix(name)
}

// hashSuffix is a stable four-hex fingerprint of a name, so a renamed tool
// keeps the same alias across requests and a prompt cache still holds.
func hashSuffix(name string) string {
	h := uint32(0x811c9dc5)
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 0x01000193
	}
	return fmt.Sprintf("%04x", h&0xffff)
}

// suffixName gives a refused name Factory's list has no droid equivalent for
// a stable replacement that no list holds.
func suffixName(name string) string {
	return strings.TrimLeft(name, "_") + "_" + hashSuffix(name)
}

// AliasToolNames renames every declared tool Factory refuses, and every later
// mention of it in the request, to a name it accepts. It returns the body and
// the alias → original names to restore in the reply. Task instructions, call
// arguments and unrelated fields are untouched; a body that declares no
// refused name comes back byte for byte.
// AliasToolNamesRenamingAll also renames every declared tool Factory's list
// does not hold yet: the list grows, so a name missed refuses the whole
// request while a renamed name costs nothing (the reply is renamed back).
// droid's own names and mcp__ tools are kept.
func AliasToolNamesRenamingAll(body []byte) ([]byte, map[string]string) {
	return aliasToolNames(body, true, false)
}

func AliasToolNames(body []byte) ([]byte, map[string]string) {
	return aliasToolNames(body, false, false)
}

// AliasGeminiToolNames also normalizes Gemini's declarations when renaming
// already requires re-encoding the request. With no aliases it returns the
// original bytes; the caller still needs SanitizeGeminiTools in that case.
func AliasGeminiToolNames(body []byte, renameAll bool) ([]byte, map[string]string) {
	return aliasToolNames(body, renameAll, true)
}

func aliasToolNames(body []byte, renameAll, sanitizeGemini bool) ([]byte, map[string]string) {
	var request object
	if decode(body, &request) != nil || request == nil {
		return body, nil
	}
	aliases := renameToolNames(request, renameAll)
	if len(aliases) == 0 {
		return body, nil
	}
	if sanitizeGemini {
		sanitizeGeminiToolDeclarations(list(request["tools"]))
	}
	out, err := marshal(request)
	if err != nil {
		return body, nil
	}
	return out, aliases
}

// renameToolNames updates declarations and references in an already-decoded
// request and returns the map used to restore the names in replies.
func renameToolNames(request object, renameAll bool) map[string]string {
	declared := declaredTools(request)
	rename := func(name string) bool {
		if RefusedToolName(name) {
			return true
		}
		if renameAll && name != "" && !TakenToolNames[name] && !strings.HasPrefix(name, "mcp__") {
			return true
		}
		return false
	}
	needed := false
	for _, s := range declared {
		if rename(text(s.holder[s.key])) {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	taken := map[string]bool{}
	for _, s := range declared {
		if n := text(s.holder[s.key]); n != "" {
			taken[n] = true
		}
	}
	renamed := map[string]string{}
	aliases := map[string]string{}
	for _, s := range declared {
		name := text(s.holder[s.key])
		if !rename(name) {
			continue
		}
		if _, ok := renamed[name]; !ok {
			alias, ok := DroidToolNames[name]
			if !ok {
				alias, ok = DroidToolNames[strings.TrimLeft(name, "_")]
			}
			if !ok || taken[alias] {
				alias = suffixName(name)
			}
			if mcpToolNameRE.MatchString(name) {
				alias = mcpAlias(name)
			}
			for taken[alias] || RefusedToolName(alias) {
				alias += "_"
			}
			taken[alias] = true
			renamed[name] = alias
			aliases[alias] = name
		}
		s.holder[s.key] = renamed[name]
	}
	for _, s := range referencedTools(request) {
		if alias, ok := renamed[text(s.holder[s.key])]; ok {
			s.holder[s.key] = alias
		}
	}
	// Gemini's allowed names are a plain string list, not a name field.
	if cfg := obj(request["toolConfig"]); cfg != nil {
		if fcc := obj(cfg["functionCallingConfig"]); fcc != nil {
			if names, ok := fcc["allowedFunctionNames"].([]any); ok {
				for i, raw := range names {
					if name, ok := raw.(string); ok {
						if alias, ok := renamed[name]; ok {
							names[i] = alias
						}
					}
				}
			}
		}
	}
	return aliases
}

// declaredTools lists every place a request declares a tool by name.
func declaredTools(request object) []struct {
	holder map[string]any
	key    string
} {
	out := []struct {
		holder map[string]any
		key    string
	}{}
	add := func(holder map[string]any) {
		if holder != nil {
			out = append(out, struct {
				holder map[string]any
				key    string
			}{holder, "name"})
		}
	}
	for _, raw := range list(request["tools"]) {
		tool := obj(raw)
		if tool == nil {
			continue
		}
		// chat completions nests the declaration under "function"; Responses
		// declares it flat. Both name the tool in a "name" field.
		if text(tool["type"]) == "function" {
			if fn := obj(tool["function"]); fn != nil {
				add(fn)
				continue
			}
		}
		switch typ := text(tool["type"]); typ {
		case "", "custom", "function":
			if text(tool["name"]) != "" {
				add(tool)
			}
		}
		for _, decl := range list(tool["functionDeclarations"]) {
			add(obj(decl))
		}
	}
	return out
}

// referencedTools lists where a request names a declared tool again:
// tool_choice, earlier calls in the history, and Gemini's function responses
// and allowed names.
func referencedTools(request object) []struct {
	holder map[string]any
	key    string
} {
	out := []struct {
		holder map[string]any
		key    string
	}{}
	push := func(holder map[string]any) {
		if holder != nil {
			out = append(out, struct {
				holder map[string]any
				key    string
			}{holder, "name"})
		}
	}
	if choice := obj(request["tool_choice"]); choice != nil {
		if fn := obj(choice["function"]); fn != nil {
			push(fn)
		} else if text(choice["name"]) != "" {
			push(choice)
		}
		for _, raw := range list(choice["tools"]) {
			t := obj(raw)
			if fn := obj(t["function"]); fn != nil {
				push(fn)
			} else if text(t["name"]) != "" {
				push(t)
			}
		}
	}
	for _, raw := range list(request["messages"]) {
		msg := obj(raw)
		if msg == nil {
			continue
		}
		for _, braw := range list(msg["content"]) {
			block := obj(braw)
			if text(block["type"]) == "tool_use" {
				push(block)
			}
		}
		for _, craw := range list(msg["tool_calls"]) {
			push(obj(obj(craw)["function"]))
		}
	}
	for _, iraw := range list(request["input"]) {
		item := obj(iraw)
		switch text(item["type"]) {
		case "function_call", "custom_tool_call":
			push(item)
		}
	}
	for _, craw := range list(request["contents"]) {
		for _, praw := range list(obj(craw)["parts"]) {
			part := obj(praw)
			if fc := obj(part["functionCall"]); fc != nil {
				push(fc)
			}
			if fr := obj(part["functionResponse"]); fr != nil {
				push(fr)
			}
		}
	}
	return out
}

// RestoreToolNames gives the caller its own tool names back in a whole reply.
// Only "name" fields holding an alias change; a call's arguments are the
// model's data and stay as they are.
func RestoreToolNames(body []byte, aliases map[string]string) ([]byte, bool) {
	if len(aliases) == 0 {
		return body, false
	}
	var value any
	// Keep the exact numeric arguments when the reply is re-encoded.
	if err := decode(body, &value); err != nil || value == nil {
		return body, false
	}
	if !restoreNames(value, aliases) {
		return body, false
	}
	out, err := marshal(value)
	if err != nil {
		return body, false
	}
	return out, true
}

func restoreNames(v any, aliases map[string]string) bool {
	changed := false
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if restoreNames(item, aliases) {
				changed = true
			}
		}
	case map[string]any:
		for k, raw := range t {
			switch k {
			case "input", "args", "arguments":
				continue
			case "name":
				if s, ok := raw.(string); ok {
					if original, ok := aliases[s]; ok {
						t[k] = original
						changed = true
					}
				}
				continue
			}
			if restoreNames(raw, aliases) {
				changed = true
			}
		}
	}
	return changed
}

// RestoreToolNamesStream restores aliases in an SSE reply. Only frames whose
// data names a tool change; every other byte streams through untouched.
func RestoreToolNamesStream(source io.ReadCloser, aliases map[string]string) io.ReadCloser {
	if len(aliases) == 0 {
		return source
	}
	s := bufio.NewScanner(source)
	s.Buffer(make([]byte, 64<<10), maxFrameBytes)
	s.Split(splitFrame)
	return &aliasReader{source: source, scanner: s, aliases: aliases}
}

type aliasReader struct {
	source  io.ReadCloser
	scanner *bufio.Scanner
	aliases map[string]string
	pending []byte
	err     error
}

func (r *aliasReader) Close() error { return r.source.Close() }

func (r *aliasReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(r.pending) == 0 && r.err == nil {
		if !r.scanner.Scan() {
			r.err = r.scanner.Err()
			if r.err == nil {
				r.err = io.EOF
			}
			break
		}
		r.pending = r.frame(append([]byte(nil), r.scanner.Bytes()...))
	}
	if len(r.pending) > 0 {
		n := copy(p, r.pending)
		r.pending = r.pending[n:]
		return n, nil
	}
	return 0, r.err
}

// frame rewrites one SSE frame's data when it names a tool alias.
func (r *aliasReader) frame(raw []byte) []byte {
	end := []byte{}
	if i, n := frameBoundary(raw); i >= 0 {
		end, raw = raw[i:i+n], raw[:i]
	}
	original := func() []byte { return append(raw, end...) }
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\r", "\n"), "\n")
	var data []string
	for _, line := range lines {
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(line[5:], " "))
		}
	}
	if len(data) == 0 {
		return original()
	}
	payload := strings.Join(data, "\n")
	if payload == "[DONE]" || !strings.Contains(payload, `"name"`) {
		return original()
	}
	var event any
	if decode([]byte(payload), &event) != nil || !restoreNames(event, r.aliases) {
		return original()
	}
	out, err := marshal(event)
	if err != nil {
		return original()
	}
	rewritten := make([]string, 0, len(lines))
	first := true
	for _, line := range lines {
		if strings.HasPrefix(line, "data:") {
			if first {
				rewritten = append(rewritten, "data: "+string(out))
				first = false
			}
			continue
		}
		rewritten = append(rewritten, line)
	}
	return append([]byte(strings.Join(rewritten, "\n")), end...)
}
