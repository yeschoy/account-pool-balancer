package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPerchRegistrationList(t *testing.T) {
	e := newPerchOpenEnv(t)
	mux := http.NewServeMux()
	e.srv.registerAdmin(mux)
	cookieRec := httptest.NewRecorder()
	e.srv.admin.create(cookieRec, httptest.NewRequest(http.MethodPost, "/api/login", nil))
	cookie := cookieRec.Result().Cookies()[0]
	get := func(path string, authenticated bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if authenticated {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	path := "/api/perch/registrations"
	if rec := get(path, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d %s", rec.Code, rec.Body.String())
	}
	rec := get(path, true)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"jobs":[]}` {
		t.Fatalf("empty list: %d %s", rec.Code, rec.Body.String())
	}
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	e.srv.perchRegistrations.jobs = map[string]*registrationJob{
		"old": {ID: "old", Status: "completed", CreatedAt: old},
		"b":   {ID: "b", Status: "running", CreatedAt: newer},
		"a": {ID: "a", Status: "completed", CreatedAt: newer, Accounts: []registrationRow{{
			Login: "user", Status: "activated", CanImport: true,
			doc:      []byte(`{"access_token":"session-secret","refresh_token":"refresh-secret"}`),
			proxyURL: "http://proxy-user:proxy-password@proxy.example:8080",
		}}},
	}
	rec = get(path, true)
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Jobs) != 3 || list.Jobs[0]["id"] != "a" || list.Jobs[1]["id"] != "b" || list.Jobs[2]["id"] != "old" {
		t.Fatalf("unexpected order: %s", rec.Body.String())
	}
	if list.Jobs[0]["createdAt"] != newer.Format(time.RFC3339) {
		t.Fatalf("unexpected createdAt: %v", list.Jobs[0]["createdAt"])
	}
	for _, secret := range []string{"session-secret", "refresh-secret", "proxy-user", "proxy-password", "proxy.example", "access_token", "proxyURL"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("list exposed %q: %s", secret, rec.Body.String())
		}
	}
	single := get(path+"/a", true)
	var job map[string]any
	if err := json.Unmarshal(single.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if single.Code != http.StatusOK || !reflect.DeepEqual(list.Jobs[0], job) {
		t.Fatalf("list and single differ: %s vs %s", rec.Body.String(), single.Body.String())
	}
}

func TestPerchRegistrationCapacityPreservesPending(t *testing.T) {
	for _, evictable := range []bool{true, false} {
		t.Run(fmt.Sprintf("evictable=%v", evictable), func(t *testing.T) {
			e := newPerchOpenEnv(t)
			m := &e.srv.perchRegistrations
			m.jobs = make(map[string]*registrationJob)
			for i := 0; i < 20; i++ {
				id := fmt.Sprintf("job%02d", i)
				m.jobs[id] = &registrationJob{ID: id, Status: "completed", CreatedAt: time.Now().Add(time.Duration(i-48) * time.Hour), Accounts: []registrationRow{{CanImport: true, doc: []byte("secret")}}}
			}
			if evictable {
				// Both candidates are recent enough to require capacity eviction.
				m.jobs["job01"].CreatedAt = time.Now().Add(-2 * time.Hour)
				m.jobs["job01"].Accounts[0].CanImport = false
				m.jobs["job02"].CreatedAt = time.Now().Add(-time.Hour)
				m.jobs["job02"].Accounts[0].CanImport = false
			}
			done := make(chan struct{})
			m.worker = func(context.Context, registrationInput) ([]byte, error) { close(done); return nil, errors.New("test") }
			req := httptest.NewRequest(http.MethodPost, "/api/perch/registrations", bytes.NewBufferString(`{"accounts":[{"login":"user","password":"pw","totpSecret":"otp","mailboxUrl":"mail"}]}`))
			rec := httptest.NewRecorder()
			e.srv.apiPerchRegister(rec, req)
			want := http.StatusConflict
			if evictable {
				want = http.StatusAccepted
			}
			if rec.Code != want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, want, rec.Body.String())
			}
			if evictable {
				<-done
				deadline := time.Now().Add(5 * time.Second)
				for {
					m.mu.Lock()
					running := m.running
					m.mu.Unlock()
					if !running {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("registration worker did not finish")
					}
					time.Sleep(time.Millisecond)
				}
				if m.jobs["job01"] != nil || m.jobs["job02"] == nil {
					t.Fatal("wrong eviction candidate")
				}
			}
			if m.jobs["job00"] == nil {
				t.Fatal("expired pending task was deleted")
			}
		})
	}
}
