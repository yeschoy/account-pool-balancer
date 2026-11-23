package server

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

const dshBenchTool = "mcp__workspace__inspect_0"

// Synthetic DSH workload: 265 unique tools, including 229 MCP tools, a long
// system instruction and replayed function calls/responses. No client data,
// credentials or network requests are used by these benchmarks.
func dshBenchmarkTools() []any {
	decls := make([]any, 0, 265)
	for i := 0; i < 265; i++ {
		prefix := "workspace_action_"
		if i < 229 {
			prefix = "mcp__workspace__inspect_"
		}
		decls = append(decls, map[string]any{
			"name":        prefix + strconv.Itoa(i),
			"description": strings.Repeat("Inspect the selected workspace item and return the result. ", 12),
			"parameters": map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{"path": map[string]any{"type": []any{"string", "null"}}, "depth": map[string]any{"type": "integer"}},
			},
		})
	}
	return decls
}

func BenchmarkFactoryPrepareGeminiDSH(b *testing.B) {
	contents := make([]any, 0, 96)
	for i := 0; i < 32; i++ {
		contents = append(contents,
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Please summarize the relevant source files."}}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": dshBenchTool, "args": map[string]any{"path": "sample.go"}}}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": dshBenchTool, "response": map[string]any{"output": strings.Repeat("name=sample.go status=ok role=fixture\n", 70)}}}}},
		)
	}
	body, err := json.Marshal(map[string]any{
		"contents":          contents,
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": strings.Repeat("Use the available tools to check the answer. ", 200)}}},
		"tools":             []any{map[string]any{"functionDeclarations": dshBenchmarkTools()}},
	})
	benchDSHPrepare(b, dshGeminiPath, body, err)
}

func BenchmarkFactoryPrepareChatDSH(b *testing.B) {
	tools := dshBenchmarkTools()
	for i, decl := range tools {
		tools[i] = map[string]any{"type": "function", "function": decl}
	}
	messages := []any{map[string]any{"role": "system", "content": strings.Repeat("Use the available tools to check the answer. ", 200)}}
	for i := 0; i < 32; i++ {
		id := "call_" + strconv.Itoa(i)
		messages = append(messages,
			map[string]any{"role": "user", "content": "Please summarize the relevant source files."},
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": dshBenchTool, "arguments": `{"path":"sample.go"}`}}}},
			map[string]any{"role": "tool", "tool_call_id": id, "content": strings.Repeat("name=sample.go status=ok role=fixture\n", 70)},
		)
	}
	body, err := json.Marshal(map[string]any{"model": "deepseek-v4.1-flash", "messages": messages, "stream": true, "tools": tools})
	benchDSHPrepare(b, dshChatPath, body, err)
}

func benchDSHPrepare(b *testing.B, path string, body []byte, err error) {
	b.Helper()
	if err != nil {
		b.Fatal(err)
	}
	cfg := config.Default()
	cfg.Factory.SystemMode = config.SystemMagpie
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := prepareFactory(cfg, path, body); err != nil {
			b.Fatal(err)
		}
	}
}
