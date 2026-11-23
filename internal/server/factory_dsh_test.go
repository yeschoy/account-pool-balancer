package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factory"
	"account-pool-balancer/internal/opencode"
)

const (
	dshGeminiPath = "/v1beta/models/gemini-4-carbon:streamGenerateContent"
	dshChatPath   = "/v1/chat/completions"
)

const dshChatToolHistory = `{
  "model":"deepseek-v4.1-flash","stream":true,
  "messages":[
    {"role":"system","content":SYSTEM_FIXTURE},
    {"role":"user","content":"Inspect sample.go"},
    {"role":"assistant","tool_calls":[{"id":"call_prior","type":"function","function":{"name":"mcp__workspace__inspect","arguments":"{\"n\":9007199254740993}"}}]},
    {"role":"tool","tool_call_id":"call_prior","content":"ok <sample.go>"}
  ],
  "tool_choice":{"type":"function","function":{"name":"mcp__workspace__inspect"}},
  "tools":[{"type":"function","function":{"name":"mcp__workspace__inspect","parameters":{"type":"object","additionalProperties":false,"properties":{"n":{"type":"integer","enum":[9007199254740993]}}}}}],
  "extra":{"seed":9007199254740993}
}`

const dshGeminiToolHistory = `{
  "contents":[
    {"role":"user","parts":[{"text":"Inspect sample.go"}]},
    {"role":"model","parts":[{"functionCall":{"name":"mcp__workspace__inspect","args":{"n":9007199254740993}},"thoughtSignature":"opaque"}]},
    {"role":"user","parts":[{"functionResponse":{"name":"mcp__workspace__inspect","response":{"output":"ok <sample.go>"}}}]}
  ],
  "systemInstruction":{"role":"system","parts":[{"text":"Follow the task instructions."}]},
  "stream":true,
  "toolConfig":{"functionCallingConfig":{"allowedFunctionNames":["mcp__workspace__inspect"]}},
  "tools":[{"functionDeclarations":[{"name":"mcp__workspace__inspect","parameters":{"type":"object","additionalProperties":false,"properties":{"n":{"type":"integer","enum":[9007199254740993]}}}}]}]
}`

func TestFactoryChatToolPreparationParity(t *testing.T) {
	for _, tc := range []struct {
		name, mode, body   string
		trusted, renameAll bool
	}{
		{"magpieString", config.SystemMagpie, strings.ReplaceAll(dshChatToolHistory, "SYSTEM_FIXTURE", fmt.Sprintf("%q", refusedSystem)), false, true},
		{"magpieParts", config.SystemMagpie, strings.ReplaceAll(dshChatToolHistory, "SYSTEM_FIXTURE", `[{"type":"text","text":"Follow the task."}]`), false, false},
		{"trustedMagpie", config.SystemMagpie, strings.ReplaceAll(dshChatToolHistory, "SYSTEM_FIXTURE", fmt.Sprintf("%q", refusedSystem)), true, true},
		{"openWithoutSystem", config.SystemOpen, ` {"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"task"}]} `, false, true},
		{"postAdapterPhraseHandling", config.SystemOpen, strings.ReplaceAll(dshChatToolHistory, "SYSTEM_FIXTURE", fmt.Sprintf("%q", refusedSystem)), false, true},
		{"openNoOp", config.SystemOpen, ` {"model":"deepseek-v4.1-flash","messages":[{"role":"system","content":"` + factory.DroidLine + `"}]} `, false, true},
		{"keep", config.SystemKeep, strings.ReplaceAll(dshChatToolHistory, "SYSTEM_FIXTURE", `"Follow the task."`), false, true},
		{"fold", config.SystemFold, strings.ReplaceAll(dshChatToolHistory, "SYSTEM_FIXTURE", `"Follow the task."`), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Factory.SystemMode, cfg.Factory.RenameAllTools = tc.mode, &tc.renameAll
			want := []byte(tc.body)
			var wantAliases map[string]string
			var adapted, escaped bool
			switch tc.mode {
			case config.SystemFold:
				want = factory.FoldSystemIntoUser("chat", want)
			case config.SystemKeep:
			default:
				if tc.mode == config.SystemMagpie && !tc.trusted {
					want, adapted = factory.MagpieAdapt("chat", want)
				}
				if tc.renameAll {
					want, wantAliases = factory.AliasToolNamesRenamingAll(want)
				} else {
					want, wantAliases = factory.AliasToolNames(want)
				}
				want = factory.OpenDroidBody("chat", want)
			}
			if cfg.Factory.EffectiveEscapeClientPhrases() && !tc.trusted {
				want, escaped = factory.EscapeClientPhrases(want)
			}
			got, err := prepareFactoryFor(cfg, dshChatPath, []byte(tc.body), tc.trusted)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.body, want) || !maps.Equal(got.toolAliases, wantAliases) || got.adapted != adapted || got.escaped != escaped {
				t.Fatalf("got %s %v adapted=%t escaped=%t\nwant %s %v adapted=%t escaped=%t", got.body, got.toolAliases, got.adapted, got.escaped, want, wantAliases, adapted, escaped)
			}
		})
	}
}

