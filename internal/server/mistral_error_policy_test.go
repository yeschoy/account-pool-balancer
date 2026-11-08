package server

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestMistralServiceErrorStopsWithoutAccountPenalty(t *testing.T) {
	e := newMistralEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		fmt.Fprint(w, `{"code":3800,"type":"internal_server_error","message":"Service unavailable"}`)
	})
	st, reply := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","messages":[{"role":"user","content":"mock"}]}`, nil)
	if st != 503 || e.count() != 1 {
		t.Fatalf("service error retried on another account: %d %s calls=%d", st, reply, e.count())
	}
	for _, v := range e.s.pool.Views(e.s.cfg.Get()) {
		if v.Samples != 0 || v.Status != "ok" {
			t.Fatal("upstream service outage penalized a healthy API key")
		}
	}
	logs, _ := e.s.logs.Query("errors", map[string]string{"provider": "mistral"}, time.Time{}, time.Time{}, 10, 0)
	if len(logs.Items) != 1 {
		t.Fatal("missing auditable service-error decision")
	}
}
