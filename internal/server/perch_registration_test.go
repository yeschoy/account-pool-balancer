package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

func TestRegistrationActivationGatesImport(t *testing.T) {
	e := newPerchOpenEnv(t)
	doc := exportDocAsMap("registered")
	doc["supabase_session"] = doc["session"]
	raw, _ := json.Marshal(doc)
	m := &e.srv.perchRegistrations
	m.worker = func(_ context.Context, in registrationInput) ([]byte, error) { return raw, nil }
	m.activate = func(_ context.Context, proxy string, doc []byte) error { return errors.New("failed with secret") }
	m.jobs = map[string]*registrationJob{"job": {ID: "job", Status: "running", Accounts: []registrationRow{{Login: "user"}}}}
	m.running = true
	e.srv.runPerchRegistration("job", []registrationInput{{Login: "user", Password: "secret"}}, true, "perch")
	row := m.jobs["job"].Accounts[0]
	if row.Status != "failed" || row.CanImport || e.count("registered") != 0 {
		t.Fatalf("activation failure must not import: %+v", row)
	}
	encoded, _ := json.Marshal(m.jobs["job"])
	if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "access-registered") {
		t.Fatal("job exposed credentials")
	}

	m.activate = func(context.Context, string, []byte) error { return nil }
	m.jobs["job"] = &registrationJob{ID: "job", Status: "running", Accounts: []registrationRow{{Login: "user"}}}
	e.srv.runPerchRegistration("job", []registrationInput{{Login: "user"}}, false, "perch")
	if !m.jobs["job"].Accounts[0].CanImport || e.count("registered") != 0 {
		t.Fatal("activated account should await selection")
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"logins":["user"],"group":"perch"}`))
	req.SetPathValue("id", "job")
	rec := httptest.NewRecorder()
	e.srv.apiPerchRegistrationImport(rec, req)
	if rec.Code != 200 || e.count("registered") != 1 {
		t.Fatalf("import failed: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "access-registered") || strings.Contains(rec.Body.String(), "refresh-registered") {
		t.Fatal("response leaked session")
	}
	if m.jobs["job"].Accounts[0].CanImport || m.jobs["job"].Accounts[0].doc != nil {
		t.Fatal("imported session retained")
	}
}

func TestRegistrationImportSchedulesQuotaWhenPollingDisabled(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(fmt.Sprint(auto), func(t *testing.T) {
			e := newPerchOpenEnv(t)
			_, err := e.srv.cfg.Update(-1, func(c *config.Config) error { c.Quota.Enabled = false; return nil })
			if err != nil {
				t.Fatal(err)
			}
			fetched := make(chan string, 1)
			e.srv.quota.PerchFetch = func(_ context.Context, a config.Account) (*quota.PerchMeter, error) {
				fetched <- a.ID
				return &quota.PerchMeter{Plan: "Starter"}, nil
			}
			e.srv.quota.PerchModels = nil
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go e.srv.quota.Run(ctx)
			doc := exportDocAsMap("registered-sync")
			doc["supabase_session"] = doc["session"]
			raw, _ := json.Marshal(doc)
			m := &e.srv.perchRegistrations
			m.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, nil }
			m.activate = func(context.Context, string, []byte) error { return nil }
			m.jobs = map[string]*registrationJob{"job": {ID: "job", Status: "running", Accounts: []registrationRow{{Login: "user"}}}}
			e.srv.runPerchRegistration("job", []registrationInput{{Login: "user"}}, auto, "perch")
			if !auto {
				req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"logins":["user"]}`))
				req.SetPathValue("id", "job")
				rec := httptest.NewRecorder()
				e.srv.apiPerchRegistrationImport(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
				}
			}
			select {
			case id := <-fetched:
				if id != m.jobs["job"].Accounts[0].AccountID {
					t.Fatalf("wrong account: %s", id)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("quota read not triggered")
			}
		})
	}
}

func TestRegistrationAutoImport(t *testing.T) {
	e := newPerchOpenEnv(t)
	doc := exportDocAsMap("auto")
	doc["supabase_session"] = doc["session"]
	raw, _ := json.Marshal(doc)
	m := &e.srv.perchRegistrations
	m.worker = func(context.Context, registrationInput) ([]byte, error) { return raw, nil }
	m.activate = func(context.Context, string, []byte) error { return nil }
	m.jobs = map[string]*registrationJob{"job": {ID: "job", Status: "running", Accounts: []registrationRow{{Login: "user", proxyURL: "socks5://example:secret@proxy.example:1080"}}}}
	e.srv.runPerchRegistration("job", []registrationInput{{Login: "user", ProxyURL: "socks5://example:secret@proxy.example:1080"}}, true, "special")
	if e.count("auto") != 1 || m.jobs["job"].Accounts[0].Status != "imported" {
		t.Fatal("auto import failed")
	}
	for _, a := range e.srv.cfg.Get().Accounts {
		if a.PerchUserID == "auto" && (a.Group != "special" || a.ProxyURL != "socks5://example:secret@proxy.example:1080") {
			t.Fatal("wrong group or proxy")
		}
	}
}
