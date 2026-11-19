package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Factory answers a WorkOS token without an organization with a plain 401.
// The console must say why instead of showing a bare HTTP status.
func mockWorkOSNoOrg(t *testing.T) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/user_management/authorize/device", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"device_code": "d", "user_code": "AAAA-BBBB", "verification_uri": "https://auth.example/device", "expires_in": 30, "interval": 1})
	})
	mux.HandleFunc("/user_management/authenticate", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  factoryLoginJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "sub": "user-x", "email": "nobody@example.com"}),
			"refresh_token": "r1",
		})
	})
	return mux
}

func TestFactoryLoginReportsMissingOrganization(t *testing.T) {
	e := newFactoryEnv(t)
	workos := httptest.NewServer(mockWorkOSNoOrg(t))
	defer workos.Close()
	e.s.factoryAuthBase = workos.URL + "/user_management"
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/cli/whoami" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"User not affiliated with an organization","status":401,"title":"Unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}
	e.mu.Unlock()
	w := deployAdmin(t, e, "POST", "/api/factory/login", map[string]any{"group": "factory"})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var flow factoryLoginFlow
	if json.Unmarshal(w.Body.Bytes(), &flow) != nil {
		t.Fatal(w.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for flow.Status == "pending" && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		w = deployAdmin(t, e, "GET", "/api/factory/login/"+flow.ID, nil)
		_ = json.Unmarshal(w.Body.Bytes(), &flow)
	}
	if flow.Status != "failed" {
		t.Fatalf("expected failure, got %+v", flow)
	}
	if !strings.Contains(flow.Error, "User not affiliated with an organization") || !strings.Contains(flow.Error, "nobody@example.com") {
		t.Fatalf("error does not explain the cause: %q", flow.Error)
	}
	if len(e.s.cfg.Get().Accounts) != 2 {
		t.Fatal("a rejected login must not add an account")
	}
}
