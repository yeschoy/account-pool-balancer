package searchgateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNullUsageIsUnknownNotZero(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"input_tokens":null,"output_tokens":1,"total_tokens":1}`, `{"input_tokens":1,"output_tokens":1,"total_tokens":null}`} {
		u := newUsageSum()
		u.add(json.RawMessage(raw))
		if u.json() != nil {
			t.Fatalf("unknown usage became zero %s", raw)
		}
	}
}
func TestFailedSecondRoundDoesNotReturnPartialUsageAsTotal(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			reply(w, []json.RawMessage{searchCall("a")}, "completed")
		} else {
			w.WriteHeader(503)
		}
	}))
	defer s.Close()
	g := startGateway(t, configFor(t, s.URL, "compat"), searchFunc(searchOK))
	body := strings.Replace(requestJSON, `"input":"question"`, `"input":"question","stream":true`, 1)
	w := do(t, g, "route0", body, "s")
	if !strings.Contains(w.Body.String(), "response.failed") {
		t.Fatal("missing failure")
	}
	text := w.Body.String()
	last := strings.LastIndex(text, "event: response.failed")
	if strings.Contains(text[last:], `"usage":`) {
		t.Fatal("first-round usage misrepresented as full request total")
	}
}
