package opencode

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestGeminiCompatibleRequestHistory(t *testing.T) {
	for _, tc := range []struct {
		protocol Protocol
		fields   string
	}{
		{Chat, `"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image_url","image_url":{"url":"data:image/png;base64,YQ=="}}]},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"n\":900719925474099312345}"}}]},{"role":"tool","tool_call_id":"call_1","content":"result"}],"stop":"END"`},
		{Responses, `"input":[{"role":"user","content":[{"type":"input_text","text":"看图"},{"type":"input_image","image_url":"data:image/png;base64,YQ=="}]},{"type":"function_call","id":"fc_original","status":"completed","call_id":"call_1","name":"lookup","arguments":"{\"n\":900719925474099312345}"},{"type":"function_call_output","call_id":"call_1","output":"result"}],"text":{"format":{"type":"text"}}`},
		{Messages, `"max_tokens":100,"messages":[{"role":"user","content":[{"type":"text","text":"看图"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"YQ=="}}]},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{"n":900719925474099312345}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result"}]}],"stop_sequences":["END"]`},
	} {
		t.Run(string(tc.protocol), func(t *testing.T) {
			raw, stream, _, err := ConvertGeminiRequest(tc.protocol, []byte(`{"model":"g","top_p":0.7,"temperature":0.4,`+tc.fields+`}`))
			const contents = `"contents":[{"parts":[{"text":"看图"},{"inlineData":{"data":"YQ==","mimeType":"image/png"}}],"role":"user"},{"parts":[{"functionCall":{"args":{"n":900719925474099312345},"id":"call_1","name":"lookup"}}],"role":"model"},{"parts":[{"functionResponse":{"id":"call_1","name":"lookup","response":{"output":"result"}}}],"role":"user"}]`
			if err != nil || stream || !strings.Contains(string(raw), contents) || !strings.Contains(string(raw), `"temperature":0.4,"topP":0.7`) || (tc.protocol != Responses && !strings.Contains(string(raw), `"stopSequences":["END"]`)) {
				t.Fatal(string(raw), stream, err)
			}
		})
	}
}

func TestGeminiCompatibleRequestRejectsUnsupported(t *testing.T) {
	for _, body := range []string{
		`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.test/a.png"}}]}]`,
		`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,%%%"}}]}]`,
		`"messages":[{"role":"user","content":"x"},{"role":"assistant","content":"pre-fill"}]`,
		`"messages":[{"role":"user","content":"x"}],"stop":["one",3]`,
		`"messages":[{"role":"user","content":"x"}],"n":2`,
		`"messages":[{"role":"user","content":"x"}],"top_p":1.1`,
		`"messages":[{"role":"user","content":"x"}],"stream_options":{"include_usage":true}`,
	} {
		if _, _, _, err := ConvertGeminiRequest(Chat, []byte(`{"model":"g",`+body+`}`)); err == nil {
			t.Fatal("unexpected success", body)
		}
	}
}

func TestGeminiCompatibilityReplyMaxTokens(t *testing.T) {
	const body = `{"responseId":"g1","candidates":[{"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":7,"thoughtsTokenCount":3,"totalTokenCount":10}}`
	r, err := ReadGeminiReply(strings.NewReader(body), false)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Usage.Known || !r.Usage.CachedKnown || r.Usage.Input != 7 || r.Usage.Cached != 0 || r.Usage.Output != 3 || r.Usage.Reasoning != 3 {
		t.Fatal(r.Usage)
	}
	for _, tc := range []struct {
		protocol Protocol
		ending   string
	}{{Chat, `"finish_reason":"length"`}, {Responses, "event: response.incomplete"}, {Messages, `"stop_reason":"max_tokens"`}} {
		raw, err := r.Stream(tc.protocol)
		if err != nil || !strings.Contains(string(raw), tc.ending) {
			t.Fatal(string(raw), err)
		}
	}
}

func TestGeminiCompatibilityReplyRejectsInvalid(t *testing.T) {
	for _, body := range []string{
		`{"promptFeedback":{"blockReason":"SAFETY"}}`,
		`{"candidates":[{"finishReason":"SAFETY"}]}`,
		`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]}},{"finishReason":"STOP"}]}`,
		`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]} }],"usageMetadata":{"promptTokenCount":2,"cachedContentTokenCount":3}}`,
		`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok"}]} }],"usageMetadata":{"candidatesTokenCount":-1}}`,
		`{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"ok","thoughtSignature":"sig"},{"functionCall":{"name":"lookup","args":{}}}]}}]}`,
	} {
		if _, err := ReadGeminiReply(strings.NewReader(body), false); err == nil {
			t.Fatal("unexpected success", body)
		}
	}
}

func TestGeminiCompatibilityStreamLimits(t *testing.T) {
	const final = "data: {\"candidates\":[{\"finishReason\":\"STOP\",\"content\":{\"parts\":[{\"text\":\"ok\"}]}}]}\n\n"
	largeLine := ":" + strings.Repeat("0", maxGeminiEventBytes/2) + "\n"
	for _, tc := range []struct {
		name string
		r    io.Reader
		want string
	}{
		{"large event (multiple short lines)", strings.NewReader(largeLine + largeLine + "\n" + final), "exceeds 2 MiB"},
		{"large total", io.MultiReader(strings.NewReader(strings.Repeat(largeLine+"\n", maxGeminiReplyBytes/(maxGeminiEventBytes/2)+1)), strings.NewReader(final)), "exceeds 16 MiB"},
		{"incomplete frame", strings.NewReader(strings.TrimSuffix(final, "\n")), "incomplete event"},
		{"changed ID", strings.NewReader(fmt.Sprintf("data: {\"responseId\":\"first\"}\n\ndata: {\"responseId\":\"changed\"}\n\n%s", final)), "changed response ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadGeminiReply(tc.r, true); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal(err)
			}
		})
	}
}

