package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
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

// Includes routing, real identity, preparation and server-side layout replay,
// without network or an upstream model. The generated legacy thinking/tool
// histories are synthetic; matched records are captured from native frames.
func BenchmarkMistralHistoryPipeline(b *testing.B) {
	const model, path = "deepseek-v4-1-flash-1017", "/v1/chat/completions"
	for _, kib := range []int{128, 512, 2048} {
		b.Run(fmt.Sprintf("%dKiB", kib), func(b *testing.B) {
			cfg := config.Default()
			s := &Server{secret: []byte("benchmark-only"), mistralLayouts: newMistralLayouts([]byte("benchmark-only"))}
			ck := config.ClientKey{ID: "synthetic", Group: config.MistralGroup}
			const identityScope = "mistral\x00synthetic"
			h := http.Header{"Session-Id": {"benchmark-session"}}
			body := fmt.Appendf(nil, `{"model":%q,"messages":[{"role":"system","content":"synthetic system"}`, model)
			const turns = 32
			part := strings.Repeat("abcdefgh0123456\n", kib*1024/(turns*64))
			for i := range turns {
				thought := fmt.Sprintf("thought %d %s", i, part)
				answer := fmt.Sprintf("answer %d %s", i, part)
				body = fmt.Appendf(body, `,{"role":"user","content":%q},{"role":"assistant","content":%q,"reasoning_content":%q,"tool_calls":[{"id":"c","function":{"name":"mock","arguments":"{\"n\":9007199254740993}"}}]},{"role":"tool","tool_call_id":"c","content":%q}`, part, answer, thought, part)
			}
			body = append(body, ']', '}')
			obj, err := parseRoutingBodyWithHistory(body, true, false)
			if err != nil {
				b.Fatal(err)
			}
			scope := s.mistralLayouts.scope(cfg, ck, s.mistralIdentity(cfg, identityScope, h, obj), model, path)
			for i := 0; i < turns; i += 2 {
				thought := fmt.Sprintf("thought %d %s", i, part)
				answer := fmt.Sprintf("answer %d %s", i, part)
				txt, _ := json.Marshal(answer)
				why, _ := json.Marshal(thought)
				frame := fmt.Appendf(nil, `{"choices":[{"index":0,"message":{"content":[{"type":"text","text":%s},{"type":"thinking","thinking":[{"type":"text","text":%s}]}]},"finish_reason":"stop"}]}`, txt, why)
				s.mistralLayouts.capture(scope, &mistralLayoutLog{}).observe(frame, false)
			}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				obj, err := parseRoutingBodyWithHistory(body, true, false)
				if err != nil {
					b.Fatal(err)
				}
				ident := s.mistralIdentity(cfg, identityScope, h, obj)
				key, _ := mistralRequestCacheKey(cfg, h, ident)
				prepared, _, err := prepareMistralRequest(body, path, key)
				if err != nil {
					b.Fatal(err)
				}
				var report mistralLayoutLog
				_, err = s.mistralLayouts.restore(body, prepared, s.mistralLayouts.scope(cfg, ck, ident, model, path), &report)
				if err != nil || report.Restored != turns/2 || report.Fallback != turns/2 {
					b.Fatalf("unexpected layout coverage: %+v (%v)", report, err)
				}
			}
		})
	}
}

func BenchmarkMistralTimingOutputProbe(b *testing.B) {
	data := []byte(`{"choices":[{"index":0,"delta":{"content":"subsequent text"},"finish_reason":null}],"usage":null}`)
	for _, pending := range []mistralOutput{mistralTool | mistralThinking | mistralText, mistralTool | mistralThinking} {
		b.Run(fmt.Sprint(pending), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = mistralOutputKind("/v1/chat/completions", "", data, pending)
			}
		})
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
