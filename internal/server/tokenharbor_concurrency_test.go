package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHRegistrationBudgetsAreGlobalAndTaskScoped(t *testing.T) {
	r := &thRegistrar{srv: &Server{thStopping: make(chan struct{})}}
	one, err := r.acquire(context.Background(), "one", 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if release, err := r.acquire(ctx, "one", 5); err == nil {
		release()
		t.Fatal("cannot rebuild a live task to bypass its cap")
	}
	one()
	var releases []func()
	for i := 0; i < 5; i++ {
		release, err := r.acquire(context.Background(), fmt.Sprintf("task-%d", i), 5)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel2()
	if release, err := r.acquire(ctx2, "sixth", 5); err == nil {
		release()
		t.Fatal("tasks bypassed global five-slot cap")
	}
	for _, release := range releases {
		release()
	}
	if len(r.attempts) != 0 || len(r.sem) != 0 || cap(r.sem) != 5 {
		t.Fatal("cancelled wait leaked task/global slots")
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if release, err := r.acquire(cancelled, "cancelled", 5); err == nil {
		release()
		t.Fatal("cancelled admission accepted")
	}
	close(r.srv.thStopping)
	if release, err := r.acquire(context.Background(), "shutdown", 5); err == nil {
		release()
		t.Fatal("shutdown admission accepted")
	}
}

func TestTHRegistrationActuallyHonorsOneToFiveWorkers(t *testing.T) {
	for limit := 1; limit <= 5; limit++ {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			s := newTHTestServer(t)
			entered := make(chan struct{}, 10)
			release := make(chan struct{})
			var once sync.Once
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/login" {
					t.Error("unexpected mutation", r.Method, r.URL.Path)
				}
				entered <- struct{}{}
				<-release
				w.Header().Set("Retry-After", "600")
				w.WriteHeader(429)
			}))
			defer site.Close()
			defer once.Do(func() { close(release) })
			_, err := s.cfg.Update(-1, func(c *config.Config) error {
				c.TokenHarbor.BaseURL = site.URL + "/v1"
				c.TokenHarbor.RegisterConcurrency = limit
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			accounts := make([]map[string]string, limit+1)
			for i := range accounts {
				accounts[i] = map[string]string{"email": fmt.Sprintf("worker%d@mock.test", i), "password": "mock"}
			}
			requested := limit
			if limit == 3 {
				requested = 0
			} // zero must use the persisted default
			body, _ := json.Marshal(map[string]any{"accounts": accounts, "concurrency": requested})
			if rec := postTH(t, s, "/api/tokenharbor/registrations", string(body)); rec.Code != 202 {
				t.Fatal(rec.Code, rec.Body.String())
			}
			for i := 0; i < limit; i++ {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatalf("only %d of %d workers started", i, limit)
				}
			}
			select {
			case <-entered:
				t.Fatal("task exceeded selected concurrency")
			case <-time.After(25 * time.Millisecond):
			}
			once.Do(func() { close(release) })
			s.thTasks.Wait()
			if len(entered) != 1 {
				t.Fatal("queued row failed to continue or was replayed", len(entered))
			}
		})
	}
}

func TestTHConcurrencyInvalidRequestsDoNotWriteRowsOrSettings(t *testing.T) {
	s := newTHTestServer(t)
	store, _ := s.thStore()
	rows, _ := store.AddSaved("saved", []thsignupstore.Row{{Email: "saved@mock.test", Password: "mock"}})
	for _, n := range []string{"-1", "6", "10", "1.5"} {
		for path, body := range map[string]string{
			"/api/tokenharbor/registrations":                          `{"saveOnly":true,"concurrency":` + n + `,"accounts":[{"email":"new@mock.test","password":"mock"}]}`,
			"/api/tokenharbor/registrations/" + rows[0].ID + "/retry": `{"concurrency":` + n + `}`,
		} {
			if rec := postTH(t, s, path, body); rec.Code != 400 {
				t.Fatal("invalid concurrency accepted", n, rec.Code)
			}
		}
		before := s.cfg.Get().Revision
		if rec := putTH(t, s, "/api/tokenharbor/settings", `{"tokenHarbor":{"registerConcurrency":`+n+`}}`); rec.Code != 400 {
			t.Fatal("invalid settings concurrency accepted", n, rec.Code)
		}
		if s.cfg.Get().Revision != before || len(store.List()) != 1 {
			t.Fatal("invalid request mutated state")
		}
	}
}
