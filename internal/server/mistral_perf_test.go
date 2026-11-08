package server

import (
	"strings"
	"testing"
)

func BenchmarkMistralNativeChatEvent(b *testing.B) {
	event := []byte("data: {\"id\":\"reply\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"native text\"},\"finish_reason\":null}],\"usage\":null}\n\n")
	b.ReportAllocs()
	b.SetBytes(int64(len(event)))
	for b.Loop() {
		if _, err := mistralChatEvent(event); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMistralLongHistory(b *testing.B) {
	body := []byte(`{"model":"deepseek-v4-1-flash-1017","reasoning_effort":"high","prompt_cache_key":"keep","messages":[` + strings.Repeat(`{"role":"user","content":[{"type":"text","text":"`+strings.Repeat("x", 1024)+`"}]},`, 127) + `{"role":"assistant","content":"answer"}]}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		if _, _, err := prepareMistralRequest(body, "/v1/chat/completions", "scope"); err != nil {
			b.Fatal(err)
		}
	}
}
