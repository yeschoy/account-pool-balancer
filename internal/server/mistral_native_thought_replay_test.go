package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Native content plus an equal OpenAI reasoning field are two representations
// of one thought. Toggling the redundant field must not rewrite old history.
// These are local mock uploads; they do not measure production cache hits.
func TestMistralNativeThoughtAliasDoesNotRewriteHistory(t *testing.T) {
	thought, _ := json.Marshal("  complete thought 中文\n\tkeep spaces  ")
	content := fmt.Sprintf(`[{ "type":"thinking", "thinking":[{"type":"text","text":%s}], "closed":true, "signature":null },{"type":"text","text":" answer\n  keep indent"}]`, thought)
	for _, model := range []string{"deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash"} {
		t.Run(model, func(t *testing.T) {
			e := newMistralEnv(t)
			var firstMessage, firstTools []byte
			for i, alias := range []string{"", `,"reasoning_content":` + string(thought), `,"reasoning":` + string(thought), `,"reasoning":` + string(thought) + `,"reasoning_content":` + string(thought)} {
				body := fmt.Sprintf(`{"model":%q,"tools":[{"type":"function","function":{"name":"tool","parameters":{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"integer"}}}}}],"messages":[{"role":"system","content":"fixed instruction"},{"role":"user","content":"fixed question"},{"role":"assistant"%s,"content":%s},{"role":"user","content":"next question"}]}`, model, alias, content)
				if status, _ := e.post(t, "/v1/chat/completions", body, map[string]string{"Session-Id": "synthetic-native-thought-session"}); status != 200 || e.count() != i+1 {
					t.Fatal("local mock request failed")
				}
				call := e.call(i)
				_, fields := mistralOrderedFieldsForTest(t, call.body)
				var messages []json.RawMessage
				if json.Unmarshal(fields["messages"], &messages) != nil || len(messages) != 4 {
					t.Fatal("invalid message sequence")
				}
				if i == 0 {
					firstMessage, firstTools = bytes.Clone(messages[2]), bytes.Clone(fields["tools"])
				}
				var assistant struct{ Content []struct{ Type string } }
				if json.Unmarshal(messages[2], &assistant) != nil {
					t.Fatal("invalid assistant")
				}
				thinking := 0
				for _, chunk := range assistant.Content {
					if chunk.Type == "thinking" {
						thinking++
					}
				}
				if thinking != 1 || !bytes.Equal(messages[2], firstMessage) {
					t.Errorf("variant %d: redundant thought rewrote old assistant; native thinking count=%d", i, thinking)
				}
				if !bytes.Equal(fields["tools"], firstTools) || call.header.Get("X-Affinity") != "synthetic-native-thought-session" {
					t.Fatal("tool schema or cache affinity changed")
				}
			}
		})
	}
}

func TestMistralNativeThoughtAliasConservative(t *testing.T) {
	for _, tc := range []struct {
		name, model, thinking, extra, legacy string
		want                                 int
	}{
		{"same", "zai-glm-5-3-flash", "thought", "", "thought", 1},
		{"whitespace-not-equal", "zai-glm-5-3-flash", "thought ", "", "thought", 2},
		{"different-thoughts-retained", "zai-glm-5-3", "native", "", "legacy", 2},
		{"closed-flag-retained", "zai-glm-5-3", "thought", `,"closed":false`, "thought", 1},
		{"ambiguous-native-not-folded", "zai-glm-5-3", "thought", `,"type":"thinking"`, "thought", 2},
		{"signed-native-not-folded", "zai-glm-5-3", "thought", `,"signature":"synthetic-signature"`, "thought", 2},
		{"unknown-metadata-not-folded", "zai-glm-5-3", "thought", `,"future_metadata":true`, "thought", 2},
		{"other-model-unchanged", "mistral-large-4-0", "thought", "", "thought", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"assistant","reasoning_content":%q,"content":[{"type":"thinking","thinking":[{"type":"text","text":%q}]%s},{"type":"text","text":"answer"}]}]}`, tc.model, tc.legacy, tc.thinking, tc.extra))
			out, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Messages []struct{ Content []struct{ Type string } }
			}
			if json.Unmarshal(out, &decoded) != nil || len(decoded.Messages) != 1 {
				t.Fatal("invalid output")
			}
			count := 0
			for _, chunk := range decoded.Messages[0].Content {
				if chunk.Type == "thinking" {
					count++
				}
			}
			if count != tc.want {
				t.Fatalf("thinking chunks=%d want=%d", count, tc.want)
			}
		})
	}
}

// Characterize the separate flat-response boundary. Ordinary thinking->text
// retains all text/whitespace; interleaved text->thinking->text cannot roundtrip
// its ordering through just two scalar OpenAI fields. This test records that
// limitation; it does NOT claim the alias fix restores native response layout.
func TestMistralFlatResponseReplayBoundary(t *testing.T) {
	for _, model := range []string{"deepseek-v4-1-flash-1017", "zai-glm-5-3", "zai-glm-5-3-flash"} {
		for _, interleaved := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/interleaved=%t/stream=%t", model, interleaved, stream), func(t *testing.T) {
					chunks := []json.RawMessage{json.RawMessage(`{"type":"thinking","closed":true,"thinking":[{"type":"text","text":" reason \n"},{"type":"text","text":" 中文\t "}]}`), json.RawMessage(`{"type":"text","text":" answer\n  code "}`)}
					wantAnswer := " answer\n  code "
					if interleaved {
						chunks = append([]json.RawMessage{json.RawMessage(`{"type":"text","text":"before "}`)}, chunks...)
						wantAnswer = "before " + wantAnswer
					}
					var answer, reasoning strings.Builder
					groups := [][]json.RawMessage{chunks}
					field := "message"
					if stream {
						field = "delta"
						groups = nil
						for _, chunk := range chunks {
							groups = append(groups, []json.RawMessage{chunk})
						}
					}
					for _, group := range groups {
						content, _ := json.Marshal(group)
						response := []byte(fmt.Sprintf(`{"choices":[{%q:{"role":"assistant","content":%s}}]}`, field, content))
						flat, err := mistralChatResponse(response, stream)
						if err != nil {
							t.Fatal(err)
						}
						var root struct {
							Choices []map[string]struct {
								Content   string
								Reasoning string `json:"reasoning_content"`
							}
						}
						if json.Unmarshal(flat, &root) != nil || len(root.Choices) != 1 {
							t.Fatal("invalid flattened response")
						}
						answer.WriteString(root.Choices[0][field].Content)
						reasoning.WriteString(root.Choices[0][field].Reasoning)
					}
					if answer.String() != wantAnswer || reasoning.String() != " reason \n 中文\t " {
						t.Fatal("response adaptation added/dropped whitespace or text")
					}
					body := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"assistant","content":%q,"reasoning_content":%q}]}`, model, answer.String(), reasoning.String()))
					sent, _, err := prepareMistralRequest(body, "/v1/chat/completions", "synthetic-replay")
					if err != nil {
						t.Fatal(err)
					}
					var replay struct {
						Messages []struct{ Content []struct{ Type string } }
					}
					if json.Unmarshal(sent, &replay) != nil || len(replay.Messages) != 1 || len(replay.Messages[0].Content) != 2 || replay.Messages[0].Content[0].Type != "thinking" || replay.Messages[0].Content[1].Type != "text" {
						t.Fatal("unexpected scalar replay layout")
					}
					if interleaved {
						t.Log("native text->thinking->text becomes thinking->text on scalar replay; this remaining layout boundary is NOT evidence of a production cache miss")
					}
				})
			}
		}
	}
}
