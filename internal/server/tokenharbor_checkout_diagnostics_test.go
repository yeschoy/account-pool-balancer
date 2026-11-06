package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHCheckoutPreviewDoesNotReserveOrLockANewCheckout(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["preview"] != true {
			t.Error("preview attempted to create an order")
		}
		w.Write([]byte(testFreeAgentQuote))
	}))
	defer up.Close()
	s := newTHTestServer(t)
	s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil })
	store, _ := s.thStore()
	rows, err := store.AddSaved("preview-only", []thsignupstore.Row{{Email: "preview@mock.test"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Update(rows[0].ID, func(r *thsignupstore.Row) error {
		r.Status = thsignupstore.Registered
		r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "saved"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	w := postTH(t, s, "/api/tokenharbor/registrations/"+before.ID+"/checkout/preview", testAgentBody)
	after, _ := store.Get(before.ID)
	if w.Code != 200 || calls.Load() != 1 || !reflect.DeepEqual(before, after) || after.Checkout != nil {
		t.Fatal("read-only preview reserved a checkout, locked the row or changed state", w.Code)
	}
}

func TestTHCheckoutPreviewKeepsUncertainMarkerAndFirstFailure(t *testing.T) {
	var previews, creates atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["preview"] == true {
			previews.Add(1)
			w.Write([]byte(testFreeAgentQuote))
			return
		}
		creates.Add(1)
		w.WriteHeader(502)
		w.Write([]byte(`{"error":"stripe_session_failed","message":"private-body-cookie"}`))
	}))
	defer up.Close()
	s := newTHTestServer(t)
	if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = up.URL + "/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	store, _ := s.thStore()
	rows, err := store.AddSaved("pay-diagnostic", []thsignupstore.Row{{Email: "pay-diagnostic@mock.test", Password: "private-password"}})
	if err != nil {
		t.Fatal(err)
	}
	id := rows[0].ID
	if _, err := store.Update(id, func(r *thsignupstore.Row) error {
		r.Status = thsignupstore.Registered
		r.Cookies = []thsignupstore.Cookie{{Name: "session", Value: "private-session-cookie"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	path := "/api/tokenharbor/registrations/" + id + "/checkout"
	first := postTH(t, s, path, testAgentBody)
	if first.Code != 502 || !strings.Contains(first.Body.String(), "stripe_session_failed") || strings.Contains(first.Body.String(), "private-body") {
		t.Fatal("original failure was hidden or leaked", first.Code, first.Body.String())
	}
	before, _ := store.Get(id)
	if before.Checkout == nil || before.Checkout.Failure == nil || before.Checkout.Failure.Stage != "create" || before.Checkout.Failure.Code != "stripe_session_failed" || before.Checkout.URL != "" {
		t.Fatal("creation failure not retained in pending marker")
	}
	if blocked := postTH(t, s, path, testAgentBody); blocked.Code != 409 || !strings.Contains(blocked.Body.String(), "首次失败") || !strings.Contains(blocked.Body.String(), "stripe_session_failed") {
		t.Fatal("protection obscured original failure", blocked.Code)
	}
	if creates.Load() != 1 {
		t.Fatal("blocked recovery replayed creation")
	}
	for _, loggedIn := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPost, path+"/preview", strings.NewReader(testAgentBody))
		if loggedIn {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		want := 401
		if loggedIn {
			want = 403
		}
		if w.Code != want {
			t.Fatal("preview bypassed admin/CSRF", w.Code)
		}
	}
	preview := postTH(t, s, path+"/preview", testAgentBody)
	if preview.Code != 200 || preview.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("preview failed", preview.Code, preview.Body.String())
	}
	var out struct {
		Quote struct {
			PayNow float64 `json:"payNowUsd"`
		} `json:"quote"`
		Creates bool `json:"createsOrder"`
		Proves  bool `json:"provesNoPendingOrder"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &out); err != nil || out.Quote.PayNow != .99 || out.Creates || out.Proves {
		t.Fatal("unsafe preview claims", err)
	}
	after, _ := store.Get(id)
	if !reflect.DeepEqual(before, after) || creates.Load() != 1 || previews.Load() != 2 {
		t.Fatal("preview mutated pending marker, account or order state")
	}
	summary := store.List()[0]
	if !summary.CheckoutPending || summary.HasCheckout || summary.CheckoutFailure == nil {
		t.Fatal("pending diagnostic not projected")
	}
	summary.CheckoutFailure.Message = "mutated copy"
	detached, _ := store.Get(id)
	if detached.Checkout.Failure.Message == "mutated copy" {
		t.Fatal("summary diagnostic aliased persisted data")
	}
	reopened, err := thsignupstore.Open(filepath.Join(s.dataDir, "tokenharbor-signup.json"), s.secret)
	if err != nil {
		t.Fatal(err)
	}
	persisted, _ := reopened.Get(id)
	if persisted.Checkout.Failure.Code != "stripe_session_failed" {
		t.Fatal("failure was lost on reopen")
	}
	raw, _ := json.Marshal(store.List())
	for _, secret := range []string{"private-body-cookie", "private-session-cookie", "private-password"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("diagnostic projection leaked credentials")
		}
	}
	if blocked := postTH(t, s, path, testAgentBody); blocked.Code != 409 || creates.Load() != 1 {
		t.Fatal("successful quote was treated as permission to replay")
	}
	if rejected := postTH(t, s, path+"/preview", `{"kind":"topup","product":"value"}`); rejected.Code != 400 {
		t.Fatal("preview accepted another product")
	}
}
