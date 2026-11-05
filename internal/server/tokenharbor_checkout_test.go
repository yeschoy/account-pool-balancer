package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

const testAgentBody = `{"kind":"pass","product":"starter_pass_monthly"}`

func TestTHCheckoutPersistsAndDoesNotLeakOrDuplicate(t *testing.T) {
	const link = "https://checkout.stripe.com/c/pay/cs_test_private#secret"
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Write([]byte(`{"quote":{"kind":"tier","fromTier":"free","oneTime":true,"period":"monthly","planName":"Agent Pass","payNowUsd":0.99}}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"checkout_url": link})
	}))
	defer up.Close()
	s := newTHTestServer(t)
	_, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	store, _ := s.thStore()
	rows, _ := store.Add("test", []thsignupstore.Row{{Email: "pay@example.com", Password: "password-secret"}})
	id := rows[0].ID
	_, err = store.Update(id, func(r *thsignupstore.Row) error {
		r.Status = thsignupstore.Registered
		r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "cookie-secret"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tokenharbor/registrations/" + id + "/checkout"
	for range 2 {
		rec := postTH(t, s, path, testAgentBody)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), link) {
			t.Fatalf("checkout returned %d: %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("payment links must not be cached")
		}
	}
	if calls != 2 {
		t.Fatalf("same selection repeated external creation: %d", calls)
	}
	row, _ := store.Get(id)
	if row.Checkout == nil || row.Checkout.URL != link || row.Status != thsignupstore.Registered {
		t.Fatal("link/status not persisted")
	}
	b, _ := json.Marshal(store.List())
	for _, secret := range []string{link, "cs_test_private", "cookie-secret", "password-secret"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("summary leaked credential")
		}
	}
	if !strings.Contains(string(b), `"hasCheckout":true`) {
		t.Fatal("summary must advertise saved link without URL")
	}
	// Different choices must not overwrite an already-created payment session.
	if rec := postTH(t, s, path, `{"kind":"topup","product":"value"}`); rec.Code != 400 {
		t.Fatal("must not replace an existing link")
	}
	if calls != 2 {
		t.Fatal("conflicting choice reached upstream")
	}
}

func TestTHCheckoutUnknownOutcomeBlocksReplay(t *testing.T) {
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(503)
		w.Write([]byte("credential-must-not-echo"))
	}))
	defer up.Close()
	s := newTHTestServer(t)
	s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
	store, _ := s.thStore()
	rows, _ := store.Add("test", []thsignupstore.Row{{Email: "unknown@example.com"}})
	id := rows[0].ID
	store.Update(id, func(r *thsignupstore.Row) error {
		r.Status = thsignupstore.Imported
		r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "saved"}}
		return nil
	})
	path := "/api/tokenharbor/registrations/" + id + "/checkout"
	rec := postTH(t, s, path, testAgentBody)
	if rec.Code != 502 || strings.Contains(rec.Body.String(), "credential-must-not-echo") {
		t.Fatal("failure must be safe")
	}
	row, _ := store.Get(id)
	if row.Status != thsignupstore.Imported || row.Checkout == nil || row.Checkout.URL != "" {
		t.Fatal("must retain prior row status and pending marker")
	}
	if rec = postTH(t, s, path, testAgentBody); rec.Code != 409 || calls != 1 {
		t.Fatal("must block unknown outcome replay")
	}
}

func TestTHParseRegisterInputFormats(t *testing.T) {
	mail := "https://icloud.mida.vip/messages/a%40icloud.com?token=test-secret"
	for _, in := range []thRegisterInput{{Email: "a@icloud.com----" + mail}, {Email: "a@icloud.com----custom-password----" + mail}, {Email: "a@icloud.com", MailboxURL: mail}} {
		email, password, mailbox, err := thParseRegisterInput(in)
		if err != nil || email != "a@icloud.com" || mailbox != mail || password == "" || password == mail {
			t.Fatal("two/three part parsing failed")
		}
	}
	_, password, _, _ := thParseRegisterInput(thRegisterInput{Email: "a@icloud.com----custom-password----" + mail})
	if password != "custom-password" {
		t.Fatal("explicit password was replaced")
	}
}
