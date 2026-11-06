package thsignupstore

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestGitHubPlaintextRowsKeepLegacyEncrypted(t *testing.T) {
	s, path := openTemp(t)
	if _, err := s.AddSaved("email-attempt", []Row{{Email: "legacy@local.test", Password: "legacy-private-password"}}); err != nil {
		t.Fatal(err)
	}
	added, err := s.AddSaved("github-attempt", []Row{{Email: "github:mock-user", Password: "github-plain-password", GitHub: &GitHubAuth{Login: "mock-user", TOTPSecret: "mock-totp", ProxyURL: "http://ipv4.local:5000"}, ProxyURL: "http://ipv6.local:5000"}})
	if err != nil {
		t.Fatal(err)
	}
	id := added[0].ID
	_, err = s.Update(id, func(r *Row) error {
		r.Email = "same-mail@local.test"
		r.Cookies = []Cookie{{Name: "sb-auth-auth-token", Value: "th-plain-session"}}
		r.GitHub.Cookies = []Cookie{{Name: "user_session", Value: "github-plain-session"}}
		r.GitHub.OAuthComplete = true
		r.GitHub.ProviderToken = "gho_mock_plain_token"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"github-plain-password", "github-plain-session", "th-plain-session", "mock-totp", "gho_mock_plain_token"} {
		if !strings.Contains(string(raw), value) {
			t.Fatal("GitHub opt-in plaintext missing")
		}
	}
	if strings.Contains(string(raw), "legacy-private-password") || strings.Contains(string(raw), "legacy@local.test") {
		t.Fatal("legacy credentials decrypted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("wrong permissions")
	}
	s, err = Open(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.Get(id)
	if err != nil || !row.GitHub.OAuthComplete || row.ProxyURL == row.GitHub.ProxyURL {
		t.Fatal("reopen lost independent routes/sessions")
	}
	row.GitHub.Cookies[0].Value = "mutated"
	fresh, _ := s.Get(id)
	if fresh.GitHub.Cookies[0].Value == "mutated" {
		t.Fatal("clone aliases credentials")
	}
	again, err := s.AddSaved("other", []Row{{Email: "github:mock-user", Password: "replacement", GitHub: &GitHubAuth{Login: "mock-user"}}})
	if err != nil || again[0].ID != id {
		t.Fatal("GitHub login dedupe lost after resolving email")
	}
	projection, _ := json.Marshal(s.List())
	for _, secret := range []string{"github-plain-password", "github-plain-session", "th-plain-session", "mock-totp", "gho_mock_plain_token"} {
		if strings.Contains(string(projection), secret) {
			t.Fatal("summary leaked credential")
		}
	}
	if !s.List()[0].HasSession && !s.List()[1].HasSession {
		t.Fatal("TH session not projected")
	}
}
