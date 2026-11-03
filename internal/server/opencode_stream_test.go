package server

import (
	"fmt"
	"net/http"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

func TestOpenCodeConvertedStreamRetriesOnlyBeforeUpstreamContent(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			e := newOCEnv(t, true)
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if r.Header.Get("X-Api-Key") == "sk-zen-secret-key" {
					if after {
						fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"r\",\"model\":\"messages\",\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
					}
					fmt.Fprint(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"status\":429,\"type\":\"rate_limit_error\",\"message\":\"limited\"}}\n\n")
					return
				}
				fmt.Fprint(w, ocStream(opencode.Messages))
			}
			status, b, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/chat/completions", ocRequest(opencode.Chat, "messages", true), nil)
			if after {
				if status != 502 || e.count() != 1 {
					t.Fatalf("buffered output replayed %d %s calls=%d", status, b, e.count())
				}
			} else if status != 200 || e.count() != 2 {
				t.Fatalf("safe pre-content retry failed %d %s calls=%d", status, b, e.count())
			}
		})
	}
}
