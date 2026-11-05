package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHRegistrationBudgetCannotBeRebuiltByLegacyConcurrency(t *testing.T) {
	r := &thRegistrar{}
	one := r.budget(2)
	if one != r.budget(10) || one != r.budget(0) || cap(one) != 1 {
		t.Fatal("all tasks must share exactly one immutable slot")
	}
}

func TestTHSignupGateHasMinuteFloorAndCancellation(t *testing.T) {
	s := &Server{thStopping: make(chan struct{})}
	r := &thRegistrar{srv: s}
	before := time.Now()
	if err := r.awaitSignup(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if r.nextSignup.Before(before.Add(time.Minute)) {
		t.Fatal("actual submission reservation must impose the 60-second floor")
	}
	reserved := r.nextSignup
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.awaitSignup(ctx, true); !errors.Is(err, context.Canceled) || r.nextSignup != reserved {
		t.Fatal("cancelled wait must not reserve another signup")
	}
	close(s.thStopping)
	if err := r.awaitSignup(context.Background(), false); err == nil || r.nextSignup != reserved {
		t.Fatal("shutdown must cancel waiting before site/captcha work")
	}
}

func TestTHPauseDoesNotReviveOldQueueAfterExpiry(t *testing.T) {
	r := &thRegistrar{}
	old := r.generation()
	r.pause(time.Hour)
	if r.checkPaused(old, true) == nil {
		t.Fatal("active pause must stop old work")
	}
	r.mu.Lock()
	r.pausedUntil = time.Now().Add(-time.Second)
	r.mu.Unlock()
	if r.checkPaused(old, true) == nil || r.checkPaused(r.generation(), true) != nil {
		t.Fatal("expiry permits only newly requested work, never the old queued job")
	}
}

func TestTHRateLimitStopsOtherQueuedRowsWithoutSiteRequests(t *testing.T) {
	s := newTHTestServer(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var posts, requests atomic.Int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/login":
			if r.Method == http.MethodGet {
				fmt.Fprint(w, `<input name="$ACTION_1:0" value="1234567890123456789012345678901234567890"><input name="$ACTION_KEY" value="key">`)
			} else {
				posts.Add(1)
				close(started)
				<-release
				w.Header().Set("Retry-After", "7200")
				fmt.Fprint(w, "1:{\"error\":\"We're receiving an unusually high number of sign-ups right now.\"}\n")
			}
		case "/api/auth/signup-precheck":
			fmt.Fprint(w, `{"needCaptcha":false}`)
		default:
			t.Error("unexpected keys, captcha, inference or privacy endpoint", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer site.Close()
	defer once.Do(func() { close(release) })
	if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = site.URL + "/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	store, _ := s.thStore()
	rows, err := store.Add("paced", []thsignupstore.Row{{Email: "first@mock.test", Password: "original"}, {Email: "queued@mock.test", Password: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	r := s.thRegistrar()
	done := make(chan error, 2)
	go func() { done <- r.run(rows[0].ID, false, 10) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first registration never submitted")
	}
	go func() { done <- r.run(rows[1].ID, false, 2) }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		row, _ := store.Get(rows[1].ID)
		if row.Status == thsignupstore.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second task did not enter the shared queue")
		}
		time.Sleep(time.Millisecond)
	}
	once.Do(func() { close(release) })
	for range 2 {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("both rejected and queued work must stop")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("paused queue must finish without waiting/retrying")
		}
	}
	if posts.Load() != 1 || requests.Load() != 3 || time.Until(r.pausedUntil) < 2*time.Hour-time.Second {
		t.Fatal("rate refusal must preserve the full pause and stop all subsequent site requests")
	}
	for _, original := range rows {
		row, _ := store.Get(original.ID)
		if row.Status != thsignupstore.Failed || !strings.Contains(row.Detail, "注册暂停") || row.Password != "original" || row.APIKey != "" {
			t.Fatal("paused rows must retain credentials and an actionable reason, not fabricate a key")
		}
	}
}

func TestTHPausedAPIsDoNotMutateRowsButSaveOnlyRemainsAvailable(t *testing.T) {
	s := newTHTestServer(t)
	store, _ := s.thStore()
	rows, _ := store.AddSaved("saved", []thsignupstore.Row{{Email: "saved@mock.test", Password: "original"}})
	s.thRegistrar().pause(time.Hour)
	for path, body := range map[string]string{
		"/api/tokenharbor/registrations/" + rows[0].ID + "/retry": `{"concurrency":2}`,
		"/api/tokenharbor/registrations":                          `{"accounts":[{"email":"new@mock.test","password":"original"}]}`,
	} {
		rec := postTH(t, s, path, body)
		if rec.Code != 429 || rec.Header().Get("Retry-After") == "" || len(store.List()) != 1 {
			t.Fatal("paused admin request must return Retry-After before changing state", rec.Code)
		}
		row, _ := store.Get(rows[0].ID)
		if row.Status != thsignupstore.Saved || row.Password != "original" {
			t.Fatal("pause rejection mutated the saved row")
		}
	}
	rec := postTH(t, s, "/api/tokenharbor/registrations", `{"saveOnly":true,"accounts":[{"email":"save@mock.test","password":"original"}]}`)
	if rec.Code != 201 || len(store.List()) != 2 {
		t.Fatal("save-only must remain usable without starting any external work")
	}
}
