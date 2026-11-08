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
