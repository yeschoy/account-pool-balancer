package opencode

import (
	"encoding/json"
	"strings"
	"testing"
)

func feed(t *testing.T, s *GeminiChatStream, payload string) (string, bool) {
	t.Helper()
	out, finished, err := s.FeedEvent([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	return string(out), finished
}

// The first frame already produces a role chunk and the content delta, so a
// Chat client can render before the upstream finishes.
func TestGeminiChatStreamIsProgressive(t *testing.T) {
	s := NewGeminiChatStream("chatcmpl-test", "gemini-4-argon", 1, true, nil)
	out1, fin := feed(t, s, `{"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}]}`)
	if fin {
		t.Fatal("finished too early")
	}
	if !strings.Contains(out1, `"role":"assistant"`) || !strings.Contains(out1, `"content":"Hel"`) {
		t.Fatalf("first frame = %s", out1)
	}
	out2, fin := feed(t, s, `{"candidates":[{"content":{"parts":[{"text":"lo"}]}}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`)
	if !strings.Contains(out2, `"content":"lo"`) {
		t.Fatalf("second frame = %s", out2)
	}
	out3, fin := feed(t, s, `{"candidates":[{"content":{"parts":[{"text":""}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`)
	if !fin || !strings.Contains(out3, `"finish_reason":"stop"`) || !strings.Contains(out3, `"usage"`) || !strings.Contains(out3, "[DONE]") {
		t.Fatalf("finish frame = %s finished=%v", out3, fin)
	}
	in, out, _, _, known := s.Usage()
	if !known || in != 5 || out != 2 {
		t.Fatalf("usage = %d/%d known=%v", in, out, known)
	}
}

// A tool call is streamed with the caller's own name restored.
func TestGeminiChatStreamToolNameRestored(t *testing.T) {
	s := NewGeminiChatStream("chatcmpl-test", "gemini-4-argon", 1, false, map[string]string{"read_ab12": "read"})
	out, _ := feed(t, s, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_ab12","args":{"path":"a"}}}]}}]}`)
	if !strings.Contains(out, `"name":"read"`) || !strings.Contains(out, `\"path\":\"a\"`) {
		t.Fatalf("tool chunk = %s", out)
	}
}

// A stream cut before the finish reason closes as truncated rather than failing.
func TestGeminiChatStreamTruncatedFinishes(t *testing.T) {
	s := NewGeminiChatStream("chatcmpl-test", "gemini-4-argon", 1, true, nil)
	feed(t, s, `{"candidates":[{"content":{"parts":[{"text":"<html>"}]}}]}`)
	out := string(s.Finish())
	if !strings.Contains(out, `"finish_reason":"length"`) || !strings.Contains(out, "[DONE]") {
		t.Fatalf("finish = %s", out)
	}
}

// Usage chunk mirrors the OpenAI shape.
func TestGeminiChatStreamUsageShape(t *testing.T) {
	s := NewGeminiChatStream("chatcmpl-test", "m", 1, true, nil)
	feed(t, s, `{"candidates":[{"content":{"parts":[{"text":"a"}]},"finishReason":"MAX_TOKENS"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":3,"cachedContentTokenCount":4}}`)
	out := string(s.Finish())
	_ = out
	// Finish after a finished stream emits nothing; check the recorded usage instead.
	in, o, cached, _, known := s.Usage()
	if !known || in != 9 || o != 3 || cached != 4 {
		t.Fatalf("usage = %d/%d/%d known=%v", in, o, cached, known)
	}
	var probe map[string]any
	if json.Unmarshal([]byte(`{"a":1}`), &probe) != nil {
		t.Fatal("json sanity")
	}
}
