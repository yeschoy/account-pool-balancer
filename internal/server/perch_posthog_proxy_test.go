package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perch"
	"account-pool-balancer/internal/perchdevice"
	"account-pool-balancer/internal/perchtelemetry"
)

func TestReportPerchTurnPostHogUsesAccountProxy(t *testing.T) {
	type observations struct {
		mu     sync.Mutex
		events []perch.PostHogEvent
	}
	newServer := func(o *observations) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != perch.PostHogPath {
				t.Errorf("path = %q", r.URL.Path)
			}
			var ev perch.PostHogEvent
			if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
				t.Errorf("decode event: %v", err)
			}
			o.mu.Lock()
			o.events = append(o.events, ev)
			o.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		}))
	}
	snapshot := func(o *observations) []perch.PostHogEvent {
		o.mu.Lock()
		defer o.mu.Unlock()
		return append([]perch.PostHogEvent(nil), o.events...)
	}
	var originHits, firstHits, secondHits observations
	origin := newServer(&originHits)
	defer origin.Close()
	first := newServer(&firstHits)
	defer first.Close()
	second := newServer(&secondHits)
	defer second.Close()
	closed := newServer(&observations{})
	closedURL := closed.URL
	closed.Close()

	dir := t.TempDir()
	store, err := config.Load(filepath.Join(dir, "fixture-config.json"))
	if err != nil {
		t.Fatal(err)
	}
	devices, err := perchdevice.Open(filepath.Join(dir, "fixture-device.json"), []byte("fixture-sealing-key"), nil)
	if err != nil {
		t.Fatal(err)
	}
	reporter := perchtelemetry.New(perchtelemetry.Settings{Egress: true, PostHogKey: "fixture-key", PostHogHost: origin.URL}, nil, nil)
	reporter.SetHTTPClient(origin.Client())
	s := &Server{cfg: store, perch: &perchRuntime{telemetry: reporter, devices: devices}}
	cfg := &config.Config{}
	cfg.Perch.PostHogKey = "fixture-key"
	rec := &requestLog{Result: "success"}
	res := &attemptResult{turnStarted: true, turnToken: "fixture-token", turn: perchTurn{Thread: "fixture-thread", RunID: "fixture-run"}}
	report := func(id, proxy string) {
		s.reportPerchTurn(cfg, config.Account{ID: id, ProxyURL: proxy}, rec, res, time.Now())
	}
	report("fixture-account-one", first.URL)
	report("fixture-account-two", second.URL)
	report("fixture-invalid", "://invalid")
	report("fixture-closed", closedURL)
	if got := len(snapshot(&originHits)); got != 0 {
		t.Fatalf("explicit proxy fell back to origin: %d events", got)
	}
	firstEvents, secondEvents := snapshot(&firstHits), snapshot(&secondHits)
	for _, tc := range []struct {
		id     string
		events []perch.PostHogEvent
	}{
		{"fixture-account-one", firstEvents},
		{"fixture-account-two", secondEvents},
	} {
		if len(tc.events) != 2 {
			t.Fatalf("%s: got %d events, want 2", tc.id, len(tc.events))
		}
		wantID := devices.Get(tc.id).MachineID
		if wantID == "" {
			t.Fatal("empty fixture machine id")
		}
		if tc.events[0].Event != perch.EventMessageSent || tc.events[1].Event != perch.EventFirstMessage {
			t.Fatalf("%s: unexpected events %+v", tc.id, tc.events)
		}
		for _, ev := range tc.events {
			if ev.DistinctID != wantID || ev.APIKey != "fixture-key" {
				t.Errorf("%s: unexpected event identity %+v", tc.id, ev)
			}
		}
	}
	if firstEvents[0].DistinctID == secondEvents[0].DistinctID {
		t.Fatal("distinct accounts shared a fixture machine id")
	}

	// The empty account proxy keeps the existing injected reporter client.
	report("fixture-default", "")
	if got := len(snapshot(&originHits)); got != 2 {
		t.Fatalf("unconfigured proxy: got %d origin events, want 2", got)
	}
}
