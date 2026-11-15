package x50reg

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeSOCKS5Gateway answers the SOCKS5 greeting with 0x05 0x00 so it reads as a
// real socks endpoint.
func fakeSOCKS5Gateway(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 3)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				_, _ = c.Write([]byte{0x05, 0x00})
			}(c)
		}
	}()
	return ln.Addr().String()
}

// fakeHTTPGateway behaves like an HTTP CONNECT proxy: it replies to the SOCKS
// greeting with a plaintext HTTP status, which is exactly what a gateway such as
// zooproxy does. That reply is what the old code surfaced as
// "server gave HTTP response to HTTPS client".
func fakeHTTPGateway(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("403 Forbidden"))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func TestDetectProxySchemeDistinguishesSOCKS5AndHTTP(t *testing.T) {
	socks := fakeSOCKS5Gateway(t)
	if got, err := DetectProxyScheme(socks); err != nil || got != SchemeSOCKS5 {
		t.Fatalf("socks gateway: got %v err=%v", got, err)
	}
	hgw := fakeHTTPGateway(t)
	if got, err := DetectProxyScheme(hgw); err != nil || got != SchemeHTTP {
		t.Fatalf("http gateway: got %v err=%v", got, err)
	}
}

func TestCoerceProxySchemeRewritesMismatchOnly(t *testing.T) {
	hgw := fakeHTTPGateway(t)

	// A socks5 label on an HTTP-only gateway is the case that broke a 499-row run.
	in := "socks5://user-region-Rand-sid-aaaaaaaa-t-10:secret@" + hgw
	out, changed := CoerceProxyScheme(in)
	if !changed {
		t.Fatal("socks5 labelled on an http gateway must be corrected")
	}
	if !strings.HasPrefix(out, "http://") {
		t.Fatalf("scheme not rewritten: %s", out)
	}
	// Credentials and the sticky-session user must survive the rewrite.
	if !strings.Contains(out, "user-region-Rand-sid-aaaaaaaa-t-10:secret@") || !strings.Contains(out, hgw) {
		t.Fatalf("credentials or gateway lost: %s", out)
	}
	// Already correct: no change, so the sticky HTTP form is preserved verbatim.
	if again, changedAgain := CoerceProxyScheme(out); changedAgain || again != out {
		t.Fatalf("correct proxy must pass through unchanged: %s changed=%v", again, changedAgain)
	}

	socks := fakeSOCKS5Gateway(t)
	keep := "socks5://u:p@" + socks
	if got, ch := CoerceProxyScheme(keep); ch || got != keep {
		t.Fatalf("working socks5 must keep its scheme: %s changed=%v", got, ch)
	}
	// A bare host:port defaults to socks5, so an HTTP gateway still corrects it.
	bare := "u:p@" + hgw
	if got, ch := CoerceProxyScheme(bare); !ch || !strings.HasPrefix(got, "http://") {
		t.Fatalf("bare host:port on an http gateway must be corrected: %s changed=%v", got, ch)
	}
}

func TestCoerceProxySchemeLeavesUnreachableAlone(t *testing.T) {
	// Nothing listens here; a probe failure must not rewrite the configured URL to
	// a guess.
	raw := "socks5://u:p@127.0.0.1:1"
	if got, changed := CoerceProxyScheme(raw); changed || got != raw {
		t.Fatalf("unreachable must be returned unchanged: %s changed=%v", got, changed)
	}
	if got, changed := CoerceProxyScheme(""); changed || got != "" {
		t.Fatal("empty proxy stays empty")
	}
}

func TestProxyPoolCoerceAllRewritesEveryEntry(t *testing.T) {
	hgw := fakeHTTPGateway(t)
	a := "socks5://u1:p1@" + hgw
	b := "socks5://u2:p2@" + hgw
	p := NewProxyPool([]string{a, b, "socks5://u3:p3@127.0.0.1:1"})

	coerced, unreachable := p.CoerceAll()
	if len(coerced) != 2 {
		t.Fatalf("want 2 corrections, got %v", coerced)
	}
	if _, ok := coerced[a]; !ok {
		t.Fatalf("first proxy not corrected: %v", coerced)
	}
	if _, ok := unreachable["socks5://u3:p3@127.0.0.1:1"]; !ok {
		t.Fatalf("dead proxy must be reported: %v", unreachable)
	}
	// The pool now serves corrected URLs, and Resolve answers for the old form so
	// rows already bound to it work without re-probing.
	for i := 0; i < 2; i++ {
		if got := p.Pick(); !strings.HasPrefix(got, "http://") {
			t.Fatalf("pool still serves an uncorrected URL: %s", got)
		}
	}
	if got := p.Resolve(a); !strings.HasPrefix(got, "http://") {
		t.Fatalf("Resolve must map the old URL: %s", got)
	}
}
