package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perch"
	"account-pool-balancer/internal/perchtelemetry"
)

func TestReportPerchTurnUsesAccountProxy(t *testing.T) {
	var originHits, proxyOneHits, proxyTwoHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	newProxy := func(hits *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			if r.URL.String() != origin.URL+perch.CLITurnPath {
				t.Errorf("proxy target = %q", r.URL.String())
			}
			if got := r.Header.Get("Authorization"); got != "Bearer fixture-token" {
				t.Errorf("authorization = %q", got)
			}
			var turn perch.CLITurn
			if err := json.NewDecoder(r.Body).Decode(&turn); err != nil || turn.Prompt != "fixture-prompt" {
				t.Errorf("turn = %+v, err = %v", turn, err)
			}
			w.WriteHeader(http.StatusNoContent)
		}))
	}
	proxyOne := newProxy(&proxyOneHits)
	defer proxyOne.Close()
	proxyTwo := newProxy(&proxyTwoHits)
	defer proxyTwo.Close()
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	reporter := perchtelemetry.New(perchtelemetry.Settings{Egress: true}, nil, nil)
	reporter.SetHTTPClient(origin.Client())
	reporter.SetClock(nil, func(context.Context, time.Duration) error { return nil })
	s := &Server{perch: &perchRuntime{telemetry: reporter}}
	cfg := &config.Config{}
	cfg.Perch.CLITurnReport = true
	cfg.Perch.AppURL = origin.URL
	rec := &requestLog{Result: "success"}
	res := &attemptResult{turnStarted: true, turnToken: "fixture-token", turn: perchTurn{Thread: "fixture-thread", RunID: "fixture-run", Prompt: "fixture-prompt"}}
	report := func(proxyURL string) {
		t.Helper()
		s.reportPerchTurn(cfg, config.Account{ID: "fixture-account", ProxyURL: proxyURL}, rec, res, time.Now())
	}

	withAuth, err := url.Parse(proxyOne.URL)
	if err != nil {
		t.Fatal(err)
	}
	withAuth.User = url.UserPassword("fixture-user", "fixture-password")
	report(withAuth.String())
	report(proxyTwo.URL)
	report("://invalid")
	report(closedURL)
	if got := originHits.Load(); got != 0 {
		t.Fatalf("explicit proxy fell back to origin: %d requests", got)
	}
	if proxyOneHits.Load() != 1 || proxyTwoHits.Load() != 1 {
		t.Fatalf("proxy hits = %d/%d, want 1/1", proxyOneHits.Load(), proxyTwoHits.Load())
	}

	// A failure before a model attempt must not emit through any transport.
	res.turnStarted = false
	report(proxyOne.URL)
	if proxyOneHits.Load() != 1 || originHits.Load() != 0 {
		t.Fatal("an unstarted model attempt emitted telemetry")
	}
	res.turnStarted = true
	res.turnToken = ""
	report("")
	if originHits.Load() != 0 {
		t.Fatal("an attempt without a token emitted telemetry")
	}
	res.turnToken = "fixture-token"
	// An unconfigured account keeps the reporter's existing injected client.
	report("")
	if got := originHits.Load(); got != 1 {
		t.Fatalf("unconfigured proxy: origin hits = %d, want 1", got)
	}
}
