package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"account-pool-balancer/internal/config"
)

func TestFactoryResponsesWebSocket(t *testing.T) {
	e := newFactoryEnv(t)
	got := make(chan struct {
		path, auth, body string
		h                http.Header
	}, 1)
	up := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		var first string
		if err := websocket.Message.Receive(ws, &first); err != nil {
			return
		}
		got <- struct {
			path, auth, body string
			h                http.Header
		}{ws.Request().URL.Path, ws.Request().Header.Get("Authorization"), first, ws.Request().Header.Clone()}
		_ = websocket.Message.Send(ws, `{"type":"response.completed","response":{"id":"resp-ws"}}`)
		<-time.After(200 * time.Millisecond)
	}))
	defer up.Close()
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Factory.InferenceURL = up.URL; return nil }); err != nil {
		t.Fatal(err)
	}
	bal := httptest.NewServer(e.s.Handler())
	defer bal.Close()
	u, _ := url.Parse(bal.URL)
	u.Scheme = "ws"
	u.Path = "/v1/responses/ws"
	ws, err := websocket.DialConfig(&websocket.Config{Location: u, Origin: &url.URL{Scheme: "http", Host: u.Host}, Version: websocket.ProtocolVersionHybi13, Header: http.Header{"Authorization": {"Bearer downstream-ms"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	if err := websocket.Message.Send(ws, `{"type":"response.create","model":"gpt-6.2-sol","input":[]}`); err != nil {
		t.Fatal(err)
	}
	var out string
	if err := websocket.Message.Receive(ws, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "response.completed") {
		t.Fatal(out)
	}
	select {
	case c := <-got:
		if c.path != "/api/llm/o/v1/responses/ws" || c.auth != "Bearer fk-mock_factory_key_1" || !strings.Contains(c.body, `"model":"gpt-6.2-sol"`) || c.h.Get("X-Api-Provider") != "openai" || c.h.Get("X-Factory-Client") != "cli" {
			t.Fatalf("ws upstream: path=%s auth=%s body=%s h=%v", c.path, c.auth, c.body, c.h)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upstream frame not received")
	}
}
