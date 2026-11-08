package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMistralDeepSeekUserImageCompatibility(t *testing.T) {
	e := newMistralEnv(t)
	for _, tc := range []struct{ name, image, want string }{
		{"url object", `{"url":"https://example.invalid/image.png"}`, `"https://example.invalid/image.png"`},
		{"data url object", `{"url":"data:image/png;base64,AAAA"}`, `"data:image/png;base64,AAAA"`},
		{"empty NewAPI MIME", `{"MimeType":"","url":"data:image/png;base64,AAAA"}`, `"data:image/png;base64,AAAA"`},
		{"null NewAPI MIME", `{"url":"https://example.invalid/image.png","MimeType":null}`, `"https://example.invalid/image.png"`},
		{"escaped NewAPI MIME", `{"url":"https://example.invalid/image.png","Mime\u0054ype":""}`, `"https://example.invalid/image.png"`},
		{"empty MIME preserves detail", `{"MimeType":"","url":"https://example.invalid/image.png","detail":"high"}`, `{"url":"https://example.invalid/image.png","detail":"high"}`},
		{"nonempty MIME preserved", `{"url":"https://example.invalid/image.png","MimeType":"image/png"}`, `{"url":"https://example.invalid/image.png","MimeType":"image/png"}`},
		{"native string", `"https://example.invalid/image.png"`, `"https://example.invalid/image.png"`},
		{"extra options preserved", `{"url":"https://example.invalid/image.png","detail":"high"}`, `{"url":"https://example.invalid/image.png","detail":"high"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			history := strings.Repeat(`{"role":"user","content":"earlier"},`, 28)
			body := []byte(`{ "model":"deepseek-v4-1-flash-1017", "thinking":{"type":"disabled"}, "reasoning_effort":"max", "messages":[` + history + `{"role":"user","content":[{"type":"text","text":"question"},{"image_url":` + tc.image + `,"type":"image_url"},{"type":"text","text":"tail","n":9007199254740993}]}], "tools":[{"n":9007199254740993}], "metadata":{"session_id":"keep"}, "prompt_cache_key":"keep" }`)
			out, key, err := prepareMistralRequest(body, "/v1/chat/completions", "scope")
			var original, got map[string]json.RawMessage
			if err != nil || json.Unmarshal(out, &got) != nil || key != "" || string(got["reasoning_effort"]) != `"high"` || got["thinking"] != nil {
				t.Fatalf("image/explicit effort preparation failed: %s %v", out, err)
			}
			_ = json.Unmarshal(body, &original)
			for _, field := range []string{"model", "tools", "metadata", "prompt_cache_key"} {
				if !bytes.Equal(got[field], original[field]) {
					t.Fatalf("unrelated wire field changed: %s", field)
				}
			}
			var before, after []map[string]json.RawMessage
			_ = json.Unmarshal(original["messages"], &before)
			_ = json.Unmarshal(got["messages"], &after)
			if len(after) != 29 {
				t.Fatal("message history was truncated")
			}
			for i := 0; i < 28; i++ {
				if !bytes.Equal(before[i]["content"], after[i]["content"]) {
					t.Fatal("earlier history changed")
				}
			}
			var chunks []map[string]json.RawMessage
			_ = json.Unmarshal(after[28]["content"], &chunks)
			if len(chunks) != 3 || string(chunks[1]["image_url"]) != tc.want || string(chunks[0]["text"]) != `"question"` || string(chunks[2]["n"]) != "9007199254740993" {
				t.Fatal("image URL, chunk order, text or exact numbers changed")
			}
			count := e.count()
			st, reply := e.post(t, "/v1/chat/completions", string(body), nil)
			if st != 200 || e.count() != count+1 || e.call(count).body != string(out) {
				t.Fatalf("mock did not receive exactly one preserved request: %d %s", st, reply)
			}
		})
	}
}

func TestMistralUserImageCompatibilityScope(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"other-model", "/v1/chat/completions"},
		{"deepseek-v4-1-flash-1017", "/v1/responses"},
		{"deepseek-v4-1-flash-1017", "/v1/fim/completions"},
	} {
		body := []byte(` {"model":"` + tc.model + `","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/image.png","MimeType":""}}]}],"prompt_cache_key":"keep"} `)
		out, key, err := prepareMistralRequest(body, tc.path, "scope")
		if err != nil || key != "" || !bytes.Equal(out, body) {
			t.Fatalf("image conversion escaped exact DeepSeek Chat scope: %s %s", tc.model, tc.path)
		}
	}
}