func TestFactoryDSHNativeChatToolRoundTrip(t *testing.T) {
	e := newFactoryEnv(t)
	setFactoryMode(t, e, func(c *config.Config) { c.Factory.SystemMode = config.SystemMagpie })
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, _ *http.Request) {
		var req struct {
			Tools []struct {
				Function struct{ Name string } `json:"function"`
			} `json:"tools"`
		}
		calls := e.snapshot()
		if len(calls) != 1 || json.Unmarshal([]byte(calls[0].body), &req) != nil || len(req.Tools) != 1 {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_next","index":0,"type":"function","function":{"name":%q,"arguments":"{\"n\":9007199254740993}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n", req.Tools[0].Function.Name)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}
	e.mu.Unlock()
	body := strings.ReplaceAll(dshChatToolHistory, "SYSTEM_FIXTURE", `"Follow the task."`)
	rsp, out := e.do(t, dshChatPath, body, nil)
	calls := e.snapshot()
	if rsp.StatusCode != 200 || rsp.Header.Get("X-Compatibility-Mode") != "" || !strings.HasPrefix(rsp.Header.Get("Content-Type"), "text/event-stream") || len(calls) != 1 {
		t.Fatal(rsp.StatusCode, rsp.Header, out, len(calls))
	}
	if calls[0].path != "/api/llm/o/v1/chat/completions" {
		t.Fatalf("wrong upstream route: %s", calls[0].path)
	}
	_, aliases := factory.AliasToolNames([]byte(body))
	if len(aliases) != 1 {
		t.Fatalf("expected one aliased tool, got %v", aliases)
	}
	for alias := range aliases {
		if strings.Count(calls[0].body, `"name":"`+alias+`"`) != 3 || strings.Contains(out, alias) {
			t.Fatalf("inconsistent tool names in request %s or SSE response %s", calls[0].body, out)
		}
	}
	if !strings.Contains(calls[0].body, `"enum":[9007199254740993]`) || !strings.Contains(calls[0].body, `"tool_call_id":"call_prior"`) || strings.Contains(calls[0].body, "mcp__workspace__inspect") {
		t.Fatalf("tool schema or history changed: %s", calls[0].body)
	}
	for _, s := range []string{`"name":"mcp__workspace__inspect"`, `"id":"call_next"`, `\"n\":9007199254740993`, `"finish_reason":"tool_calls"`, `data: [DONE]`} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing %s in SSE response: %s", s, out)
		}
	}
}

// Compare final bytes and alias tables to the original sequential adapters:
// reordering tool work must not change the payload used for prompt caching.
func TestFactoryGeminiToolPreparationParity(t *testing.T) {
	for _, mode := range []string{config.SystemMagpie, config.SystemOpen, config.SystemFold, config.SystemKeep} {
		for _, name := range []string{"mcp__workspace__inspect", "workspace_action", "Read"} {
			for _, renameAll := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/renameAll=%t", mode, name, renameAll), func(t *testing.T) {
					cfg := config.Default()
					cfg.Factory.SystemMode = mode
					cfg.Factory.RenameAllTools = &renameAll
					body := []byte(strings.ReplaceAll(dshGeminiToolHistory, "mcp__workspace__inspect", name))
					got, err := prepareFactory(cfg, dshGeminiPath, body)
					if err != nil {
						t.Fatal(err)
					}

					want := body
					var aliases map[string]string
					if mode != config.SystemKeep {
						if renameAll {
							want, aliases = factory.AliasToolNamesRenamingAll(want)
						} else {
							want, aliases = factory.AliasToolNames(want)
						}
					}
					want = factory.DropGeminiStreamField(want)
					if mode == config.SystemMagpie {
						want = factory.MagpieAdaptGemini(want)
					}
					if mode == config.SystemFold {
						want, err = factory.FoldGeminiSystem("gemini-4-carbon", want)
					} else {
						want, err = factory.OpenGeminiBodyWith("gemini-4-carbon", want, mode != config.SystemKeep)
					}
					if err != nil {
						t.Fatal(err)
					}
					want, _ = factory.SanitizeGeminiTools(want)
					if !bytes.Equal(got.body, want) || !maps.Equal(got.toolAliases, aliases) {
						t.Fatalf("got %s %v\nwant %s %v", got.body, got.toolAliases, want, aliases)
					}
				})
			}
		}
	}
}

