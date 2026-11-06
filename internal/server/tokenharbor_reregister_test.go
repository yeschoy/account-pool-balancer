package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHIntermediateCookieMismatchReregistersOnce(t *testing.T) {
	for _, tc := range []struct {
		name, signupError                                      string
		captcha, key, verified, linked, checkout, loginSession bool
	}{
		{name: "registered"},
		{name: "signup-refused", signupError: "Already registered"},
		{name: "signup-limited", signupError: "We're receiving an unusually high number of sign-ups right now."},
		{name: "captcha-not-authorized", captcha: true},
		{name: "existing-key", key: true},
		{name: "verified-account", verified: true},
		{name: "linked-account", linked: true},
		{name: "existing-checkout", checkout: true},
		{name: "ambiguous-new-session", loginSession: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTHTestServer(t)
			signins, signupPages, prechecks, signups, keyCreates := 0, 0, 0, 0, 0
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/login":
					if r.Method == http.MethodGet {
						if r.URL.Query().Get("mode") == "signup" {
							signupPages++
						}
						fmt.Fprint(w, `<input name="$ACTION_1:0" value="1234567890123456789012345678901234567890"><input name="$ACTION_KEY" value="key">`)
						return
					}
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						return
					}
					if r.FormValue("_1_email") != "recover@example.com" || r.FormValue("_1_password") != "original-password" {
						t.Error("credentials changed")
					}
					if r.URL.Query().Get("mode") == "signin" {
						signins++
						if tc.loginSession {
							http.SetCookie(w, &http.Cookie{Name: "sb-test-auth-token", Value: "ambiguous", Path: "/"})
						}
						fmt.Fprint(w, `1:{"error":"That email and password don't match. Give it another try."}`)
						return
					}
					signups++
					if tc.signupError != "" {
						http.SetCookie(w, &http.Cookie{Name: "sb-test-auth-token-code-verifier", Value: "new-intermediate", Path: "/"})
						fmt.Fprintf(w, "1:{\"error\":%q}", tc.signupError)
						return
					}
					http.SetCookie(w, &http.Cookie{Name: "sb-test-auth-token", Value: "registered", Path: "/"})
					fmt.Fprint(w, "NEXT_REDIRECT")
				case "/api/auth/signup-precheck":
					prechecks++
					fmt.Fprintf(w, `{"needCaptcha":%t}`, tc.captcha)
				case "/api/keys":
					if r.Method == http.MethodPost {
						keyCreates++
						fmt.Fprintf(w, `{"plaintext":"thk_live_%s"}`, strings.Repeat("A", 48))
					} else {
						fmt.Fprint(w, `{"keys":[]}`)
					}
				case "/pricing":
					w.WriteHeader(http.StatusUnauthorized)
				default:
					t.Error("unexpected captcha purchase, consent, payment or inference request", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer site.Close()
			if _, err := s.cfg.Update(-1, func(c *config.Config) error {
				c.TokenHarbor.BaseURL = site.URL + "/v1"
				c.TokenHarbor.ProxyURL = "http://must-not-use.invalid:5000"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			store, _ := s.thStore()
			rows, err := store.Add("that_mismatch", []thsignupstore.Row{{Email: "recover@example.com", Password: "original-password", Group: "original-group"}})
			if err != nil {
				t.Fatal(err)
			}
			id := rows[0].ID
			_, err = store.Update(id, func(row *thsignupstore.Row) error {
				row.Cookies = []thsignupstore.Cookie{{Name: "sb-test-auth-token-code-verifier", Value: "original-intermediate"}}
				if tc.key {
					row.APIKey = "original-key"
				}
				row.Verified = tc.verified
				if tc.linked {
					row.Account = "original-account"
				}
				if tc.checkout {
					row.Checkout = &thsignupstore.Checkout{URL: "https://checkout.stripe.com/c/pay/mock"}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			registrar := s.thRegistrar()
			err = registrar.run(id, false, 1)
			row, _ := store.Get(id)
			fresh := !tc.key && !tc.verified && !tc.linked && !tc.checkout && !tc.loginSession
			wantPages, wantPosts := 0, 0
			if fresh {
				wantPages = 1
				if !tc.captcha {
					wantPosts = 1
				}
			}
			if signins != 1 || signupPages != wantPages || prechecks != wantPages || signups != wantPosts {
				t.Fatalf("wrong path/replay: signin=%d pages=%d precheck=%d signup=%d", signins, signupPages, prechecks, signups)
			}
			if row.Password != "original-password" || !row.CreatedAt.Equal(rows[0].CreatedAt) || row.Attempt != "that_mismatch" || row.Group != "original-group" || !row.ProxyBound || row.ProxyURL != "" {
				t.Fatal("original identity/route/history changed")
			}
			success := fresh && !tc.captcha && tc.signupError == ""
			if success {
				if err != nil || keyCreates != 1 || row.APIKey == "" || !thsignupstore.SummaryOf(row).HasSession || row.Status != thsignupstore.Unverified {
					t.Fatalf("fresh signup did not checkpoint key/session: %v", err)
				}
			} else if err == nil || keyCreates != 0 || row.Status != thsignupstore.Failed {
				t.Fatal("rejection or ambiguity must stop without key creation")
			}
			if tc.key && row.APIKey != "original-key" || tc.verified && !row.Verified || tc.linked && row.Account != "original-account" || tc.checkout && row.Checkout == nil {
				t.Fatal("existing account assets were overwritten")
			}
			logs, _ := s.thLog().since(0)
			freshLogs := 0
			for _, entry := range logs {
				if entry.Stage == "重新注册" {
					freshLogs++
				}
				if strings.Contains(entry.Detail, "original-password") {
					t.Fatal("credentials leaked into log")
				}
			}
			if (freshLogs == 1) != fresh {
				t.Fatal("missing or misleading re-registration stage")
			}
			if row.FreeModels || len(s.cfg.Get().Accounts) != 0 {
				t.Fatal("recovery must not import or change consent")
			}
		})
	}
}
