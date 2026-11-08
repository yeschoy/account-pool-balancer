package server

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestMistralDeepSeekStatelessStoreCompatibility(t *testing.T) {
	for _, body := range []string{
		`{"store":false, "model":"deepseek-v4-1-flash-1017","messages":[],"tools":[{"n":9007199254740993}]}`,
		`{"model":"deepseek-v4-1-flash-1017", "store":null, "messages":[{"role":"assistant","reasoning_content":"thought","content":"answer"}],"reasoning_effort":"medium","tools":[{"n":9007199254740993}]}`,
		"{\"model\":\"deepseek-v4-1-flash-1017\",\"messages\":[],\"tools\":[{\"n\":9007199254740993}], \n\t\"store\":false \n}",
	} {
		out, generated, err := prepareMistralRequest([]byte(body), "/v1/chat/completions", "scope")
		var fields map[string]json.RawMessage
		if err != nil || json.Unmarshal(out, &fields) != nil || generated != "scope" {
			t.Fatalf("invalid root removal: %s %v", out, err)
		}
		if _, exists := fields["store"]; exists || !bytes.Contains(fields["tools"], []byte("9007199254740993")) {
			t.Fatal("store remained or neighboring tools changed")
		}
		if _, exists := fields["reasoning_effort"]; exists && string(fields["reasoning_effort"]) != `"high"` {
			t.Fatal("combined effort/content/store changes overlapped")
		}
	}
	e := newMistralEnv(t)
	st, body := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017","store":false,"messages":[],"metadata":{"store":true}}`, nil)
	if st != 200 || e.count() != 1 || strings.Contains(e.call(0).body, `"store":false`) || !strings.Contains(e.call(0).body, `"metadata":{"store":true}`) {
		t.Fatalf("native attempt/root-only scope lost: %d %s", st, body)
	}
}

func TestMistralStoreTrueAndAmbiguousRejectedLocally(t *testing.T) {
	e := newMistralEnv(t)
	for _, fields := range []string{`"store":true`, `"store":"false"`, `"store":0`, `"store":false,"store":null`} {
		st, body := e.post(t, "/v1/chat/completions", `{"model":"deepseek-v4-1-flash-1017",`+fields+`,"messages":[]}`, nil)
		if st != 400 || e.count() != 0 || !strings.Contains(body, "stateless") {
			t.Fatalf("unsupported persistence reached upstream: %d %s", st, body)
		}
	}
}

func TestMistralStoreOtherModelAndProtocolUnchanged(t *testing.T) {
	for _, tc := range []struct{ model, path string }{
		{"other-model", "/v1/chat/completions"},
		{"deepseek-v4-1-flash-1017", "/v1/responses"},
		{"deepseek-v4-1-flash-1017", "/v1/fim/completions"},
	} {
		body := []byte(`{ "model":"` + tc.model + `", "reasoning":{"effort":"high"}, "store":false, "messages":[] }`)
		out, _, err := prepareMistralRequest(body, tc.path, "")
		if err != nil || !bytes.Equal(out, body) {
			t.Fatal("other model/protocol store behavior changed")
		}
	}
}
