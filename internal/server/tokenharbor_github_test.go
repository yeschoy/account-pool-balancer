package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHGitHubSaveOnlyAndIndependentRoutes(t *testing.T) {
	s := newTHTestServer(t)
	body := `{"saveOnly":true,"accounts":[{"email":"mock-user----mock-password----GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ----https://191006.xyz/mailbox/mock-mail-token"}],"proxies":["socks5://mock:private@ipv4.local:5000"],"sessionProxies":["http://mock:private@ipv6.local:6000"]}`
	rec := postTH(t, s, "/api/tokenharbor/registrations", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("save only: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Rows []thsignupstore.Summary `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Rows) != 1 {
		t.Fatal("missing summary")
	}
	summary := out.Rows[0]
	if summary.AuthProvider != "github" || summary.GitHubLogin != "mock-user" || summary.Status != thsignupstore.Saved || summary.Proxy != "http://ipv6.local:6000" || summary.GitHubProxy != "socks5://ipv4.local:5000" {
		t.Fatal("bad provider/route projection")
	}
	if strings.Contains(rec.Body.String(), "mock-password") || strings.Contains(rec.Body.String(), "private") || strings.Contains(rec.Body.String(), "GEZDGNBV") {
		t.Fatal("HTTP leaked credentials")
	}
	store, _ := s.thStore()
	row, _ := store.Get(summary.ID)
	if row.GitHub.TOTPSecret == "" || row.GitHub.ProxyURL == row.ProxyURL {
		t.Fatal("lost credentials/routes")
	}
	if len(row.Cookies) != 0 || len(row.GitHub.Cookies) != 0 {
		t.Fatal("saveOnly started authentication")
	}
	row.GitHub.OAuthComplete = true
	row.GitHub.ProviderToken = "gho_mock_token"
	row.GitHub.Cookies = []thsignupstore.Cookie{{Name: "user_session", Value: "mock-session"}}
	thSaveResult(&row, thregister.Result{APIKey: "thk_live_mock_abcdefghijklmnopqrstuvwxyz", VerifiedEmail: true})
	if !row.GitHub.OAuthComplete || len(row.GitHub.Cookies) != 1 || row.GitHub.ProviderToken != "gho_mock_token" {
		t.Fatal("TH-only refresh/reverify erased GitHub session")
	}
}

func TestTHGitHubRejectsInvalidCredentialsBeforeSaving(t *testing.T) {
	s := newTHTestServer(t)
	for _, body := range []string{
		`{"saveOnly":true,"accounts":[{"provider":"github","login":"mock-user","password":"pw","totpSecret":"invalid","mailboxUrl":"https://191006.xyz/mailbox/mock-token"}]}`,
		`{"saveOnly":true,"accounts":[{"provider":"github","login":"mock-user","password":"pw","totpSecret":"GEZDGNBVGY3TQOJQ","mailboxUrl":"https://191006.xyz/mailbox/mock-token"}],"sessionProxies":["socks4://example:1000"]}`,
	} {
		if rec := postTH(t, s, "/api/tokenharbor/registrations", body); rec.Code != http.StatusBadRequest {
			t.Fatal("invalid input accepted")
		}
	}
	store, _ := s.thStore()
	if len(store.List()) != 0 {
		t.Fatal("partial validation saved rows")
	}
}
