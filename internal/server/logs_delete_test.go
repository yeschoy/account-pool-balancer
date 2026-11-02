package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

// logIDs returns one identity field per stored record, newest first.
func logIDs(t *testing.T, e *env, kind, field string) []string {
	t.Helper()
	page, err := e.srv.logs.Query(kind, nil, time.Time{}, time.Time{}, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(page.Items))
	for _, raw := range page.Items {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		s, _ := m[field].(string)
		out = append(out, s)
	}
	return out
}

func deleteLogReq(s *Server, kind, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodDelete, "/api/logs/"+kind+"/log/"+id, nil)
	req.SetPathValue("kind", kind)
	req.SetPathValue("id", id)
	out := httptest.NewRecorder()
	if kind == "perch" {
		s.apiDeletePerchLog(out, req)
	} else {
		s.apiDeleteLog(out, req)
	}
	return out
}

// Delete removes exactly one request record, keeping its neighbours.
func TestDeleteRequestLogByID(t *testing.T) {
	e := newEnv(t, []config.Account{{ID: "a"}}, nil)
	for i := 0; i < 2; i++ {
		if resp := e.post("/v1/chat/completions", chat(false), nil); resp.StatusCode != 200 {
			t.Fatalf("chat %d", resp.StatusCode)
		}
	}
	ids := logIDs(t, e, "requests", "requestId")
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("need two distinct request logs, got %v", ids)
	}
	if out := deleteLogReq(e.srv, "requests", ids[0]); out.Code != 200 || !strings.Contains(out.Body.String(), `"memory":1`) {
		t.Fatalf("delete %d %s", out.Code, out.Body.String())
	}
	if left := logIDs(t, e, "requests", "requestId"); len(left) != 1 || left[0] != ids[1] {
		t.Fatalf("remaining = %v, want %v", left, ids[1])
	}
	if out := deleteLogReq(e.srv, "requests", ids[0]); out.Code != http.StatusNotFound {
		t.Fatalf("second delete %d %s", out.Code, out.Body.String())
	}
	if out := deleteLogReq(e.srv, "bogus", "x"); out.Code != http.StatusNotFound {
		t.Fatalf("unknown kind %d", out.Code)
	}
}

// A value that only appears inside another record must not delete that record.
func TestDeleteLogMatchesIdentityFieldOnly(t *testing.T) {
	e := newEnv(t, []config.Account{{ID: "a"}}, nil)
	if resp := e.post("/v1/chat/completions", chat(false), nil); resp.StatusCode != 200 {
		t.Fatalf("chat %d", resp.StatusCode)
	}
	if ids := logIDs(t, e, "requests", "requestId"); len(ids) != 1 || ids[0] == "" {
		t.Fatalf("ids %v", ids)
	}
	// The client key is named inside the record but is not its identity.
	if out := deleteLogReq(e.srv, "requests", e.key); out.Code != http.StatusNotFound {
		t.Fatalf("client key resolved to a record: %d %s", out.Code, out.Body.String())
	}
	if len(logIDs(t, e, "requests", "requestId")) != 1 {
		t.Fatal("record removed by a body value")
	}
}
