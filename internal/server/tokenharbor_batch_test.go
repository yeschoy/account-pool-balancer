package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHSelectedRegistrationRejectsDuplicateAndHonorsConcurrencyInput(t *testing.T) {
	s := newTHTestServer(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int64
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer up.Close()
	defer close(release)
	if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	store, _ := s.thStore()
	rows, err := store.AddSaved("batch", []thsignupstore.Row{{Email: "selected@mock.test", Password: "mock-only"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tokenharbor/registrations/" + rows[0].ID + "/retry"
	for _, body := range []string{`{"concurrency":-1}`, `{"concurrency":11}`} {
		if rec := postTH(t, s, path, body); rec.Code != 400 {
			t.Fatal("invalid concurrency accepted", rec.Code)
		}
		row, _ := store.Get(rows[0].ID)
		if row.Status != thsignupstore.Saved {
			t.Fatal("invalid request changed saved state")
		}
	}
	if rec := postTH(t, s, path, `{"concurrency":2,"solveCaptcha":false}`); rec.Code != 202 {
		t.Fatal("selected start failed", rec.Body.String())
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("registration did not start")
	}
	if rec := postTH(t, s, path, `{"concurrency":2}`); rec.Code != 409 {
		t.Fatal("duplicate running registration must be rejected", rec.Code)
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate started a second upstream request")
	}
}
