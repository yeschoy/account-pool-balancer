package server

import (
	"net/http"
	"testing"

	"account-pool-balancer/internal/config"
)

func TestOpenCodeParentChildIdentityIsKeyScoped(t *testing.T) {
	s := &Server{secret: []byte("test-identity-secret")}
	cfg := config.Default()
	parent := s.identity(cfg, "opencode-zen\x00client-1", http.Header{"X-Opencode-Session-Id": []string{"parent"}}, map[string]any{})
	child := s.identity(cfg, "opencode-zen\x00client-1", http.Header{"X-Opencode-Session-Id": []string{"child"}, "X-Opencode-Parent-Session-Id": []string{"parent"}}, map[string]any{})
	bodyChild := s.identity(cfg, "opencode-zen\x00client-1", http.Header{}, map[string]any{"metadata": map[string]any{"session_id": "child", "parent_session_id": "parent"}})
	other := s.identity(cfg, "opencode-zen\x00client-2", http.Header{"X-Opencode-Session-Id": []string{"parent"}}, map[string]any{})
	if parent.Fingerprint == "" || parent.Fingerprint != child.Fingerprint || parent.Fingerprint != bodyChild.Fingerprint || parent.Fingerprint == other.Fingerprint {
		t.Fatal("parent/child or downstream key isolation failed")
	}
}
