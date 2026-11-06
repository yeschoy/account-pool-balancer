package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/egressid"
	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHVerifyFirstPendingCanExplicitlyResendWithoutCreatingKey(t *testing.T) {
	s := newTHTestServer(t)
	store, _ := s.thStore()
	rows, _ := store.AddSaved("verify-first", []thsignupstore.Row{{Email: "pending@mock.test", Password: "original", MailboxURL: "https://mail.mock.test/"}})
	row, err := store.Update(rows[0].ID, func(r *thsignupstore.Row) error {
		r.CompletionOrder = thregister.VerifyThenKey
		r.Status = thsignupstore.Failed
		r.Cookies = []thsignupstore.Cookie{{Name: "sb-auth-auth-token", Value: "mock-session"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/me/send-verification-email" {
			t.Error("resend must not create key/register", r.Method, r.URL.Path)
		}
		if r.UserAgent() != egressid.ForProfile(row.ID, row.BrowserProfile).UserAgent {
			t.Error("saved profile not restored for manual send")
		}
		latest, _ := store.Get(row.ID)
		if !latest.VerificationMailAttempted {
			t.Error("manual send marker missing before request")
		}
		fmt.Fprint(w, `{"ok":true,"alreadyVerified":true}`)
	}))
	defer site.Close()
	_, err = s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = site.URL + "/v1"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	rec := postTH(t, s, "/api/tokenharbor/registrations/"+row.ID+"/resend-verification", `{}`)
	if rec.Code != 200 || calls != 1 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	got, _ := store.Get(row.ID)
	if got.APIKey != "" || got.Status != thsignupstore.Failed || !got.Verified || got.CompletionOrder != thregister.VerifyThenKey || got.BrowserProfile != row.BrowserProfile {
		t.Fatal("manual send changed completion order or claimed key completion")
	}
	stored := thStoredResult(got)
	var copied = got
	thSaveResult(&copied, stored)
	if copied.CompletionOrder != got.CompletionOrder || copied.VerificationMailAttempted != got.VerificationMailAttempted {
		t.Fatal("completion metadata lost at service/store boundary")
	}
}
