package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHResendVerification(t *testing.T) {
	s := newTHTestServer(t)
	var posts atomic.Int32
	var reject, alreadyVerified atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("sb-test-auth-token"); err != nil {
			t.Error("saved session missing")
		}
		switch {
		case r.URL.Path == "/api/me/send-verification-email" && r.Method == http.MethodPost:
			n := posts.Add(1)
			if r.Header.Get("Origin") == "" || !strings.HasSuffix(r.Header.Get("Referer"), "/dashboard") {
				t.Error("missing origin or dashboard referer")
			}
			if n == 1 {
				close(entered)
				<-release
			}
			if reject.Load() {
				fmt.Fprint(w, `{"ok":false,"error":"reflected-session-and-secrets"}`)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "sb-test-auth-token", Value: "refreshed", Path: "/"})
			fmt.Fprintf(w, `{"ok":true,"alreadyVerified":%t}`, alreadyVerified.Load())
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	if _, err := s.cfg.Update(-1, func(c *config.Config) error {
		c.TokenHarbor.BaseURL = upstream.URL + "/v1"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	store, _ := s.thStore()
	rows, err := store.Add("resend", []thsignupstore.Row{{Email: "mail@mock.test", Password: "keep-password", APIKey: "thk_live_keep-key", ProxyBound: true}})
	if err != nil {
		t.Fatal(err)
	}
	id := rows[0].ID
	original, err := store.Update(id, func(rw *thsignupstore.Row) error {
		rw.Status = thsignupstore.Unverified
		rw.Cookies = []thsignupstore.Cookie{{Name: "sb-test-auth-token", Value: "saved", Path: "/"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tokenharbor/registrations/" + id + "/resend-verification"
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- postTH(t, s, path, `{}`) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("resend did not reach mock endpoint")
	}
	busy := postTH(t, s, path, `{}`)
	close(release)
	sent := <-result
	if busy.Code != http.StatusConflict || sent.Code != http.StatusOK || posts.Load() != 1 {
		t.Fatalf("busy=%d sent=%d posts=%d response=%s", busy.Code, sent.Code, posts.Load(), sent.Body.String())
	}
	if sent.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("sensitive response is cacheable")
	}
	row, _ := store.Get(id)
	if row.Verified || row.Status != original.Status || row.APIKey != original.APIKey || row.Password != original.Password || row.CreatedAt != original.CreatedAt || row.Cookies[0].Value != "refreshed" {
		t.Fatal("resend must preserve lifecycle, credentials and verification state, saving refreshed session only")
	}
	for _, secret := range []string{"keep-password", "thk_live_keep-key", "refreshed"} {
		if strings.Contains(sent.Body.String(), secret) {
			t.Fatal("response leaked credentials")
		}
	}
	// An HTTP200 business rejection must not claim mail was sent or replay it.
	reject.Store(true)
	failed := postTH(t, s, path, `{}`)
	row, _ = store.Get(id)
	if failed.Code != http.StatusBadGateway || posts.Load() != 2 || row.Status != original.Status || row.Verified || strings.Contains(failed.Body.String(), "reflected-session") {
		t.Fatal("business rejection was replayed, leaked or changed verification state")
	}
	reject.Store(false)
	alreadyVerified.Store(true)
	verified := postTH(t, s, path, `{}`)
	row, _ = store.Get(id)
	if verified.Code != http.StatusOK || !row.Verified || row.Status != thsignupstore.Registered || posts.Load() != 3 {
		t.Fatal("upstream alreadyVerified proof was not preserved")
	}
	if rec := postTH(t, s, path, `{}`); rec.Code != http.StatusConflict || posts.Load() != 3 {
		t.Fatal("already-verified row must not send again")
	}
}
