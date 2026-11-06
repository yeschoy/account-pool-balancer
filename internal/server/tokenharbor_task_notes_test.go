package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thsignupstore"
)

func TestTHJobNotesAreAuthenticatedTaskScopedAndPersistent(t *testing.T) {
	s := newTHTestServer(t)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	if _, err := s.cfg.Update(-1, func(c *config.Config) error { c.TokenHarbor.BaseURL = upstream.URL + "/v1"; return nil }); err != nil {
		t.Fatal(err)
	}
	store, err := s.thStore()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.AddSaved("thj_note-one", []thsignupstore.Row{
		{Email: "one@mock.test", Password: "private-password", ProxyURL: "http://user:private-proxy@proxy.test:8080"},
		{Email: "two@mock.test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddSaved("thj_note-two", []thsignupstore.Row{{Email: "three@mock.test"}}); err != nil {
		t.Fatal(err)
	}
	before, err := store.Update(rows[0].ID, func(r *thsignupstore.Row) error {
		r.Status = thsignupstore.Running
		r.APIKey = "thk_live_private-note-test-key"
		r.Cookies = []thsignupstore.Cookie{{Name: "sb-mock-auth-token", Value: "private-cookie"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tokenharbor/registrations/thj_note-one/note"
	for _, loggedIn := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"note":"blocked"}`))
		if loggedIn {
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		want := http.StatusUnauthorized
		if loggedIn {
			want = http.StatusForbidden
		}
		if w.Code != want || store.AttemptNote("thj_note-one") != "" {
			t.Fatal("note edit bypassed authentication or CSRF", w.Code)
		}
	}
	note := `美国测试 <img src=x onerror=alert(1)> & "批次"`
	body, _ := json.Marshal(map[string]string{"note": "  " + note + "  "})
	w := putTH(t, s, path, string(body))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("note save failed", w.Code, w.Body.String())
	}
	var edit map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &edit); err != nil || edit["id"] != "thj_note-one" || edit["note"] != note {
		t.Fatal("wrong note save response", err)
	}
	after, _ := store.Get(before.ID)
	if !reflect.DeepEqual(before, after) || store.AttemptNote("thj_note-two") != "" {
		t.Fatal("note edit changed account state or another task")
	}
	for _, endpoint := range []string{"/api/tokenharbor/registrations", "/api/tokenharbor/registrations/thj_note-one"} {
		req := httptest.NewRequest(http.MethodGet, endpoint, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: savedSession(t, s)})
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("task read failed", w.Code)
		}
		var view thRegistrationView
		if strings.HasSuffix(endpoint, "thj_note-one") {
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
		} else {
			var list struct {
				Jobs []thRegistrationView `json:"jobs"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			for _, job := range list.Jobs {
				if job.ID == "thj_note-one" {
					view = job
				} else if job.Note != "" {
					t.Fatal("note leaked into another task")
				}
			}
		}
		if view.Note != note || len(view.Rows) != 2 || !view.Running {
			t.Fatal("task note not returned with original task state")
		}
		for _, secret := range []string{before.Password, before.APIKey, before.Cookies[0].Value, "private-proxy"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("note/list response leaked account credentials")
			}
		}
	}
	if w := putTH(t, s, path, `{"note":""}`); w.Code != http.StatusOK || store.AttemptNote("thj_note-one") != "" {
		t.Fatal("explicit clear failed", w.Code)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatal("editing notes triggered upstream registration or account work")
	}
}

func TestTHJobNoteRejectsMissingInvalidAndUnknownTask(t *testing.T) {
	s := newTHTestServer(t)
	store, _ := s.thStore()
	rows, err := store.AddSaved("task", []thsignupstore.Row{{Email: "note@mock.test"}})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/tokenharbor/registrations/task/note"
	for _, body := range []string{`{}`, `{"note":null}`, `{"note":3}`, `{"note":"unfinished"`, `{"note":"` + strings.Repeat("测", 201) + `"}`} {
		if w := putTH(t, s, path, body); w.Code != http.StatusBadRequest {
			t.Fatal("invalid note accepted", w.Code)
		}
	}
	for _, id := range []string{"unknown-task", rows[0].ID} {
		if w := putTH(t, s, "/api/tokenharbor/registrations/"+id+"/note", `{"note":"wrong task"}`); w.Code != http.StatusNotFound {
			t.Fatal("unknown task or row ID accepted", w.Code)
		}
	}
	if store.AttemptNote("task") != "" {
		t.Fatal("invalid request changed note")
	}
}
