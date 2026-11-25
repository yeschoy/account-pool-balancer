package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMistralValidationDetailsBoundedAndPrivate(t *testing.T) {
	validation := `[{"type":"extra_forbidden","loc":["body","thinking"],"msg":"private-message","input":"` + strings.Repeat("private-prompt-", 10000) + `"}]`
	nested, _ := json.Marshal(map[string]any{"error": map[string]any{"message": validation}})
	twice, _ := json.Marshal(map[string]any{"error": map[string]any{"message": string(nested)}})
	for _, raw := range [][]byte{
		[]byte(`{"detail":` + validation + `}`),
		[]byte(`{"detail":` + validation + `}`)[:64<<10],
		nested[:64<<10],
		twice[:64<<10],
		[]byte(`{"error":{"message":"[{\"loc\":[\"body\",\"thinking\"],\"type\":\"extra_forbidden\",\"input\":\"truncated\\u00`),
	} {
		got := mistralValidationDetails(raw)
		b, _ := json.Marshal(got)
		if string(b) != `[{"loc":["body","thinking"],"type":"extra_forbidden"}]` {
			t.Fatalf("coordinates lost across envelope/truncation: %s", b)
		}
		for _, private := range []string{"private-message", "private-prompt", "truncated", "input"} {
			if strings.Contains(string(b), private) {
				t.Fatal("diagnostic retained echoed input/message")
			}
		}
	}
	for _, raw := range []string{
		`{"detail":[{"input":"unfinished`,
		`{"input":[{"loc":["body","thinking"],"type":"extra_forbidden"}]}`,
		`{"error":{"type":"internal_server_error","message":"Service unavailable"}}`,
		`{"detail":[{"type":"private-secret-type","loc":["body","thinking"]}]}`,
		`{"detail":[{"type":"extra_forbidden","loc":["body"],"loc":["thinking"]}]}`,
		`{"detail":[{"type":"extra_forbidden","loc":["body",-1]}]}`,
	} {
		if got := mistralValidationDetails([]byte(raw)); len(got) != 0 {
			t.Fatalf("invented validation evidence: %+v", got)
		}
	}
	got := mistralValidationDetails([]byte(`{"detail":[{"type":"literal_error","loc":["body","messages",12,"private-identity-key"],"input":"private-value","ctx":{"loc":["secret"],"type":"extra_forbidden"}}]}`))
	b, _ := json.Marshal(got)
	if string(b) != `[{"loc":["body","messages",12,"[other]"],"type":"literal_error"}]` {
		t.Fatalf("untrusted path was retained: %s", b)
	}
	many := `{"detail":[` + strings.TrimSuffix(strings.Repeat(`{"type":"extra_forbidden","loc":["body","thinking"]},`, 30), ",") + `]}`
	if len(mistralValidationDetails([]byte(many))) != 8 {
		t.Fatal("diagnostics were not count bounded")
	}
}

func TestMistralBadRequestReturnsUsableErrorWithoutEcho(t *testing.T) {
	const gifMessage = `Image is an animated GIF. Image URL: data:image/gif;base64,`
	const private = "synthetic-private-attachment"
	gifError := func(size int) string {
		return `{"object":"error","message":"` + gifMessage + private + strings.Repeat("A", size) + `","type":"invalid_request_error","code":"3310","raw_status_code":400}`
	}
	const imageLimit = `{"object":"error","message":"Total number of images exceeds the maximum allowed of 20.","code":"3051"}`
	const nested = `{"detail":{"message":"` + gifMessage + `not-a-top-level-error"}}`
	for _, tc := range []struct {
		name, wire, code, hint string
		status                 int
	}{
		{"oversized GIF", gifError(100 << 10), "animated_gif_unsupported", "static PNG or JPEG", 400},
		{"small GIF", gifError(4000), "animated_gif_unsupported", "conversation history", 400},
		{"unknown oversized error", `{"detail":{"message":"` + gifMessage + private + strings.Repeat("A", 100<<10) + `"}}`, "upstream_error_body_truncated", "request ID", 400},
		{"known distinct rejection", imageLimit, "", "", 400},
		{"nested content is not the error message", nested, "", "", 400},
		{"other status", gifError(20), "", "", 422},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.wire)
			})
			st, reply := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","stream":true,"messages":[{"role":"user","content":"describe the image"}]}`, nil)
			if st != tc.status || e.count() != 1 {
				t.Fatalf("wrong status or replay: %d, %d attempts", st, e.count())
			}
			if tc.code == "" {
				if reply != tc.wire {
					t.Fatal("unrelated complete error changed")
				}
				return
			}
			var envelope struct {
				Error struct{ Code, Message, Type string }
			}
			if json.Unmarshal([]byte(reply), &envelope) != nil || envelope.Error.Type != "invalid_request_error" || envelope.Error.Code != tc.code || !strings.Contains(envelope.Error.Message, tc.hint) || len(reply) > 500 || strings.Contains(reply, "data:image/") || strings.Contains(reply, private) || strings.Contains(strings.ToLower(reply), "mistral") {
				t.Fatal("missing parseable, bounded, actionable response or echoed attachment")
			}
			logs, _ := e.s.logs.Query("errors", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
			var row errorLog
			if len(logs.Items) != 1 || json.Unmarshal(logs.Items[0], &row) != nil || row.Reason != envelope.Error.Message || row.Status != st || row.UpstreamStatus == nil || *row.UpstreamStatus != st || row.ResponseBodyTruncated != (len(tc.wire) > 64<<10) || row.ResponseBytes != min(len(tc.wire), 64<<10) || row.HealthAction != "none" || row.RetryDecision != "stop" || row.Switched || strings.Contains(string(logs.Items[0]), private) || strings.Contains(string(logs.Items[0]), "data:image/") {
				t.Fatal("incorrect error diagnosis, retry/health decision, or attachment privacy")
			}
		})
	}
}

