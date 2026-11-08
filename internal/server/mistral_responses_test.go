package server

import (
	"fmt"
	"net/http"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestMistralResponsesTerminalKinds(t *testing.T) {
	for _, tc := range []struct {
		event, data string
		want        bool
	}{
		{"response.completed", `{"type":"response.completed","response":{"output":[]}}`, true},
		{"response.incomplete", `{"response":{"output":[]}}`, true},
		{"", `{"type":"response.completed"}`, true},
		{"message", `{"type":"response.incomplete"}`, true},
		{"", `{"type":"\u0072esponse.completed"}`, true},
		{"response.completed", `null`, false},
		{"response.completed", `{"type":null}`, false},
		{"response.completed", `{"type":""}`, false},
		{"", `{"Type":"response.completed"}`, false},
		{"response.completed", `{`, false},
		{"response.completed", `{"type":"response.output_text.delta"}`, false},
		{"", `{"type":"response.output_text.delta","delta":"response.completed"}`, false},
		{"", `{"delta":{"type":"response.completed"}}`, false},
		{"", `{"type":"response.completed","type":"response.created"}`, false},
		{"", `[DONE]`, false},
		{"message_stop", `{}`, false},
	} {
		if got := mistralResponsesTerminal(tc.event, []byte(tc.data)); got != tc.want {
			t.Errorf("event=%q data=%q: got %v want %v", tc.event, tc.data, got, tc.want)
		}
	}
}

func TestMistralResponsesEOFCheckDoesNotChangeOtherPaths(t *testing.T) {
	t.Run("Mistral Chat", func(t *testing.T) {
		e := newMistralEnv(t)
		wire := "data: {\"choices\":[{\"delta\":{\"content\":\"legacy\"}}]}\n\n"
		e.setHandler(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, wire)
		})
		if st, body := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[],"stream":true}`, nil); st != 200 || body != wire || mistralTerminalRequestLog(t, e).Result != "success" {
			t.Fatal("Mistral Responses termination policy escaped to Chat")
		}
	})
	t.Run("other pool Responses", func(t *testing.T) {
		e := newEnv(t, []config.Account{{ID: "native-other"}}, nil)
		wire := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"
		e.up.handle = func(w http.ResponseWriter, r *http.Request, key string, body map[string]any) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, wire)
		}
		resp := e.post("/v1/responses", map[string]any{"model": "m", "input": "mock", "stream": true}, nil)
		if body := readAll(resp); body != wire {
			t.Fatal("Mistral termination policy escaped to another pool")
		}
		logs := e.requestLogs()
		if len(logs) != 1 || logs[0]["result"] != "success" {
			t.Fatal("other pool legacy EOF policy changed")
		}
	})
}