func TestFactoryGeminiBatchedThinkingOptions(t *testing.T) {
	for _, tc := range []struct {
		name, input, wantConfig string
		from                    opencode.Protocol
		include                 bool
	}{
		{"nativeThoughtsThenLevel", `{"contents":[]}`, `{"thinkingConfig":{"includeThoughts":true}}`, "", true},
		{"nativeLevelOnly", `{"contents":[]}`, `{"thinkingConfig":{"thinkingLevel":"low"}}`, "", false},
		{"convertedDoesNotInjectThoughts", `{"contents":[]}`, `{"thinkingConfig":{"thinkingLevel":"low"}}`, opencode.Chat, true},
		{"explicitThoughtsOff", `{"contents":[],"generationConfig":{ "thinkingConfig":{"includeThoughts":false} }}`, `{ "thinkingConfig":{"includeThoughts":false} }`, "", true},
		{"preserveClientLevel", `{"contents":[],"generationConfig":{"thinkingConfig":{"thinkingLevel":"high"}}}`, `{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"high"}}`, "", true},
		{"preserveBudget", `{"contents":[],"generationConfig":{"thinkingBudget":0}}`, `{"thinkingBudget":0}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Factory.ThinkingLevel = "low"
			cfg.Factory.IncludeThoughts = &tc.include
			got, err := prepareFactoryGenerate(cfg, "gemini-4-carbon", true, []byte(tc.input), tc.from, false)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got.body), `"generationConfig":`+tc.wantConfig) {
				t.Fatalf("thinking settings changed: %s", got.body)
			}
		})
	}
}

func TestFactoryDSHNativeGeminiToolRoundTrip(t *testing.T) {
	e := newFactoryEnv(t)
	_, aliases := factory.AliasToolNames([]byte(dshGeminiToolHistory))
	if len(aliases) != 1 {
		t.Fatalf("expected one aliased tool, got %v", aliases)
	}
	alias := ""
	for name := range aliases {
		alias = name
	}
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":%q,"args":{"n":9007199254740993}},"thoughtSignature":"opaque"}]}}]}`+"\n\n", alias)
		fmt.Fprint(w, `data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":123,"cachedContentTokenCount":100}}`+"\n\n")
	}
	e.mu.Unlock()
	rsp, out := e.do(t, dshGeminiPath+"?alt=sse", dshGeminiToolHistory, map[string]string{"User-Agent": "google-genai-sdk/2.21.0, deepseek"})
	if rsp.StatusCode != 200 || !strings.HasPrefix(rsp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatal(rsp.StatusCode, rsp.Header, out)
	}
	calls := e.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected one upstream call, got %d", len(calls))
	}
	wire := calls[0].body
	if strings.Contains(wire, "mcp__workspace__inspect") || strings.Contains(wire, "additionalProperties") ||
		strings.Count(wire, `"name":"`+alias+`"`) != 3 || !strings.Contains(wire, `"allowedFunctionNames":["`+alias+`"]`) ||
		!strings.Contains(wire, `"enum":["9007199254740993"]`) || !strings.Contains(wire, `"args":{"n":9007199254740993}`) {
		t.Fatalf("unexpected upstream request: %s", wire)
	}
	for _, s := range []string{`"name":"mcp__workspace__inspect"`, `"args":{"n":9007199254740993}`, `"thoughtSignature":"opaque"`, `"finishReason":"STOP"`, `"cachedContentTokenCount":100`} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing %s in SSE response: %s", s, out)
		}
	}
}
