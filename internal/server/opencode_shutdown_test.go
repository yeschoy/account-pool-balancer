package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

func TestOpenCodeDrainCancelsRequestsBeforeClosingState(t *testing.T) {
	e := newOCEnv(t, false)
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"r\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}
	req, _ := http.NewRequest(http.MethodPost, e.front.URL+"/v1/chat/completions", strings.NewReader(ocRequest(opencode.Chat, "chat", true)))
	req.Header.Set("Authorization", "Bearer downstream-opencode-zen")
	resp, err := e.front.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data := make([]byte, 1)
	if _, err := resp.Body.Read(data); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { e.s.drainRequests(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel/drain request")
	}
	if e.s.pool.Views(e.s.cfg.Get())["zen1"].Active != 0 {
		t.Fatal("shutdown leaked lease")
	}
	before := e.count()
	status, body, _ := e.post(t, config.ProviderOpenCodeZen, "/v1/chat/completions", ocRequest(opencode.Chat, "chat", false), nil)
	if status != 503 || !strings.Contains(string(body), "server_draining") || e.count() != before {
		t.Fatalf("request admitted during drain: %d %s", status, body)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
}
