package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

func TestTokenHarborNoRedirectOrUncertainReplay(t *testing.T) {
	e := newTHEnv(t)
	var sinkCalls atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sinkCalls.Add(1) }))
	defer sink.Close()
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	})
	status, _, _ := e.post(t, "/v1/chat/completions", ocRequest(opencode.Chat, "m", false), nil)
	if status != 307 || sinkCalls.Load() != 0 || e.count() != 1 {
		t.Fatal("credential redirect or replay")
	}
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	})
	before := e.count()
	status, _, _ = e.post(t, "/v1/chat/completions", ocRequest(opencode.Chat, "m", false), nil)
	if status != 502 || e.count() != before+1 {
		t.Fatal("uncertain request retried")
	}
}

func TestTokenHarborCommittedStreamNotReplayed(t *testing.T) {
	e := newTHEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"failed after commit\",\"status\":503}}\n\n")
	})
	status, body, _ := e.post(t, "/v1/chat/completions", ocRequest(opencode.Chat, "m", true), nil)
	if status != 200 || !strings.Contains(body, "started") || e.count() != 1 {
		t.Fatal("committed stream replayed")
	}
}

func TestTokenHarborStatefulFailureDoesNotSwitch(t *testing.T) {
	e := newTHEnv(t)
	_, _, _ = e.post(t, "/v1/responses", ocRequest(opencode.Responses, "m", false), nil)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		fmt.Fprint(w, `{"error":{"message":"temporary"}}`)
	})
	before := e.count()
	status, _, _ := e.post(t, "/v1/responses", `{"model":"m","input":"next","previous_response_id":"resp-th"}`, nil)
	if status != 503 || e.count() != before+1 {
		t.Fatal("stateful failure switched account")
	}
}

func TestTokenHarborUnknownUsageStaysUnknown(t *testing.T) {
	e := newTHEnv(t)
	e.setHandler(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	})
	status, _, _ := e.post(t, "/v1/chat/completions", ocRequest(opencode.Chat, "m", false), nil)
	if status != 200 {
		t.Fatal(status)
	}
	logs, _ := e.s.logs.Query("requests", nil, time.Time{}, time.Time{}, 10, 0)
	if len(logs.Items) != 1 || !strings.Contains(string(logs.Items[0]), `"cachedTokens":null`) || !strings.Contains(string(logs.Items[0]), `"cacheHit":null`) {
		t.Fatal("unknown usage fabricated as zero")
	}
	if got := redactSecrets(config.Default(), "unconfigured thk_live_not-in-the-pool-secret"); strings.Contains(got, "thk_live_") {
		t.Fatal("unconfigured upstream key not redacted")
	}
}
