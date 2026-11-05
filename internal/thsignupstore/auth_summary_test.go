package thsignupstore

import "testing"

func TestSummaryDoesNotCallPKCEALoginSession(t *testing.T) {
	row := Row{Cookies: []Cookie{{Name: "sb-test-auth-token-code-verifier", Value: "intermediate-only"}}}
	if SummaryOf(row).HasSession {
		t.Fatal("a code verifier is not a logged-in session")
	}
	row.Cookies = append(row.Cookies, Cookie{Name: "sb-test-auth-token.0", Value: "session-part"})
	if !SummaryOf(row).HasSession {
		t.Fatal("a saved auth token must remain visible as a session")
	}
}