// A Codex Responses request carries fields Gemini has no place for: strict-mode
// tool schemas, parallel_tool_calls, reasoning, include, prompt_cache_key and
// store. The conversion drops the client-side hints and keeps the content.
func TestGeminiCompatibleCodexRequest(t *testing.T) {
	body := []byte(`{"model":"gemini-4-argon","stream":true,
		"instructions":"You are Codex.",
		"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[{"type":"function","name":"shell","parameters":{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","strict":false,"additionalProperties":false,"properties":{"cmd":{"type":"string"}},"required":["cmd"]}}],
		"tool_choice":"auto","parallel_tool_calls":false,"store":false,
		"include":["reasoning.encrypted_content"],"prompt_cache_key":"ck-1",
		"reasoning":{"effort":"medium","summary":"auto"},"max_output_tokens":32}`)
	wire, stream, _, err := ConvertGeminiRequest(Responses, body)
	if err != nil {
		t.Fatal(err)
	}
	if !stream {
		t.Fatal("stream flag lost")
	}
	for _, bad := range []string{"parallel_tool_calls", `"include"`, "prompt_cache_key", `"reasoning"`, `"store"`} {
		if strings.Contains(string(wire), bad) {
			t.Fatalf("%s survived conversion: %s", bad, wire)
		}
	}
	if !strings.Contains(string(wire), `"functionDeclarations"`) {
		t.Fatalf("tools lost: %s", wire)
	}
}

// Responses clients (Codex) send both top-level instructions and a leading
// developer message; both are instructions and must merge into one channel.
func TestGeminiCompatibleMergesInstructions(t *testing.T) {
	body := []byte(`{"model":"g","instructions":"You are an assistant.","input":[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"workspace context"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	wire, _, _, err := ConvertGeminiRequest(Responses, body)
	if err != nil {
		t.Fatal(err)
	}
	got := string(wire)
	if !strings.Contains(got, `"systemInstruction"`) || !strings.Contains(got, "workspace context") || !strings.Contains(got, "You are an assistant.") {
		t.Fatalf("instructions lost: %s", got)
	}
	// An instruction after the conversation started stays unsupported.
	bad := []byte(`{"model":"g","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]},{"type":"message","role":"developer","content":[{"type":"input_text","text":"late"}]}]}`)
	if _, _, _, err := ConvertGeminiRequest(Responses, bad); err == nil {
		t.Fatal("late instruction accepted")
	}
}

// A long generation cut off before the final event still carries real content;
// it is delivered as truncated rather than failing the request.
func TestGeminiCompatibleTruncatedStreamKeepsContent(t *testing.T) {
	stream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"<!doctype html>\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"<html>\"}]}}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2}}\n\n"
	r, err := ReadGeminiReply(strings.NewReader(stream), true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Stop != "length" {
		t.Fatalf("stop = %q", r.Stop)
	}
	if len(r.Parts) == 0 {
		t.Fatal("content lost")
	}
	// An empty stream still fails: there is nothing to deliver.
	if _, err := ReadGeminiReply(strings.NewReader(""), true); err == nil {
		t.Fatal("empty stream accepted")
	}
}

// A chat history that starts at the assistant is representable: measured
// 2026-11-24, Factory's generate route accepts contents whose first turn is
// model and continues from them. Rejecting it broke chat callers whose window
// begins mid-conversation (and Gemini clients whose first user turn was
// dropped by new-api's Gemini→OpenAI conversion).
func TestGeminiCompatibleLeadingAssistantTurn(t *testing.T) {
	raw, _, _, err := ConvertGeminiRequest(Chat, []byte(`{"model":"g","messages":[{"role":"assistant","content":"here is the summary"},{"role":"user","content":"continue"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `"contents":[{"parts":[{"text":"here is the summary"}],"role":"model"},{"parts":[{"text":"continue"}],"role":"user"}]`
	if !strings.Contains(string(raw), want) {
		t.Fatalf("leading assistant turn rewritten: %s", raw)
	}
	// The same holds for a leading assistant turn that carries tool calls: the
	// call stays model-side and its result lands in the user turn after it.
	raw, _, _, err = ConvertGeminiRequest(Chat, []byte(`{"model":"g","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"done"}]}`))
	if err != nil || !strings.Contains(string(raw), `"contents":[{"parts":[{"functionCall":{"args":{},"id":"c1","name":"read"}}],"role":"model"},{"parts":[{"functionResponse":{"id":"c1","name":"read","response":{"output":"done"}}}],"role":"user"}]`) {
		t.Fatalf("leading tool-call turn: %s %v", raw, err)
	}
	// A history that opens with a tool result still cannot be represented: the
	// call it answers is gone.
	if _, _, _, err := ConvertGeminiRequest(Chat, []byte(`{"model":"g","messages":[{"role":"tool","tool_call_id":"c1","content":"done"},{"role":"user","content":"go on"}]}`)); err == nil {
		t.Fatal("leading tool result accepted")
	}
	// Ending with the assistant stays rejected: the upstream refuses it.
	if _, _, _, err := ConvertGeminiRequest(Chat, []byte(`{"model":"g","messages":[{"role":"user","content":"write"},{"role":"assistant","content":"Sure, "}]}`)); err == nil {
		t.Fatal("trailing assistant accepted")
	}
}
