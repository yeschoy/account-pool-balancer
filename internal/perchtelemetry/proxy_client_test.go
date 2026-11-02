package perchtelemetry_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/perch"
	pt "account-pool-balancer/internal/perchtelemetry"
)

func TestCLITurnProxyFailuresDoNotFallback(t *testing.T) {
	var direct atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		direct.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()
	r := pt.New(pt.Settings{Egress: true}, nil, nil)
	r.SetHTTPClient(origin.Client())
	r.SetClock(nil, func(context.Context, time.Duration) error { return nil })
	turn := perch.CLITurn{Status: perch.TurnStatusComplete, RunID: "fixture-run"}
	for _, proxy := range []string{"://invalid", closedURL} {
		if err := r.CLITurnWithProxy(context.Background(), origin.URL, "fixture-token", turn, proxy); err == nil {
			t.Errorf("proxy %q: expected failure", proxy)
		}
	}
	if got := direct.Load(); got != 0 {
		t.Fatalf("fell back to origin %d times", got)
	}
}
