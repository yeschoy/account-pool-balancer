package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMistralProviderTraceIDReachesClientAndSummary(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, header := range []string{"Mistral-Correlation-Id", "X-Kong-Request-Id", "X-Request-Id"} {
			t.Run(fmt.Sprintf("%s/stream=%v", header, stream), func(t *testing.T) {
				e := newMistralEnv(t)
				e.setHandler(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set(header, "provider-trace-123")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cached_tokens\":0}}}\n\ndata: [DONE]\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":100,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":0}}}`)
				})
				req, _ := http.NewRequest(http.MethodPost, e.front.URL+"/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"m","messages":[],"stream":%v}`, stream)))
				req.Header.Set("Authorization", "Bearer mistral-client")
				req.Header.Set("Content-Type", "application/json")
				resp, err := e.front.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_, err = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 {
					t.Fatal("unexpected response", resp.StatusCode, err)
				}
				if resp.Header.Get("X-Upstream-Request-Id") != "provider-trace-123" {
					t.Errorf("provider trace header %s was lost downstream", header)
				}
				logs, _ := e.s.logs.Query("requests", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
				var rec requestLog
				if len(logs.Items) != 1 || json.Unmarshal(logs.Items[0], &rec) != nil {
					t.Fatal("missing request summary")
				}
				if rec.UpstreamRequestID != "provider-trace-123" {
					t.Errorf("provider trace header %s was lost from request summary", header)
				}
				if rec.CachedTokens == nil || *rec.CachedTokens != 0 {
					t.Fatal("trace extraction changed real zero cache usage")
				}
			})
		}
	}
}
