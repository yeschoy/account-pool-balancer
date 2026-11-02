package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestRegistrationProxyChanges(t *testing.T) {
	s := &Server{}
	s.perchRegistrations.jobs = map[string]*registrationJob{"job": {ID: "job", Accounts: []registrationRow{
		{Login: "waiting", Status: "queued", proxyURL: "socks5://old:secret@host.example:1080", Proxy: "socks5://host.example:1080"},
		{Login: "active", Status: "activating"},
		{Login: "ready", Status: "activated", CanImport: true},
		{Login: "imported", Status: "imported"},
	}}}
	post := func(login, proxy string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"login": login, "proxyUrl": proxy})
		r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		r.SetPathValue("id", "job")
		w := httptest.NewRecorder()
		s.apiPerchRegistrationProxy(w, r)
		return w
	}
	w := post("waiting", "socks5h://user:secret@new.example:1080")
	if w.Code != 200 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "user") {
		t.Fatalf("unexpected response %d %s", w.Code, w.Body.String())
	}
	row := s.perchRegistrations.jobs["job"].Accounts[0]
	if row.proxyURL != "socks5://user:secret@new.example:1080" || row.Proxy != "socks5://new.example:1080" {
		t.Fatal("queued proxy not updated")
	}
	if w = post("active", ""); w.Code != 409 {
		t.Fatalf("running account accepted change: %d", w.Code)
	}
	if w = post("imported", ""); w.Code != 409 {
		t.Fatalf("imported account accepted change: %d", w.Code)
	}
	if w = post("ready", ""); w.Code != 200 {
		t.Fatalf("ready account rejected direct: %d", w.Code)
	}
	if w = post("waiting", "socks5://user:secret@host.example:1080/?secret=leak"); w.Code != 400 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("invalid proxy accepted or exposed")
	}
}

func TestRegistrationImportedProxyUsesCurrentAccount(t *testing.T) {
	e := newPerchOpenEnv(t)
	doc := exportDocAsMap("proxy-view")
	raw, _ := json.Marshal(doc)
	row := registrationRow{Login: "user", Status: "activated", CanImport: true, doc: raw, proxyURL: "socks5://first:secret@old.example:1080"}
	e.srv.importRegistered(&row, "perch")
	if row.AccountID == "" {
		t.Fatal("account not imported")
	}
	_, err := e.srv.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == row.AccountID {
				c.Accounts[i].ProxyURL = "socks5://second:secret@new.example:1080"
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	view := e.srv.registrationView(&registrationJob{Accounts: []registrationRow{row}})
	if got := view.Accounts[0].Proxy; got != "socks5://new.example:1080" {
		t.Fatalf("stale proxy label %q", got)
	}
}

func TestRegistrationQueuedProxyUsedByWorker(t *testing.T) {
	e := newPerchOpenEnv(t)
	m := &e.srv.perchRegistrations
	entered, release := make(chan struct{}), make(chan struct{})
	seen := make(chan string, 2)
	m.worker = func(_ context.Context, a registrationInput) ([]byte, error) {
		seen <- a.ProxyURL
		if a.Login == "first" {
			close(entered)
			<-release
		}
		return nil, context.Canceled
	}
	m.jobs = map[string]*registrationJob{"job": {ID: "job", Status: "running", Accounts: []registrationRow{{Login: "first", Status: "queued"}, {Login: "second", Status: "queued", proxyURL: "socks5://old:1080"}}}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.runPerchRegistration("job", []registrationInput{{Login: "first"}, {Login: "second"}}, false, "perch")
	}()
	<-entered
	r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"login":"second","proxyUrl":"socks5://new:1080"}`))
	r.SetPathValue("id", "job")
	w := httptest.NewRecorder()
	e.srv.apiPerchRegistrationProxy(w, r)
	if w.Code != 200 {
		t.Fatalf("update failed: %d", w.Code)
	}
	close(release)
	<-done
	<-seen
	if got := <-seen; got != "socks5://new:1080" {
		t.Fatalf("worker used wrong proxy: %q", got)
	}
}
