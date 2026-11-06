package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"account-pool-balancer/internal/thsignupstore"
)

func TestTHJobsSortByCreationBeforeLimiting(t *testing.T) {
	s := newTHTestServer(t)
	store, _ := s.thStore()
	base := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	var oldestID string
	for i := 0; i < 55; i++ {
		// Lexicographic ID order is intentionally the reverse of creation order.
		id := fmt.Sprintf("that_%03d", 100-i)
		rows, err := store.AddSaved(id, []thsignupstore.Row{{Email: fmt.Sprintf("job%d@mock.test", i), Password: "mock"}})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			oldestID = rows[0].ID
		}
		_, err = store.Update(rows[0].ID, func(r *thsignupstore.Row) error { r.CreatedAt = base.Add(time.Duration(i) * time.Minute); return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	get := func(path string, out any) {
		req := httptest.NewRequest("GET", path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(rec.Code, rec.Body.String())
		}
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var first struct {
		Jobs []thRegistrationView `json:"jobs"`
	}
	get("/api/tokenharbor/registrations", &first)
	if len(first.Jobs) != 50 {
		t.Fatal("wrong list cap", len(first.Jobs))
	}
	for i, job := range first.Jobs {
		if want := fmt.Sprintf("that_%03d", 46+i); job.ID != want || !job.CreatedAt.Equal(base.Add(time.Duration(54-i)*time.Minute)) {
			t.Fatal("random ID sort or cap-before-sort", i, job.ID)
		}
	}
	var detail thRegistrationView
	get("/api/tokenharbor/registrations/"+first.Jobs[0].ID, &detail)
	if !detail.CreatedAt.Equal(first.Jobs[0].CreatedAt) {
		t.Fatal("detail/list disagree on creation time")
	}
	_, err := store.Update(oldestID, func(r *thsignupstore.Row) error { r.Status = thsignupstore.Running; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetAttemptNote("that_100", "just edited"); err != nil {
		t.Fatal(err)
	}
	var after struct {
		Jobs []thRegistrationView `json:"jobs"`
	}
	get("/api/tokenharbor/registrations", &after)
	if !reflect.DeepEqual(first, after) {
		t.Fatal("status/note/update promoted an old task into the latest 50")
	}
}

func TestTHJobCreationIsEarliestRowAndTieOrderStable(t *testing.T) {
	old := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	rows := []thsignupstore.Summary{{CreatedAt: old.Add(time.Hour), Status: thsignupstore.Running}, {CreatedAt: old, Status: thsignupstore.Saved}}
	view := thJobView("job", "note", rows)
	if !view.CreatedAt.Equal(old) || !view.Running || view.Counts["saved"] != 1 {
		t.Fatal("task must use creation, not latest account activity")
	}
	s := newTHTestServer(t)
	store, _ := s.thStore()
	for _, id := range []string{"that_b", "that_a"} {
		r, _ := store.AddSaved(id, []thsignupstore.Row{{Email: id + "@mock.test"}})
		_, err := store.Update(r[0].ID, func(r *thsignupstore.Row) error { r.CreatedAt = old; return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	for range 10 {
		rec := httptest.NewRecorder()
		s.apiTHRegistrations(rec, httptest.NewRequest("GET", "/", nil))
		var out struct {
			Jobs []thRegistrationView `json:"jobs"`
		}
		json.Unmarshal(rec.Body.Bytes(), &out)
		if len(out.Jobs) != 2 || out.Jobs[0].ID != "that_a" || out.Jobs[1].ID != "that_b" {
			t.Fatal("equal-time tasks flicker with map iteration")
		}
	}
}
