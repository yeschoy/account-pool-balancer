package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
		if calls == 1 {
			w.Write([]byte(testFreeAgentQuote))
			return
		}
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
	if rec = postTH(t, s, path, testAgentBody); rec.Code != 409 || calls != 2 {
		t.Fatal("must block unknown outcome replay")
	}
}

const testFreeAgentQuote = `{"quote":{"kind":"tier","fromTier":"free","oneTime":true,"period":"monthly","planName":"Agent Pass","payNowUsd":0.99}}`

func seedTHCheckout(t *testing.T, s *Server) string {
	t.Helper()
	store, err := s.thStore()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.Add("checkout", []thsignupstore.Row{{Email: "checkout@mock.test", Password: "original-password"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(rows[0].ID, func(r *thsignupstore.Row) error {
		r.Status = thsignupstore.Registered
		r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "saved"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows[0].ID
}

func TestTHCheckoutPreviewFailureCanRetryWithoutPendingOrder(t *testing.T) {
	for _, body := range []string{"temporary-error-secret", `{}`, strings.Replace(testFreeAgentQuote, `"free"`, `"plus"`, 1)} {
		t.Run(body, func(t *testing.T) {
			s := newTHTestServer(t)
			id := seedTHCheckout(t, s)
			store, _ := s.thStore()
			calls := 0
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				row, _ := store.Get(id)
				if row.Status != thsignupstore.Running || (calls <= 2 && row.Checkout != nil) || (calls == 3 && row.Checkout == nil) {
					t.Error("pending marker must only precede hosted creation, not preview")
				}
				if calls == 1 {
					w.Write([]byte(body))
				} else if calls == 2 {
					w.Write([]byte(testFreeAgentQuote))
				} else {
					w.Write([]byte(`{"checkout_url":"https://checkout.stripe.com/c/pay/cs_recovered"}`))
				}
			}))
			defer up.Close()
			s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
			path := "/api/tokenharbor/registrations/" + id + "/checkout"
			if rec := postTH(t, s, path, testAgentBody); rec.Code != 502 || strings.Contains(rec.Body.String(), "temporary-error-secret") {
				t.Fatal("expected safe preview failure", rec.Code)
			}
			row, _ := store.Get(id)
			if row.Checkout != nil || row.Status != thsignupstore.Registered || row.Password != "original-password" || len(row.Cookies) == 0 {
				t.Fatal("preview failure retained false order marker or erased credentials")
			}
			if rec := postTH(t, s, path, testAgentBody); rec.Code != 200 || calls != 3 {
				t.Fatal("explicit retry after preview failure was blocked", rec.Code)
			}
		})
	}
}

func TestTHCheckoutLegacyUncertainRequiresFreshManualConfirmation(t *testing.T) {
	s := newTHTestServer(t)
	id := seedTHCheckout(t, s)
	store, _ := s.thStore()
	before, err := store.Update(id, func(r *thsignupstore.Row) error {
		r.Checkout = &thsignupstore.Checkout{Kind: "pass", Product: "starter_pass_monthly", CreatedAt: r.CreatedAt}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503) // Failed recovery preview must preserve the legacy marker.
		} else if calls == 2 {
			w.Write([]byte(testFreeAgentQuote))
		} else {
			w.Write([]byte(`{"checkout_url":"https://checkout.stripe.com/c/pay/cs_manual_recovery"}`))
		}
	}))
	defer up.Close()
	s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
	path := "/api/tokenharbor/registrations/" + id + "/checkout"
	confirmed := `{"kind":"pass","product":"starter_pass_monthly","confirmNoPendingOrder":true}`
	if rec := postTH(t, s, path, testAgentBody); rec.Code != 409 || calls != 0 || !strings.Contains(rec.Body.String(), "已核对账单") {
		t.Fatal("legacy uncertain marker must block unconfirmed replay")
	}
	if rec := postTH(t, s, path, confirmed); rec.Code != 502 || calls != 1 {
		t.Fatal("expected recovery preview failure")
	}
	row, _ := store.Get(id)
	if row.Checkout == nil || row.Checkout.CreatedAt != before.Checkout.CreatedAt || row.Status != before.Status {
		t.Fatal("failed preview cleared uncertain history")
	}
	if rec := postTH(t, s, path, testAgentBody); rec.Code != 409 || calls != 1 {
		t.Fatal("confirmation must not be remembered for future requests")
	}
	if rec := postTH(t, s, path, confirmed); rec.Code != 200 || calls != 3 {
		t.Fatal("manual recovery failed", rec.Code)
	}
	if rec := postTH(t, s, path, confirmed); rec.Code != 200 || calls != 3 {
		t.Fatal("confirmation must not replace a saved link")
	}
}

func TestTHCheckoutSingleFlightDuringPreviewAndCreation(t *testing.T) {
	for _, stage := range []string{"preview", "create"} {
		t.Run(stage, func(t *testing.T) {
			s := newTHTestServer(t)
			id := seedTHCheckout(t, s)
			store, _ := s.thStore()
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if (stage == "preview" && n == 1) || (stage == "create" && n == 2) {
					close(entered)
					<-release
				}
				if n == 1 {
					w.Write([]byte(testFreeAgentQuote))
				} else {
					w.Write([]byte(`{"checkout_url":"https://checkout.stripe.com/c/pay/cs_single"}`))
				}
			}))
			defer up.Close()
			s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
			path := "/api/tokenharbor/registrations/" + id + "/checkout"
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- postTH(t, s, path, testAgentBody) }()
			<-entered
			row, _ := store.Get(id)
			duplicate := postTH(t, s, path, `{"kind":"pass","product":"starter_pass_monthly","confirmNoPendingOrder":true}`)
			_, proxyErr := store.SetProxy(id, "http://other-proxy:8080")
			count := calls.Load()
			close(release)
			rec := <-done
			if row.Status != thsignupstore.Running || (row.Checkout != nil) != (stage == "create") || duplicate.Code != 409 || proxyErr == nil || rec.Code != 200 || calls.Load() != 2 || (stage == "preview" && count != 1) || (stage == "create" && count != 2) {
				t.Fatal("checkout reservation allowed concurrent work or lost creation marker")
			}
		})
	}
}

func TestTHCheckoutSourceChangeDuringPreviewStopsBeforeCreation(t *testing.T) {
	s := newTHTestServer(t)
	id := seedTHCheckout(t, s)
	calls := 0
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = "http://changed.invalid/v1"; return nil })
		w.Write([]byte(testFreeAgentQuote))
	}))
	defer up.Close()
	s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
	rec := postTH(t, s, "/api/tokenharbor/registrations/"+id+"/checkout", testAgentBody)
	store, _ := s.thStore()
	row, _ := store.Get(id)
	if rec.Code != 502 || calls != 1 || row.Checkout != nil || row.Status != thsignupstore.Registered {
		t.Fatal("changed source must stop before order creation without retaining a false marker")
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
