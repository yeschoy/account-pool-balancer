package perch

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// HTTP/1.1 observations are limited to sequential, fully consumed replies.
func TestEgressConnectionAndProtocolRegression(t *testing.T) {
	type observation struct {
		remote, path string
		header       http.Header
		body         map[string]any
	}
	seen := make(chan observation, 16)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil && r.ContentLength > 0 {
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error("invalid fixture request body")
			}
		}
		seen <- observation{r.RemoteAddr, r.URL.Path, r.Header.Clone(), body}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/v1/user":
			fmt.Fprint(w, `{"id":"fixture-user"}`)
		case "/auth/v1/token":
			fmt.Fprint(w, `{"access_token":"fixture-next-access","refresh_token":"fixture-next-refresh"}`)
		case ModelCallPath:
			fmt.Fprint(w, `{"type":"done","ok":true,"text":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	})
	origin := httptest.NewServer(handler)
	defer origin.Close()
	proxy := httptest.NewServer(handler)
	defer proxy.Close()
	c := NewClient(&http.Client{Transport: &http.Transport{Proxy: nil}})
	t.Cleanup(func() {
		c.HTTP.CloseIdleConnections()
		for _, client := range c.proxies {
			client.CloseIdleConnections()
		}
	})
	user := func(proxyURL, token string) observation {
		t.Helper()
		_, err := c.User(context.Background(), Creds{SupabaseURL: origin.URL, SupabaseKey: "fixture-key", ProxyURL: proxyURL}, token)
		if err != nil {
			t.Fatalf("user request failed: %v", err)
		}
		return <-seen
	}
	direct1 := user("", "fixture-access-a")
	direct2 := user("", "fixture-access-b")
	p1 := user(proxy.URL, "fixture-access-a")
	p2 := user(proxy.URL, "fixture-access-b")
	// Distinct full URLs address the same local proxy but own separate pools.
	alternate := strings.Replace(proxy.URL, "http://", "http://fixture-user:fixture-password@", 1)
	p3 := user(alternate, "fixture-access-c")
	if direct1.remote != direct2.remote || p1.remote != p2.remote {
		t.Fatal("sequential requests did not reuse the expected HTTP/1.1 connection")
	}
	if p1.remote == p3.remote {
		t.Fatal("different full proxy URLs unexpectedly reused a connection")
	}
	for i, o := range []observation{direct1, direct2, p1, p2, p3} {
		tokens := []string{"fixture-access-a", "fixture-access-b", "fixture-access-a", "fixture-access-b", "fixture-access-c"}
		if o.path != "/auth/v1/user" || o.header.Get("Apikey") != "fixture-key" || o.header.Get("Authorization") != "Bearer "+tokens[i] || o.header.Get("User-Agent") != UserAgent || o.header.Get("Cookie") != "" {
			t.Errorf("user request %d did not match credential/header contract", i)
		}
	}
	_, err := c.Renew(context.Background(), Creds{SupabaseURL: origin.URL, SupabaseKey: "fixture-key", ProxyURL: proxy.URL}, "fixture-refresh")
	if err != nil {
		t.Fatalf("refresh failed: %v", err)
	}
	r := <-seen
	if r.remote != p1.remote || r.path != "/auth/v1/token" || r.header.Get("Authorization") != "Bearer fixture-key" || r.header.Get("Apikey") != "fixture-key" || r.header.Get("Content-Type") != "application/json" || r.body["refresh_token"] != "fixture-refresh" || len(r.body) != 1 {
		t.Error("refresh protocol or shared proxy connection changed")
	}
	_, err = c.CallModel(context.Background(), ModelCall{Surface: SurfaceCLI, Base: origin.URL, ProxyURL: proxy.URL, Token: "fixture-model-token", Body: []byte(`{"fixture":"value"}`)}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("model call failed: %v", err)
	}
	m := <-seen
	if m.remote != p1.remote || m.path != ModelCallPath || m.header.Get("Authorization") != "Bearer fixture-model-token" || m.header.Get("Content-Type") != "application/json" || m.header.Get("Accept") != "text/event-stream" || m.header.Get("X-Perch-Call-Delta") != "1" || m.header.Get("X-Perch-Locale") != "en" || m.header.Get("User-Agent") != UserAgent || m.header.Get("Cookie") != "" || m.body["fixture"] != "value" {
		t.Error("model protocol or shared proxy connection changed")
	}
}

// A fresh process avoids ProxyFromEnvironment's cache. A non-loopback name
// exercises proxy selection; the dialer allows only the two local fixtures.
func TestEgressEnvironmentProxyRegression(t *testing.T) {
	if os.Getenv("PERCH_EGRESS_CHILD") == "1" {
		c := NewClient(nil)
		tr := c.HTTP.Transport.(*http.Transport)
		tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			switch address {
			case "fixture.invalid:80":
				address = os.Getenv("PERCH_ORIGIN")
			case os.Getenv("PERCH_PROXY_ADDR"):
			default:
				return nil, fmt.Errorf("blocked non-fixture dial")
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		}
		defer c.HTTP.CloseIdleConnections()
		got, err := c.User(context.Background(), Creds{SupabaseURL: "http://fixture.invalid", SupabaseKey: "fixture-key"}, "fixture-token")
		if err != nil {
			t.Fatalf("fixture request failed: %v", err)
		}
		if got.UserID != os.Getenv("PERCH_EXPECT") {
			t.Fatalf("route = %q, want %q", got.UserID, os.Getenv("PERCH_EXPECT"))
		}
		return
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"id":"origin"}`) }))
	defer origin.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"id":"proxy"}`) }))
	defer proxy.Close()
	for _, tc := range []struct{ name, proxy, noProxy, want string }{
		{"unset", "", "", "origin"},
		{"set", proxy.URL, "", "proxy"},
		{"no_proxy_match", proxy.URL, "fixture.invalid", "origin"},
		{"no_proxy_other", proxy.URL, "other.invalid", "proxy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestEgressEnvironmentProxyRegression$", "-test.count=1")
			cmd.Env = []string{
				"PERCH_EGRESS_CHILD=1", "PERCH_ORIGIN=" + strings.TrimPrefix(origin.URL, "http://"),
				"PERCH_PROXY_ADDR=" + strings.TrimPrefix(proxy.URL, "http://"), "PERCH_EXPECT=" + tc.want,
				"HTTP_PROXY=" + tc.proxy, "HTTPS_PROXY=", "NO_PROXY=" + tc.noProxy,
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("isolated child failed: %v\n%s", err, out)
			}
		})
	}
}
