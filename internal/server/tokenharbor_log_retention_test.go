package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestTHLiveLogUsesConfiguredRetentionWithoutResettingCursor(t *testing.T) {
	s := newTHTestServer(t)
	if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.Logs.RetentionDays = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	hub := s.thLog()
	hub.append(thLogEntry{TS: time.Now().Add(-2 * 24 * time.Hour), Detail: "expired"})
	hub.append(thLogEntry{TS: time.Now(), Detail: "fresh"})
	req := httptest.NewRequest(http.MethodGet, "/api/tokenharbor/log?after=0", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	var out struct {
		Entries       []thLogEntry `json:"entries"`
		Seq           int64        `json:"seq"`
		RetentionDays int          `json:"retentionDays"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("live log query failed", err, w.Code)
	}
	if out.RetentionDays != 1 || out.Seq != 2 || len(out.Entries) != 1 || out.Entries[0].Detail != "fresh" {
		t.Fatal("live tail retained expired records or lost cursor")
	}
	entries, seq := hub.since(0)
	if len(entries) != 1 || seq != 2 {
		t.Fatal("expired live-tail memory was not cleared")
	}
}
