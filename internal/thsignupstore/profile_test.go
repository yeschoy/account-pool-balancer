package thsignupstore

import (
	"fmt"
	"testing"

	"account-pool-balancer/internal/egressid"
	"account-pool-balancer/internal/thregister"
)

func TestNewProfilesAndCompletionProgressSurviveSaveReopenAndDuplicate(t *testing.T) {
	s, path := openTemp(t)
	input := make([]Row, 200)
	for i := range input {
		input[i] = Row{Email: fmt.Sprintf("profile%d@mock.test", i), Password: "original", MailboxURL: "https://mock.test/mail"}
	}
	rows, err := s.AddSaved("first", input)
	if err != nil {
		t.Fatal(err)
	}
	profiles, orders := map[string]bool{}, map[thregister.CompletionOrder]bool{}
	for _, summary := range rows {
		r, _ := s.Get(summary.ID)
		if r.BrowserProfile != egressid.PickProfile(r.ID) || r.CompletionOrder != thregister.CompletionOrderFor(r.ID) {
			t.Fatal("new assignment missing")
		}
		profiles[r.BrowserProfile], orders[r.CompletionOrder] = true, true
	}
	if len(profiles) < 20 || len(orders) != 2 {
		t.Fatal("new profiles/order collapsed", len(profiles), orders)
	}
	first, _ := s.Get(rows[0].ID)
	if _, err := s.Update(first.ID, func(r *Row) error {
		r.VerificationMailAttempted = true
		r.APIKeyCreationAttempted = true
		r.Verified = true
		r.APIKey = "thk_live_saved_key"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Get(first.ID)
	if got.BrowserProfile != first.BrowserProfile || got.CompletionOrder != first.CompletionOrder || !got.VerificationMailAttempted || !got.APIKeyCreationAttempted || !got.Verified || got.APIKey != "thk_live_saved_key" {
		t.Fatal("new metadata or progress lost")
	}
	again, err := reopened.AddSaved("different", []Row{{Email: first.Email, Password: "replacement"}})
	if err != nil || again[0].ID != first.ID || again[0].BrowserProfile != first.BrowserProfile || again[0].CompletionOrder != first.CompletionOrder {
		t.Fatal("duplicate reassigned identity/order", err)
	}
}

func TestLegacyProfilesAreNotAssignedOnReopen(t *testing.T) {
	s, path := openTemp(t)
	rows, _ := s.AddSaved("legacy", []Row{{Email: "legacy@mock.test", Password: "original"}})
	_, err := s.Update(rows[0].ID, func(r *Row) error { r.BrowserProfile = ""; r.CompletionOrder = ""; return nil })
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, secret)
	if err != nil {
		t.Fatal(err)
	}
	r, _ := reopened.Get(rows[0].ID)
	if r.BrowserProfile != "" || r.CompletionOrder != "" || egressid.ForProfile(r.ID, r.BrowserProfile) != egressid.For(r.ID) {
		t.Fatal("legacy session remapped")
	}
}
