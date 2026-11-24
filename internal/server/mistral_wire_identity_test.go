package server

import (
	"bytes"
	"encoding/json"
	"testing"
)

// legacyMistralReasoningContent reproduces the byte construction the pool used
// before the request splice was hand-assembled: encoding/json map marshals for
// the native chunk wrappers and an array marshal for the content list.
func legacyMistralReasoningContent(t *testing.T, text, content string) []byte {
	t.Helper()
	thinking, err := json.Marshal(map[string]any{
		"type":     "thinking",
		"thinking": []map[string]string{{"type": "text", "text": text}},
	})
	if err != nil {
		t.Fatal(err)
	}
	chunks := []json.RawMessage{thinking}
	if content != "" {
		chunk, err := json.Marshal(map[string]string{"type": "text", "text": content})
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
	}
	out, err := json.Marshal(chunks)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestMistralAssistantReasoningWireBytesUnchanged pins the optimized splice to
// the exact bytes the previous map/array encoder produced. A difference here
// would change the upstream prompt prefix (and therefore cache behavior).
func TestMistralAssistantReasoningWireBytesUnchanged(t *testing.T) {
	cases := []struct{ text, content string }{
		{"plain thought", "plain answer"},
		{"中文思考，含标点。", "中文回答"},
		{"<b>&</b>", "<script>"},
		{"line\nbreak\ttab", "answer\r\n"},
		{`quote " backslash \ slash /`, `"`},
		{"emoji 😀🙂", "answer emoji 🚀"},
		{"\u2028separator\u2029", "text"},
		{"", "answer without thought"},
	}
	for _, tc := range cases {
		body, err := json.Marshal(map[string]any{
			"model":    "mistral-large-4-0",
			"messages": []any{map[string]any{"role": "assistant", "reasoning_content": tc.text, "content": tc.content}},
		})
		if err != nil {
			t.Fatal(err)
		}
		prepared, _, err := prepareMistralRequest(body, "/v1/chat/completions", "")
		if err != nil {
			t.Fatalf("%q: %v", tc.text, err)
		}
		root := scanMistralObject(prepared)
		messages := root.get("messages")
		message := scanMistralObject(messages[skipSpace(messages, 1):skipValue(messages, skipSpace(messages, 1))])
		if message.get("reasoning_content") != nil {
			t.Fatalf("%q: reasoning_content was not consumed", tc.text)
		}
		got := message.get("content")
		want := legacyMistralReasoningContent(t, tc.text, tc.content)
		if tc.text == "" {
			encoded, _ := json.Marshal(tc.content)
			want = encoded
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("content bytes changed for %q:\n got %s\nwant %s", tc.text, got, want)
		}
	}
}

func TestMistralAssistantReasoningSpliceContentShapes(t *testing.T) {
	const before = ` {"model":"deepseek-v4-1-flash-1017", "reasoning_effort":"high","prompt_cache_key":"keep", "messages":[ {"role":"user","content":"hello"}, {"role":"assistant", "reasoning_content":"\u003cwhy\u003e\n\u2028",`
	const after = `"tool_calls":[{"id":"call","type":"function","function":{"name":"mock","arguments":"{\"n\":9007199254740993}"}}]}, {"role":"tool","content":"<keep>","tool_call_id":"call"} ]} `
	const wantBefore = ` {"model":"deepseek-v4-1-flash-1017", "reasoning_effort":"high","prompt_cache_key":"keep", "messages":[ {"role":"user","content":"hello"}, {"role":"assistant", `
	const thought = `{"thinking":[{"text":"\u003cwhy\u003e\n\u2028","type":"text"}],"type":"thinking"}`
	for _, tc := range []struct{ name, content, expected string }{
		{"null", ` "content":null,`, thought},
		{"empty string", ` "content":"",`, thought + `,{"text":"","type":"text"}`},
		{"scalar", ` "content":"<answer> 😀",`, thought + `,{"text":"\u003canswer\u003e 😀","type":"text"}`},
		{"existing array", ` "content":[ { "type":"text", "text":"<answer>" } ],`, thought + `,{"type":"text","text":"\u003canswer\u003e"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(before + tc.content + after)
			want := wantBefore + `"content":[` + tc.expected + `],` + after
			got, _, err := prepareMistralRequest(raw, "/v1/chat/completions", "unused")
			if err != nil || string(got) != want || string(raw) != before+tc.content+after {
				t.Fatalf("splice changed request bytes outside the established transform:\n got %s\nwant %s\nerr %v", got, want, err)
			}
		})
	}
}

func TestMistralUnchangedHistoryKeepsInputSlice(t *testing.T) {
	raw := []byte(` {"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high","prompt_cache_key":"keep","messages":[ {"role":"user","content":[{"type":"text","text":"hello"}]}, {"role":"assistant","content":"done"} ]} `)
	got, _, err := prepareMistralRequest(raw, "/v1/chat/completions", "unused")
	if err != nil || !bytes.Equal(got, raw) || &got[0] != &raw[0] {
		t.Fatal("native history must be returned without a body copy")
	}
}

// TestMistralLayoutRenderWireBytesUnchanged pins the layout replay splice to
// the established encoding: raw text chunks wrapped as before, joined by the
// same array encoder.
func TestMistralLayoutRenderWireBytesUnchanged(t *testing.T) {
	record := mistralLayoutRecord{Blocks: []mistralLayoutBlock{
		{Kind: "text", Bytes: 5},
		{Kind: "thinking", Parts: []int{3, 2}, Closed: 2, Signature: 2},
		{Kind: "text", Bytes: 3},
	}}
	got, err := record.render("he<&>ABC", "abcde")
	if err != nil {
		t.Fatal(err)
	}
	text := func(s string) []byte {
		quoted, _ := json.Marshal(s)
		return append(append([]byte(`{"type":"text","text":`), quoted...), '}')
	}
	thoughts, _ := json.Marshal([]json.RawMessage{text("abc"), text("de")})
	thinking := append([]byte(`{"type":"thinking","thinking":`), thoughts...)
	thinking = append(thinking, `,"closed":true,"signature":""}`...)
	want, err := json.Marshal([]json.RawMessage{text("he<&>"), thinking, text("ABC")})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("replayed content bytes changed:\n got %s\nwant %s", got, want)
	}
}

// legacyLayoutPlain is the map-decoding layout parser the wire view replaced.
// It stays here as the reference the optimized parser must match exactly.
func legacyLayoutPlain(raw []byte) (string, string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", "", true
	}
	if raw[0] == '"' {
		var s string
		err := json.Unmarshal(raw, &s)
		return s, "", err == nil
	}
	var chunks []json.RawMessage
	if json.Unmarshal(raw, &chunks) != nil {
		return "", "", false
	}
	var a, r bytes.Buffer
	for _, rawChunk := range chunks {
		view := scanMistralObject(rawChunk)
		if view == nil || view.unique("type", "text", "thinking", "closed", "signature") != nil {
			return "", "", false
		}
		var c map[string]json.RawMessage
		if json.Unmarshal(rawChunk, &c) != nil {
			return "", "", false
		}
		if mistralString(c["type"]) == "thinking" {
			var parts []json.RawMessage
			if json.Unmarshal(c["thinking"], &parts) != nil {
				return "", "", false
			}
			for _, rawPart := range parts {
				p := scanMistralObject(rawPart)
				if p == nil || p.unique("type", "text") != nil {
					return "", "", false
				}
			}
		}
		switch mistralString(c["type"]) {
		case "text":
			text, err := mistralTextChunk(c)
			if err != nil {
				return "", "", false
			}
			a.WriteString(text)
		case "thinking":
			parts, err := mistralThinkingTextParts(c)
			if err != nil {
				return "", "", false
			}
			for _, text := range parts {
				r.WriteString(text)
			}
		default:
			return "", "", false
		}
	}
	return a.String(), r.String(), true
}

// TestMistralLayoutPlainMatchesResponseParser locks the wire-view layout parser
// to the response adapter's map parser, including duplicate keys, unknown keys
// and unusable shapes.
func TestMistralLayoutPlainMatchesResponseParser(t *testing.T) {
	samples := []string{
		`null`,
		`"plain answer"`,
		`""`,
		`[]`,
		`[{"type":"text","text":"answer"}]`,
		`[{"type":"thinking","thinking":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"closed":true},{"type":"text","text":"c"}]`,
		`[{"type":"text","text":""},{"type":"thinking","thinking":[{"type":"text","text":""},{"type":"text","text":"\u4e2d\n"}]},{"type":"text","text":" first "},{"type":"thinking","thinking":[{"type":"text","text":" second "},{"type":"text","text":" third "}]},{"type":"text","text":"last"}]`,
		`[{"type":"thinking","thinking":[{"type":"text","text":"t"}],"closed":null,"signature":null}]`,
		`[{"type":"thinking","thinking":[{"type":"text","text":"t"}],"signature":""}]`,
		`[{"type":"thinking","thinking":[{"type":"text","text":"t"}],"signature":"abc"}]`,
		`[{"type":"thinking","text":"not-thinking"}]`,
		`[{"type":"thinking","thinking":null}]`,
		`[{"type":"thinking","thinking":[{"type":"text","text":"t","extra":1}]}]`,
		`[{"type":"text","text":null}]`,
		`[{"type":"text"}]`,
		`[{"type":"text","text":"a","text":"b"}]`,
		`[{"type":"text","text":"a","extra":true}]`,
		`[{"type":"image"}]`,
		`[3]`,
		`["string"]`,
		`not json`,
		`{"type":"text","text":"object not array"}`,
	}
	for _, sample := range samples {
		gotA, gotT, gotOK := mistralLayoutPlain([]byte(sample))
		wantA, wantT, wantOK := legacyLayoutPlain([]byte(sample))
		if gotA != wantA || gotT != wantT || gotOK != wantOK {
			t.Fatalf("%s: view parser diverged: (%q,%q,%v) vs (%q,%q,%v)", sample, gotA, gotT, gotOK, wantA, wantT, wantOK)
		}
	}
	for _, tc := range []struct {
		sample string
		want   bool
	}{
		{`[]`, false},
		{`null`, false},
		{`{"type":"thinking"}`, false},
		{`[{"type":"text","text":"a"}]`, false},
		{`[{"type":"thinking"}]`, true},
		{`["x"]`, false},
		{`[{"type":"text","text":"a"},{"type":"thinking","thinking":[]}]`, true},
		{`[{"type":"Thinking"}]`, false},
	} {
		if got := mistralHasNativeThinking([]byte(tc.sample)); got != tc.want {
			t.Fatalf("native thinking detection for %s: got %v want %v", tc.sample, got, tc.want)
		}
	}
}
