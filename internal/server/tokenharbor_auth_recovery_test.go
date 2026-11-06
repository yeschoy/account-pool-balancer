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

func TestTHIntermediateCookieRetryLogsInWithoutSignup(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(fmt.Sprint(accepted), func(t *testing.T) {
			s := newTHTestServer(t)
			signins, signups, keyCreates := 0, 0, 0
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/login":
					if r.Method == http.MethodGet {
						fmt.Fprint(w, `<input name="$ACTION_1:0" value="1234567890123456789012345678901234567890"><input name="$ACTION_KEY" value="key">`)
						return
					}
					if r.URL.Query().Get("mode") != "signin" {
						signups++
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					signins++
					if !accepted {
						fmt.Fprint(w, "1:{\"error\":\"Account sign-in refused\"}\n")
						return
					}
					http.SetCookie(w, &http.Cookie{Name: "sb-test-auth-token", Value: "logged-in", Path: "/"})
					fmt.Fprint(w, "NEXT_REDIRECT")
				case "/api/keys":
					if !accepted {
						t.Error("rejected login must not request keys")
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if r.Method == http.MethodPost {
						keyCreates++
						fmt.Fprintf(w, `{"plaintext":"thk_live_%s"}`, strings.Repeat("A", 48))
					} else {
						fmt.Fprint(w, `{"keys":[]}`)
					}
				case "/pricing":
					w.WriteHeader(http.StatusUnauthorized)
				default:
					t.Error("unexpected signup, captcha, privacy or inference request", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer site.Close()
			if _, err := s.cfg.Update(-1, func(c *config.Config) error {
				c.TokenHarbor.BaseURL = site.URL + "/v1"
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			store, _ := s.thStore()
			rows, err := store.Add("that_recovery", []thsignupstore.Row{{Email: "recover@example.com", Password: "original-password"}})
			if err != nil {
				t.Fatal(err)
			}
			id := rows[0].ID
			_, err = store.Update(id, func(row *thsignupstore.Row) error {
				row.Cookies = []thsignupstore.Cookie{{Name: "sb-test-auth-token-code-verifier", Value: "intermediate-only"}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			err = s.thRegistrar().run(id, false, 1)
			row, _ := store.Get(id)
			if signins != 1 || signups != 0 || row.Password != "original-password" || !row.CreatedAt.Equal(rows[0].CreatedAt) {
				t.Fatal("retry must try the original login once without another signup or credential reset")
			}
			if accepted {
				if err != nil || keyCreates != 1 || row.APIKey == "" || !thsignupstore.SummaryOf(row).HasSession {
					t.Fatalf("successful login must continue and save the new key: %v", err)
				}
			} else if err == nil || keyCreates != 0 || row.Status != thsignupstore.Failed || !strings.Contains(row.Detail, "Account sign-in refused") || thsignupstore.SummaryOf(row).HasSession {
				t.Fatal("failed login must stop with the upstream business reason, no key and no fake session")
			}
			// The recovery must never import an account or opt in to free models.
			if row.Account != "" || row.FreeModels || len(s.cfg.Get().Accounts) != 0 {
				t.Fatal("login recovery must not import or change consent")
			}
		})
	}
}
