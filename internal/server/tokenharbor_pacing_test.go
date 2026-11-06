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

func TestTHSignupCheckHonorsCancellationWithoutReservingDelay(t *testing.T) {
	s := &Server{thStopping: make(chan struct{})}
	r := &thRegistrar{srv: s}
	for range 2 {
		if err := r.checkSignup(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.checkSignup(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled work must stop before signup")
	}
	close(s.thStopping)
	if err := r.checkSignup(context.Background()); err == nil {
		t.Fatal("shutdown must stop site/captcha work and submission")
	}
}

func TestTHRateLimitOnlyFailsCurrentRow(t *testing.T) {
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
				switch posts.Add(1) {
				case 1:
					close(started)
					<-release
					w.Header().Set("Retry-After", "7200")
					fmt.Fprint(w, "1:{\"error\":\"We're receiving an unusually high number of sign-ups right now.\"}\n")
				case 2:
					select {
					case <-release:
					default:
						t.Error("the next account started before the single worker was released")
					}
					fmt.Fprint(w, "1:{\"error\":\"Account registration refused\"}\n")
				default:
					t.Error("registration must not automatically retry either row")
					w.WriteHeader(500)
				}
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
	s.thTasks.Add(1)
	go func() {
		defer s.thTasks.Done()
		done <- r.run(rows[0].ID, false, 1)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first registration never submitted")
	}
	s.thTasks.Add(1)
	go func() {
		defer s.thTasks.Done()
		done <- r.run(rows[1].ID, false, 1)
	}()
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
				t.Fatal("each row must report its own upstream rejection")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("the queued row must not wait for a local signup interval or another account's Retry-After")
		}
	}
	if posts.Load() != 2 || requests.Load() != 6 {
		t.Fatalf("both rows must reach signup exactly once: posts=%d requests=%d", posts.Load(), requests.Load())
	}
	for i, original := range rows {
		row, _ := store.Get(original.ID)
		want := "当前账号注册受限"
		if i == 1 {
			want = "Account registration refused"
		}
		if row.Status != thsignupstore.Failed || !strings.Contains(row.Detail, want) || row.Password != "original" || row.APIKey != "" {
			t.Fatal("each failed row must retain its credentials and own upstream reason", row.Detail)
		}
	}
}

func TestTHRateLimitDoesNotBlockRegisterOrRetryAPIs(t *testing.T) {
	s := newTHTestServer(t)
	var requests atomic.Int64
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/login" {
			t.Error("unexpected signup/captcha/key request", r.Method, r.URL.Path)
		}
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer site.Close()
	t.Cleanup(s.thTasks.Wait)
	if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = site.URL + "/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	store, _ := s.thStore()
	limited, err := store.Add("limited", []thsignupstore.Row{{Email: "limited@mock.test", Password: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.AddSaved("saved", []thsignupstore.Row{{Email: "saved@mock.test", Password: "original"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.thRegistrar().run(limited[0].ID, false, 1); err == nil {
		t.Fatal("the first row must report the upstream rate limit")
	}
	for path, body := range map[string]string{
		"/api/tokenharbor/registrations/" + rows[0].ID + "/retry": `{"concurrency":2}`,
		"/api/tokenharbor/registrations":                          `{"accounts":[{"email":"new@mock.test","password":"original"}]}`,
	} {
		rec := postTH(t, s, path, body)
		if rec.Code != http.StatusAccepted || rec.Header().Get("Retry-After") != "" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("another row's rate limit must not reject explicit register/retry requests", rec.Code, rec.Body.String())
		}
	}
	s.thTasks.Wait()
	if requests.Load() != 3 || len(store.List()) != 3 {
		t.Fatal("each explicitly requested row must make its own attempt, without automatic replay")
	}
	for _, row := range store.List() {
		if row.Status != thsignupstore.Failed || !strings.Contains(row.Detail, "当前账号注册受限") {
			t.Fatal("only the row's own upstream response may fail its attempt", row.Detail)
		}
	}
	rec := postTH(t, s, "/api/tokenharbor/registrations", `{"saveOnly":true,"accounts":[{"email":"save@mock.test","password":"original"}]}`)
	if rec.Code != http.StatusCreated || len(store.List()) != 4 || requests.Load() != 3 {
		t.Fatal("save-only must remain usable without starting any external work")
	}
}
