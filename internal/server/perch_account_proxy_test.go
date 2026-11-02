package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPerchAccountProxyManagement(t *testing.T) {
	e := newPerchEnv(t)
	e.fake.seed("proxy-user")
	e.importFiles(map[string][]byte{"a.json": []byte(exportDoc("proxy-user", e.fake, time.Now().Add(time.Hour).Unix(), 0))})
	id := e.ownAccountID()
	update := func(body string, status int) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodPut, "/api/perch/accounts/"+id, strings.NewReader(body))
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		e.srv.apiPerchUpdateAccount(w, r)
		if w.Code != status {
			t.Fatalf("update: status %d, body %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	proxy := "socks5://secretuser:secretpass@proxy.example:1080/private?token=secretquery#secretfragment"
	update(`{"proxyUrl":"`+proxy+`"}`, http.StatusOK)
	check := func(want string) {
		t.Helper()
		a, ok := e.srv.cfg.Get().Account(id)
		if !ok || a.ProxyURL != want {
			t.Fatalf("stored proxy = %q, want %q", a.ProxyURL, want)
		}
	}
	check(proxy)
	for _, path := range []string{"/api/perch/accounts", "/api/perch/accounts/" + id} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		if strings.HasSuffix(path, id) {
			e.srv.apiPerchAccount(w, r)
		} else {
			e.srv.apiPerchAccounts(w, r)
		}
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `"hasProxy":true`) || !strings.Contains(body, `socks5://***@proxy.example:1080`) {
			t.Fatalf("view: %d %s", w.Code, body)
		}
		for _, secret := range []string{"secretuser", "secretpass", "private", "secretquery", "secretfragment"} {
			if strings.Contains(body, secret) {
				t.Fatalf("view leaked %s: %s", secret, body)
			}
		}
	}
	update(`{"name":"renamed"}`, http.StatusOK)
	update(`{"proxyUrl":null}`, http.StatusOK)
	check(proxy)
	body := update(`{"proxyUrl":"ftp://secretuser:secretpass@example.com"}`, http.StatusBadRequest)
	if strings.Contains(body, "secretuser") || strings.Contains(body, "secretpass") {
		t.Fatal("validation leaked credentials")
	}
	check(proxy)
	update(`{"proxyUrl":""}`, http.StatusOK)
	check("")
	_, rows := e.accounts("")
	if rows[0]["hasProxy"] != false || rows[0]["proxyUrl"] != "" {
		t.Fatalf("direct view: %v", rows[0])
	}
}
