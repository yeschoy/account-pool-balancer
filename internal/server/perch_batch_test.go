package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPerchImportIsolatesIdentityMismatch(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-good")
	e.fake.seed("u-bad")
	bad := exportDocAsMap("u-bad")
	badSession := bad["session"].(map[string]any)
	badSession["access_token"] = "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"other"}`)) + ".signature"
	badRaw, err := json.Marshal(bad)
	if err != nil {
		t.Fatal(err)
	}
	e.importFiles(map[string][]byte{
		"good.json": []byte(exportDoc("u-good", e.fake, time.Now().Add(time.Hour).Unix(), 0)),
		"bad.json":  badRaw,
	})
	if _, ok := e.srv.perch.keeper.ByUserID("u-good"); !ok {
		t.Fatal("valid session was blocked by a different invalid document")
	}
	if _, ok := e.srv.perch.keeper.ByUserID("u-bad"); ok {
		t.Fatal("identity-mismatched session was imported")
	}
}

func TestPerchBatchSessionRejectsMultipleAccounts(t *testing.T) {
	e := newPerchEnv(t)
	for _, uid := range []string{"u1", "u2"} {
		e.fake.seed(uid)
	}
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u1", e.fake, time.Now().Add(time.Hour).Unix(), 0)), "b.json": []byte(exportDoc("u2", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	if len(rows) != 2 {
		t.Fatalf("accounts = %d", len(rows))
	}
	ids := []string{rows[0]["id"].(string), rows[1]["id"].(string)}
	before1, _ := e.srv.perch.keeper.Get(ids[0])
	before2, _ := e.srv.perch.keeper.Get(ids[1])
	raw, _ := json.Marshal(map[string]any{"action": "session", "ids": ids, "session": map[string]string{"accessToken": "shared", "refreshToken": "shared-refresh"}})
	rec := httptest.NewRecorder()
	e.srv.apiPerchBatch(rec, httptest.NewRequest(http.MethodPost, "/api/perch/batch", bytes.NewReader(raw)))
	var out struct {
		OK     int `json:"ok"`
		Failed int `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK != 0 || out.Failed != 2 {
		t.Fatalf("batch response: %s", rec.Body.String())
	}
	after1, _ := e.srv.perch.keeper.Get(ids[0])
	after2, _ := e.srv.perch.keeper.Get(ids[1])
	if before1 != after1 || before2 != after2 {
		t.Fatal("batch modified a session")
	}
}

func TestPerchManualSessionRejectsMismatchedJWT(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u1")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u1", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	id := rows[0]["id"].(string)
	before, _ := e.srv.perch.keeper.Get(id)
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"other"}`)) + ".signature"
	raw, _ := json.Marshal(map[string]string{"accessToken": token, "refreshToken": "r"})
	r := httptest.NewRequest(http.MethodPut, "/api/perch/accounts/"+id+"/session", bytes.NewReader(raw))
	r.SetPathValue("id", id)
	rec := httptest.NewRecorder()
	e.srv.apiPerchUpdateSession(rec, r)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "identity") {
		t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
	}
	after, _ := e.srv.perch.keeper.Get(id)
	if before != after {
		t.Fatal("manual update modified session")
	}
}

func TestPerchManualSessionRequiresBothTokens(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u1")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u1", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	id := e.ownAccountID()
	before, _ := e.srv.perch.keeper.Get(id)
	cases := []struct {
		name string
		body map[string]string
	}{
		{"access only", map[string]string{"accessToken": "new", "refreshToken": " "}},
		{"refresh only", map[string]string{"accessToken": " ", "refreshToken": "new"}},
		{"json access only", map[string]string{"json": `{"account":{"supabase_user_id":"u1"},"session":{"access_token":"new"}}`}},
		{"json refresh only", map[string]string{"json": `{"account":{"supabase_user_id":"u1"},"session":{"refresh_token":"new"}}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.body)
			r := httptest.NewRequest(http.MethodPut, "/api/perch/accounts/"+id+"/session", bytes.NewReader(raw))
			r.SetPathValue("id", id)
			rec := httptest.NewRecorder()
			e.srv.apiPerchUpdateSession(rec, r)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s", rec.Code, rec.Body.String())
			}
			after, _ := e.srv.perch.keeper.Get(id)
			if after != before {
				t.Fatal("partial session replaced the original")
			}
		})
	}
}

func TestPerchBatchSessionRequiresBothTokens(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u1")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u1", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	id := e.ownAccountID()
	before, _ := e.srv.perch.keeper.Get(id)
	for _, session := range []map[string]string{{"accessToken": "new", "refreshToken": " "}, {"accessToken": " ", "refreshToken": "new"}} {
		raw, _ := json.Marshal(map[string]any{"action": "session", "ids": []string{id}, "session": session})
		rec := httptest.NewRecorder()
		e.srv.apiPerchBatch(rec, httptest.NewRequest(http.MethodPost, "/api/perch/batch", bytes.NewReader(raw)))
		var out struct {
			OK     int `json:"ok"`
			Failed int `json:"failed"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.OK != 0 || out.Failed != 1 {
			t.Fatalf("response = %s", rec.Body.String())
		}
		after, _ := e.srv.perch.keeper.Get(id)
		if after != before {
			t.Fatal("partial batch session replaced the original")
		}
	}
}

// A successful batch quota sync must not panic: the detail is empty, not the
// result of calling Error() on a nil error.
func TestPerchBatchQuotaSuccessDoesNotPanic(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("u-quota")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("u-quota", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	_, rows := e.accounts("")
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	id := rows[0]["id"].(string)

	raw, _ := json.Marshal(map[string]any{"action": "quota", "ids": []string{id}})
	r := httptest.NewRequest(http.MethodPost, "/api/perch/batch", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.srv.apiPerchBatch(rec, r)
	}()
	<-done
	if rec.Code != http.StatusOK {
		t.Fatalf("batch: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Results []struct {
			ID     string `json:"id"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(out.Results) != 1 || !out.Results[0].OK {
		t.Fatalf("results = %+v", out.Results)
	}
	if out.Results[0].Detail != "" {
		t.Fatalf("detail = %q, want empty on success", out.Results[0].Detail)
	}
}
