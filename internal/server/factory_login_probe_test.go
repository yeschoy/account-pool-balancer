package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factoryauth"
)

// jwtFor mints a JWT-shaped access token whose iss identifies its WorkOS client.
func jwtFor(clientID string) string {
	payload, _ := json.Marshal(map[string]any{"iss": "https://api.workos.com/user_management/" + clientID, "sub": "user-1"})
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func TestFactoryWhoamiUsesRawBearerAndReportsEnvironment(t *testing.T) {
	e := newFactoryEnv(t)
	var got string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]any{"userId": "u1", "email": "a@example.com", "orgId": "org-1"})
	}))
	defer up.Close()
	if _, err := e.s.cfg.Update(-1, func(c *config.Config) error { c.Factory.BaseURL = up.URL; c.Factory.InferenceURL = up.URL; return nil }); err != nil {
		t.Fatal(err)
	}
	raw := jwtFor(factoryauth.ProductionClientID)
	tok := factoryauth.Token{AccessToken: raw, AccessTokenRaw: raw}
	if _, _, _, err := e.s.factoryWhoamiOAuth(context.Background(), tok, "", "test"); err != nil {
		t.Fatal(err)
	}
	if got != "Bearer "+raw {
		t.Fatalf("whoami must receive the token exactly as issued: %q", got)
	}
	if env := factoryauth.EnvironmentOf(raw); env != "production" {
		t.Fatal("production token misread", env)
	}
	if env := factoryauth.EnvironmentOf(jwtFor(factoryauth.DevClientID)); env != "development" {
		t.Fatal("dev token misread", env)
	}
	if factoryauth.EnvironmentOf("opaque-not-a-jwt") != "" {
		t.Fatal("opaque token must not claim an environment")
	}
}

func TestFactoryLoginDiagnosticMentionsDevClient(t *testing.T) {
	e := newFactoryEnv(t)
	d := e.s.factoryLoginDiagnostic(factoryauth.Token{AccessTokenRaw: jwtFor(factoryauth.DevClientID)})
	if !strings.Contains(d, "development") || !strings.Contains(d, "401") {
		t.Fatal(d)
	}
	if strings.Contains(d, "eyJ") {
		t.Fatal("diagnostic leaked a token")
	}
}