func TestMistralPublicFailuresHideProvider(t *testing.T) {
	const chunk = `{"choices":[{"delta":{"content":"The word Mistral can appear in legitimate model output."}}]}`
	const escapedError = `{"type":"error","error":{"message":"Mistr\u0061l failed (synthetic-upstream-diagnostic)","code":"provider_failure"}}`
	const req = `{"model":"deepseek-v4-1-flash-1017","stream":true,"messages":[{"role":"user","content":"hello"}]}`
	for _, tc := range []struct {
		name, path, request, upstream, contentType, want string
		upstreamStatus, status, calls                    int
	}{
		{"unsupported endpoint", "/v1/messages", req, "", "", "This API endpoint is not supported.", 0, 404, 0},
		{"local invalid model", "/v1/chat/completions", `{"messages":[]}`, "", "", "invalid_model", 0, 400, 0},
		{"native branded HTTP failure", "/v1/chat/completions", req, `{"error":{"message":"MiStRaL error: synthetic-upstream-diagnostic","code":3051}}`, "application/json", "upstream_request_rejected", 400, 400, 1},
		{"native escaped first error", "/v1/chat/completions", req, "event: error\ndata: " + strings.Replace(escapedError, `"type":"error"`, `"type":"error","status":400`, 1) + "\n\n", "text/event-stream", "upstream_request_rejected", 200, 400, 1},
		{"native escaped committed error", "/v1/chat/completions", req, "data: " + chunk + "\n\nevent: error\ndata: " + escapedError + "\n\n", "text/event-stream", "upstream_request_failed", 200, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newMistralEnv(t)
			e.setHandler(func(w http.ResponseWriter, _ *http.Request) {
				if tc.upstreamStatus == 0 {
					t.Error("local rejection contacted upstream")
					return
				}
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.upstreamStatus)
				fmt.Fprint(w, tc.upstream)
			})
			st, reply := e.post(t, tc.path, tc.request, nil)
			if tc.status == 200 {
				prefix := "data: " + chunk + "\n\n"
				if !strings.HasPrefix(reply, prefix) {
					t.Fatal("changed previously committed model content")
				}
				reply = strings.TrimPrefix(reply, prefix)
				if !strings.HasPrefix(reply, "event: error\ndata: ") || strings.Contains(reply, "[DONE]") {
					t.Fatal("lost the failure event or fabricated successful termination")
				}
			}
			if st != tc.status || e.count() != tc.calls || !strings.Contains(reply, tc.want) || strings.Contains(strings.ToLower(reply), "mistral") || strings.Contains(reply, "synthetic-upstream-diagnostic") {
				t.Fatalf("unexpected status/attempts or unsanitized failure: %d attempts=%d reply=%s", st, e.count(), reply)
			}
			if tc.upstreamStatus == 400 {
				logs, _ := e.s.logs.Query("errors", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
				if len(logs.Items) != 1 || !strings.Contains(string(logs.Items[0]), "synthetic-upstream-diagnostic") {
					t.Fatal("client-facing sanitization lost the admin diagnostic")
				}
			}
		})
	}
}

func TestMistralOversizedValidationErrorNoReplayOrPromptLog(t *testing.T) {
	for _, size := range []int{64 << 10, 100 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			e := newMistralEnv(t)
			prefix := `{"detail":[{"type":"extra_forbidden","loc":["body","thinking"],"input":"`
			suffix := `"}]}`
			wire := prefix + strings.Repeat("p", size-len(prefix)-len(suffix)) + suffix
			if size > 64<<10 {
				wire = prefix + "private-prompt-secret" + wire[len(prefix)+len("private-prompt-secret"):]
			}
			e.setHandler(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(422)
				fmt.Fprint(w, wire)
			})
			st, _ := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","messages":[]}`, nil)
			if st != 422 || e.count() != 1 {
				t.Fatalf("diagnostic changed status/replayed request: status=%d attempts=%d", st, e.count())
			}
			logs, _ := e.s.logs.Query("errors", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
			if len(logs.Items) != 1 {
				t.Fatal("missing validation error record")
			}
			var record errorLog
			if json.Unmarshal(logs.Items[0], &record) != nil || len(record.MistralValidation) != 1 || record.MistralValidation[0].Type != "extra_forbidden" || record.ResponseBytes != 64<<10 || record.ResponseBodyTruncated != (size > 64<<10) {
				t.Fatalf("incorrect bounded diagnostics: %s", logs.Items[0])
			}
			if record.HealthAction != "none" || record.RetryDecision != "stop" || record.Switched || strings.Contains(string(logs.Items[0]), "private-prompt-secret") {
				t.Fatal("validation error penalized/replayed account or exposed input")
			}
		})
	}
}
