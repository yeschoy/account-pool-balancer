package thsignupstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetryAndReopenKeepSessionKeyAndCheckout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signup.json")
	secret := []byte("test-routing-secret-at-least-16-bytes")
	s, err := Open(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Add("test", []Row{{Email: "test@example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	id := rows[0].ID
	_, err = s.Update(id, func(r *Row) error {
		r.Status = Failed
		r.APIKey = "thk_live_SECRETSECRETSECRETSECRETSECRETSECRETSECRET"
		r.Cookies = []Cookie{{Name: "session", Value: "cookie-secret", Path: "/", Secure: true, HTTPOnly: true}}
		r.Checkout = &Checkout{Kind: "topup", Product: "starter", URL: "https://checkout.stripe.com/c/pay/private-secret"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Retry(id); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	row, err := s2.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if row.APIKey == "" || len(row.Cookies) != 1 || !row.Cookies[0].Secure || !row.Cookies[0].HTTPOnly || row.Checkout == nil || row.Checkout.URL == "" {
		t.Fatal("retry/restart erased business credentials")
	}
	// Trusted callers must not mutate the store through returned pointers.
	row.Checkout.URL = "changed"
	row.Cookies[0].Value = "changed"
	again, _ := s2.Get(id)
	if again.Checkout.URL == "changed" || again.Cookies[0].Value == "changed" {
		t.Fatal("mutable snapshot escaped")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"cookie-secret", "private-secret", "thk_live_SECRET"} {
		if strings.Contains(string(raw), v) {
			t.Fatal("encrypted file contains plaintext")
		}
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential store permissions")
	}
}
