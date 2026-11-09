package server

import (
	"account-pool-balancer/internal/config"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestSkipStringPreservesEscapedQuoteAndSlashBoundaries(t *testing.T) {
	for n := 0; n < 16; n++ {
		text := strings.Repeat("\\", n) + "\"quote\"\n中文" + strings.Repeat("\\", n)
		raw, _ := json.Marshal(text)
		input := append(bytes.Clone(raw), []byte(",next")...)
		if skipString(input, 0) != len(raw) {
			t.Fatalf("wrong quote boundary for %q", raw)
		}
	}
	if skipString([]byte(`"unfinished\"`), 0) != -1 {
		t.Fatal("accepted unfinished string")
	}
}
func TestLazyMistralMetadataKeepsOnlyRoutingScalars(t *testing.T) {
	raw := []byte(`{"model":"m","metadata":{"session_id":"stable","user_id":"user_id_session_12345678-abcd","other":{"large":[1,2,{"unknown":"preserve"}]},"thread_id":{"invalid":"not a session"}},"messages":[{"role":"user","content":"hello"}]}`)
	eager, err := parseRoutingBody(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	lazy, err := parseRoutingBodyWithHistory(raw, true, false)
	if err != nil {
		t.Fatal(err)
	}
	meta := lazy["metadata"].(map[string]any)
	if _, ok := meta["other"]; ok {
		t.Fatal("arbitrary metadata decoded")
	}
	s := &Server{secret: []byte("synthetic-routing-secret")}
	cfg := config.Default()
	if a, b := s.mistralIdentity(cfg, "g", http.Header{}, eager), s.mistralIdentity(cfg, "g", http.Header{}, lazy); a != b {
		t.Fatal("metadata identity changed")
	}
	prepared, _, err := prepareMistralRequest(raw, "/v1/chat/completions", "")
	if err != nil || !bytes.Equal(prepared, raw) {
		t.Fatal("lazy projection changed native wire")
	}
	if _, err := parseRoutingBodyWithHistory([]byte(`{"metadata":{"invalid":}}`), false, false); err == nil {
		t.Fatal("unchecked malformed metadata")
	}
}
