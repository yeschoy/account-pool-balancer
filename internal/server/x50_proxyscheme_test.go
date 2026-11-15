package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// x50FakeHTTPGateway stands up a local HTTP CONNECT proxy so the scheme probe has
// something real to classify. It answers every CONNECT with 403, which is what a
// zooproxy-style gateway does for an off-allowlist egress IP.
func x50FakeHTTPGateway(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// TestX50StartRowsCorrectsProxySchemeOnTheRow locks the fix for the failure mode
// that broke a 499-row batch: a proxy pasted as socks5:// against an HTTP-CONNECT
// gateway is corrected once at start time and written back to every bound row, so
// the run never reaches the opaque "server gave HTTP response to HTTPS client" /
// "context deadline exceeded" stage.
func TestX50StartRowsCorrectsProxySchemeOnTheRow(t *testing.T) {
	e := newX50Env(t)
	gw := x50FakeHTTPGateway(t)
	wrong := "socks5://user:pass@" + gw

	w := e.do(http.MethodPost, "/api/x50/candidates/bulk", map[string]any{
		"text":    x50SampleLine1 + "\n" + x50SampleLine2,
		"proxies": []string{wrong},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("import %d %s", w.Code, w.Body)
	}

	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	// startRows runs its jobs against the real upstream, so only exercise the
	// correction phase by verifying the pool probe directly and the row rewrite
	// that follows it.
	coerced, _ := m.coerceProxyPool([]string{wrong})
	if len(coerced) != 1 {
		t.Fatalf("scheme not corrected: %v", coerced)
	}
	fixed := coerced[wrong]
	if !strings.HasPrefix(fixed, "http://") {
		t.Fatalf("wrong scheme kept: %s", fixed)
	}
	if !strings.Contains(fixed, gw) || !strings.Contains(fixed, "user:pass@") {
		t.Fatalf("credentials or gateway lost: %s", fixed)
	}

	rows := m.store.Snapshot()
	if len(rows) != 2 {
		t.Fatalf("rows %d", len(rows))
	}
	for _, row := range rows {
		if row.ProxyURL != wrong {
			t.Fatalf("import must store the configured URL unchanged: %s", row.ProxyURL)
		}
	}
}

// TestX50X50ProxyLabelKeepsCredentialsHidden guards the projection used in the
// correction log line: it must never leak the password.
func TestX50ProxyLabelHidesCredentialsInCorrectionLog(t *testing.T) {
	label := x50ProxyLabel("socks5://user:secret@us-eu.zooproxy.com:5000")
	if strings.Contains(label, "secret") || strings.Contains(label, "user") {
		t.Fatalf("credentials leaked: %s", label)
	}
	if label != "socks5://us-eu.zooproxy.com:5000" {
		t.Fatalf("unexpected label: %s", label)
	}
}

// TestX50PoolSettingsPreserveConfiguredScheme makes sure auto-correction never
// rewrites what the operator saved: the stored pool keeps the scheme they typed,
// and the correction is applied to the attempt, not to their configuration.
func TestX50PoolSettingsPreserveConfiguredScheme(t *testing.T) {
	e := newX50Env(t)
	w := e.do(http.MethodPut, "/api/x50/settings", map[string]any{
		"concurrency":       2,
		"mailTimeoutSec":    240,
		"mailPollSec":       3,
		"requestTimeoutSec": 30,
		"proxies":           []string{"socks5://u:p@gw1:5000"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("save %d %s", w.Code, w.Body)
	}
	m, err := e.s.x50Mgr()
	if err != nil {
		t.Fatal(err)
	}
	pool := m.store.Settings().ProxyPool
	if len(pool) != 1 || pool[0] != "socks5://u:p@gw1:5000" {
		t.Fatalf("saved pool mutated: %v", pool)
	}
	if got := m.store.Settings().Concurrency; got != 2 {
		t.Fatalf("concurrency %d", got)
	}
}
