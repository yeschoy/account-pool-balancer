package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestPerchAccountProxyEgress(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("egress")
	e.importFiles(map[string][]byte{"egress.json": []byte(exportDoc("egress", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	id := e.ownAccountID()

	type hits struct {
		mu    sync.Mutex
		paths map[string]int
	}
	newProxy := func() (*httptest.Server, *hits) {
		h := &hits{paths: map[string]int{}}
		p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.mu.Lock()
			h.paths[r.URL.Path]++
			h.mu.Unlock()
			e.fake.handle(w, r)
		}))
		t.Cleanup(p.Close)
		return p, h
	}
	p1, h1 := newProxy()
	p2, h2 := newProxy()
	setProxy := func(proxy string) config.Account {
		t.Helper()
		_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
			c.Perch.AppURL = "http://perch.invalid"
			for i := range c.Accounts {
				if c.Accounts[i].ID == id {
					c.Accounts[i].ProxyURL = proxy
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		a, ok := e.srv.cfg.Get().Account(id)
		if !ok {
			t.Fatal("missing account")
		}
		return a
	}
	assertHit := func(h *hits, path string) {
		t.Helper()
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.paths[path] == 0 {
			t.Errorf("proxy did not receive %s: %v", path, h.paths)
		}
	}

	a := setProxy(p1.URL)
	if res := e.srv.perch.keeper.Refresh(context.Background(), id, true); res.Err != nil {
		t.Fatal(res.Err)
	}
	if got := e.srv.checkPerchAccount(context.Background(), a); got.Status != "ok" {
		t.Fatalf("check: %+v", got)
	}
	if _, err := e.srv.fetchPerchMeter(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if _, err := e.srv.fetchPerchModels(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/auth/v1/token", "/auth/v1/user", "/api/perchai/account", "/rest/v1/rpc/perch_ai_my_inference_usage_meter", "/api/perch-terminal/starter-limits", "/api/perchai/models"} {
		assertHit(h1, path)
	}

	setProxy(p2.URL)
	if res := e.srv.perch.keeper.Refresh(context.Background(), id, true); res.Err != nil {
		t.Fatal(res.Err)
	}
	assertHit(h2, "/auth/v1/token")
}
