package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"account-pool-balancer/internal/thsignupstore"
)

func TestTHGitHubFiveFieldSaveAndLoginDedup(t *testing.T) {
	s := newTHTestServer(t)
	body := `{"saveOnly":true,"accounts":[{"email":"mock-user----mock-password----GEZDGNBVGY3TQOJQ----bound@icloud.com----https://mail.test/api/v1/mailboxes/bound@icloud.com/code?key=private-key"}]}`
	rec := postTH(t, s, "/api/tokenharbor/registrations", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("five field save rejected: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Rows []thsignupstore.Summary `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Rows) != 1 {
		t.Fatal("missing saved row")
	}
	store, _ := s.thStore()
	row, _ := store.Get(out.Rows[0].ID)
	if row.Email != "bound@icloud.com" || row.GitHub == nil || row.GitHub.Login != "mock-user" || row.Password != "mock-password" || !strings.Contains(row.MailboxURL, "key=private-key") || row.Status != thsignupstore.Saved {
		t.Fatal("five fields did not survive persistence")
	}
	if len(row.Cookies) != 0 || len(row.GitHub.Cookies) != 0 {
		t.Fatal("save-only performed authentication")
	}
	for _, secret := range []string{"mock-password", "GEZDGNBV", "private-key"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("summary leaked credentials")
		}
	}
	// The same GitHub login remains the identity even if a later pasted email
	// or mailbox URL differs. Original credentials and mailbox must win.
	duplicate := `{"saveOnly":true,"accounts":[{"provider":"github","login":"MOCK-USER","password":"replacement","totpSecret":"GEZDGNBVGY3TQOJQ","githubEmail":"other@icloud.com","mailboxUrl":"https://mail.test/api/v1/mailboxes/other@icloud.com/code?key=other-key"}]}`
	if second := postTH(t, s, "/api/tokenharbor/registrations", duplicate); second.Code != http.StatusOK {
		t.Fatalf("login dedup failed: %d", second.Code)
	}
	preserved, _ := store.Get(row.ID)
	if len(store.List()) != 1 || preserved.Email != row.Email || preserved.Password != row.Password || preserved.MailboxURL != row.MailboxURL {
		t.Fatal("duplicate overwrote original five-field account")
	}
}

func TestTHGitHubFiveFieldValidationAndBatchIdentity(t *testing.T) {
	s := newTHTestServer(t)
	for _, line := range []string{
		"mock-user----pw----GEZDGNBVGY3TQOJQ--------https://mail.test/api/v1/mailboxes/test@icloud.com/code?key=private-key",
		"mock-user----pw----GEZDGNBVGY3TQOJQ----invalid----https://mail.test/api/v1/mailboxes/test@icloud.com/code?key=private-key",
		"mock-user----pw----GEZDGNBVGY3TQOJQ----other@icloud.com----https://mail.test/api/v1/mailboxes/test@icloud.com/code?key=private-key",
	} {
		body, _ := json.Marshal(map[string]any{"saveOnly": true, "accounts": []map[string]string{{"email": line}}})
		rec := postTH(t, s, "/api/tokenharbor/registrations", string(body))
		if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "private-key") {
			t.Fatal("bad five-field input accepted or secret reflected")
		}
	}
	store, _ := s.thStore()
	if len(store.List()) != 0 {
		t.Fatal("invalid input created partial rows")
	}
	// Two different logins may share a bound email; one login repeated in the
	// same request must still be rejected before adding anything.
	account := func(login, email string) map[string]string {
		return map[string]string{"provider": "github", "login": login, "password": "pw", "totpSecret": "GEZDGNBVGY3TQOJQ", "githubEmail": email, "mailboxUrl": "https://mail.test/api/v1/mailboxes/" + email + "/code?key=private-key"}
	}
	body, _ := json.Marshal(map[string]any{"saveOnly": true, "accounts": []map[string]string{account("mock-user", "one@icloud.com"), account("MOCK-USER", "two@icloud.com")}})
	if rec := postTH(t, s, "/api/tokenharbor/registrations", string(body)); rec.Code != http.StatusBadRequest || len(store.List()) != 0 {
		t.Fatal("batch duplicate identity was not rejected atomically")
	}
	body, _ = json.Marshal(map[string]any{"saveOnly": true, "accounts": []map[string]string{account("mock-one", "shared@icloud.com"), account("mock-two", "shared@icloud.com")}})
	if rec := postTH(t, s, "/api/tokenharbor/registrations", string(body)); rec.Code != http.StatusCreated || len(store.List()) != 2 {
		t.Fatal("different GitHub logins were deduplicated by mailbox email")
	}
}
