package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

func seedTHImported(t *testing.T, s *Server) thsignupstore.Row {
	t.Helper()
	store, err := s.thStore()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.Add("that_return", []thsignupstore.Row{{Email: "return@mock.test", Password: "secret-password", APIKey: "thk_live_return_mock_only", MailboxURL: "https://mida.vip/messages/mock?token=mail-secret", Group: config.TokenHarborGroup}})
	if err != nil {
		t.Fatal(err)
	}
	row, err := store.Update(rows[0].ID, func(r *thsignupstore.Row) error {
		r.Status, r.Account, r.Verified = thsignupstore.Imported, "acc_return", true
		r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "secret-session"}}
		r.Checkout = &thsignupstore.Checkout{URL: "https://checkout.stripe.com/c/pay/mock_secret"}
		r.Subscription = thregister.Subscription{Status: "active", Tier: "plus", CheckedAt: time.Now()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.cfg.Update(-1, func(c *config.Config) error {
		c.Accounts = append(c.Accounts, config.Account{ID: row.Account, Provider: config.ProviderTokenHarbor, Name: row.Email, Email: row.Email, RuntimeKey: row.APIKey, Group: config.TokenHarborGroup, Enabled: false})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestTHReturnPreservesCredentialsAndRequiresAdminCSRF(t *testing.T) {
	s := newTHTestServer(t)
	before := seedTHImported(t, s)
	path := "/api/accounts/" + before.Account + "/return-to-registration"
	unauth := httptest.NewRecorder()
	s.Handler().ServeHTTP(unauth, httptest.NewRequest("POST", path, nil))
	if unauth.Code != 401 {
		t.Fatal("unauthenticated return accepted", unauth.Code)
	}
	req := httptest.NewRequest("POST", path, nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
	noCSRF := httptest.NewRecorder()
	s.Handler().ServeHTTP(noCSRF, req)
	if noCSRF.Code != 403 {
		t.Fatal("return without CSRF accepted", noCSRF.Code)
	}
	if len(s.cfg.Get().Accounts) != 1 {
		t.Fatal("unauthorized request changed account")
	}

	rec := postTH(t, s, path, `{}`)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("return failed", rec.Code, rec.Body.String())
	}
	if len(s.cfg.Get().Accounts) != 0 {
		t.Fatal("returned account remains schedulable")
	}
	store, _ := s.thStore()
	after, _ := store.Get(before.ID)
	if after.Status != thsignupstore.Registered || after.Account != "" {
		t.Fatal("registration not restored")
	}
	if after.APIKey != before.APIKey || after.Password != before.Password || after.MailboxURL != before.MailboxURL || after.ProxyURL != before.ProxyURL || after.ProxyBound != before.ProxyBound || !reflect.DeepEqual(after.Cookies, before.Cookies) || !reflect.DeepEqual(after.Checkout, before.Checkout) || !reflect.DeepEqual(after.Subscription, before.Subscription) {
		t.Fatal("return changed saved credentials, egress or plan observation")
	}
	for _, secret := range []string{before.APIKey, before.Password, before.MailboxURL, before.Cookies[0].Value, before.Checkout.URL} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("return response leaked credentials")
		}
	}
	var summary thsignupstore.Summary
	if err := json.Unmarshal(rec.Body.Bytes(), &summary); err != nil || summary.Attempt != before.Attempt {
		t.Fatal("missing original task")
	}
	reopened, err := thsignupstore.Open(filepath.Join(s.dataDir, "tokenharbor-signup.json"), s.secret)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := reopened.Get(before.ID)
	if persisted.Status != thsignupstore.Registered || persisted.APIKey != before.APIKey {
		t.Fatal("return did not persist")
	}
}

func TestTHReturnRejectsMissingOrChangedAssociation(t *testing.T) {
	for _, mode := range []string{"manual", "changed-key", "wrong-provider", "busy", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			s := newTHTestServer(t)
			row := seedTHImported(t, s)
			store, _ := s.thStore()
			switch mode {
			case "manual":
				if err := store.Delete(row.ID); err != nil {
					t.Fatal(err)
				}
			case "changed-key", "wrong-provider":
				_, err := s.cfg.Update(-1, func(c *config.Config) error {
					if mode == "changed-key" {
						c.Accounts[0].RuntimeKey = "thk_live_replacement_mock_only"
					} else {
						c.Accounts[0].Provider = config.ProviderOllama
						c.Accounts[0].Group = config.GroupForProvider(config.ProviderOllama)
						c.Accounts[0].RuntimeKey = "ollama_fake_only"
					}
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
			case "busy":
				if _, err := store.Update(row.ID, func(r *thsignupstore.Row) error { r.Status = thsignupstore.Running; return nil }); err != nil {
					t.Fatal(err)
				}
			case "ambiguous":
				other, err := store.Add("that_other", []thsignupstore.Row{{Email: "other@mock.test"}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.Update(other[0].ID, func(r *thsignupstore.Row) error {
					r.Account = row.Account
					r.Status = thsignupstore.Imported
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			before := s.cfg.Get()
			if _, err := s.thReturn(row.Account); err == nil {
				t.Fatal("invalid association accepted")
			}
			if s.cfg.Get() != before {
				t.Fatal("rejected return changed the config")
			}
		})
	}
}

func TestTHReturnPartialSaveCanFinishWithoutRecreatingPoolAccount(t *testing.T) {
	s := newTHTestServer(t)
	row := seedTHImported(t, s)
	file := filepath.Join(s.dataDir, "tokenharbor-signup.json")
	if err := os.Rename(file, file+".before"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.thReturn(row.Account); err != errTHReturnIncomplete {
		t.Fatal("missing partial-outcome warning", err)
	}
	if len(s.cfg.Get().Accounts) != 0 {
		t.Fatal("failed row save restored schedulable account")
	}
	store, _ := s.thStore()
	pending, _ := store.Get(row.ID)
	if pending.Status != thsignupstore.Imported || pending.Account != row.Account || pending.APIKey != row.APIKey {
		t.Fatal("retry association or credentials lost")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file+".before", file); err != nil {
		t.Fatal(err)
	}
	if _, err := s.thReturn(row.Account); err != nil {
		t.Fatal("cannot finish partial return", err)
	}
	if len(s.cfg.Get().Accounts) != 0 {
		t.Fatal("retry recreated a pool account")
	}
}

func TestTHReturnRejectsLateImportCheck(t *testing.T) {
	s := newTHTestServer(t)
	row := seedTHImported(t, s)
	started, release := make(chan struct{}), make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.Write([]byte(thPricingFixture("plus")))
	}))
	defer up.Close()
	if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	imported := make(chan error, 1)
	go func() { imported <- s.thImport(context.Background(), row.ID, "") }()
	<-started
	returned := make(chan error, 1)
	go func() { _, err := s.thReturn(row.Account); returned <- err }()
	select {
	case err := <-returned:
		if err != nil {
			close(release)
			<-imported
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(release)
		<-imported
		<-returned
		t.Fatal("upstream check held the local admission lock")
	}
	close(release)
	if err := <-imported; err == nil {
		t.Fatal("late check reimported the returned account")
	}
	if len(s.cfg.Get().Accounts) != 0 {
		t.Fatal("returned account silently reappeared in the pool")
	}
}
