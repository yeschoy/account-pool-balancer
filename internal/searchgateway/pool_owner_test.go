package searchgateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRequiredPoolOwnerRejectsOldPoolBeforeInference(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pool-capabilities" {
			w.WriteHeader(404)
			return
		}
		calls.Add(1)
	}))
	defer s.Close()
	c := configFor(t, s.URL, "compat")
	c.Routes[0].RequirePoolOwner = true
	g := startGateway(t, c, searchFunc(searchOK))
	w := do(t, g, "route0", requestJSON, "s")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "pool_owner_unsupported") || calls.Load() != 0 {
		t.Fatalf("old pool generated unpinned inference: %d %s", w.Code, w.Body.String())
	}
}
func TestPoolOwnerResponseMustBeStable(t *testing.T) {
	token := "ow1." + strings.Repeat("a", 64)
	s := &ownerTokenState{token: "start"}
	response := &http.Response{Header: make(http.Header)}
	response.Header.Set(ownerHeader, token)
	if e := acceptPoolOwner(response, s); e != nil {
		t.Fatal(e)
	}
	response.Header.Set(ownerHeader, "ow1."+strings.Repeat("b", 64))
	if e := acceptPoolOwner(response, s); e == nil {
		t.Fatal("account owner silently changed")
	}
	response.Header.Del(ownerHeader)
	if e := acceptPoolOwner(response, s); e == nil {
		t.Fatal("missing safety token ignored")
	}
}
