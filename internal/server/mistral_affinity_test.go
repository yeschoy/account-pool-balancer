package server

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestEnsureMistralXAffinity(t *testing.T) {
	for _, tc := range []struct{ body, supplied, want string }{
		{`{"prompt_cache_key":"body-cache"}`, "client-affinity", "client-affinity"},
		{`{"prompt_cache_key":"session-42"}`, "", "session-42"},
		{`{}`, "", ""}, {`{"prompt_cache_key":null}`, "", ""},
		{`{"prompt_cache_key":"   "}`, "", ""}, {`not-json`, "", ""},
	} {
		h := http.Header{}
		if tc.supplied != "" {
			h.Set("X-Affinity", tc.supplied)
		}
		ensureMistralXAffinity(h, []byte(tc.body))
		if h.Get("X-Affinity") != tc.want {
			t.Fatal("deployed affinity behavior changed")
		}
	}
}

func TestMistralOutboundXAffinityFromFinalCache(t *testing.T) {
	e := newMistralEnv(t)
	st, _ := e.post(t, "/v1/chat/completions", `{"model":"m","messages":[{"role":"user","content":"mock"}]}`, map[string]string{"X-Session-Id": "private-session"})
	call := e.call(0)
	var body map[string]any
	if st != 200 || json.Unmarshal([]byte(call.body), &body) != nil || body["prompt_cache_key"] == "" || call.header.Get("X-Affinity") != body["prompt_cache_key"] {
		t.Fatal("deployed final cache key was not propagated as affinity")
	}
}
