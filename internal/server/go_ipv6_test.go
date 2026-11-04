package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func ipv6TestListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// Both ends are local test sockets. Closing either direction tears down the
// other and joins the copy goroutine, including keep-alive connections.
func pipeIPv6TestTunnel(client, target net.Conn, reader io.Reader) {
	defer client.Close()
	defer target.Close()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(target, reader)
		_ = target.Close()
		close(done)
	}()
	_, _ = io.Copy(client, target)
	_ = client.Close()
	_ = target.Close()
	<-done
}

func TestGoIPv6FixedProxyInferenceMetadataAndPin(t *testing.T) {
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			ln := ipv6TestListener(t)
			e := newGoGatewayEnv(t)
			e.front.Client().Timeout = 5 * time.Second
			var calls atomic.Int32
			var ipv4Exit atomic.Bool
			up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Proxy-Authorization") != "" {
					t.Error("proxy credentials leaked to upstream")
				}
				if r.URL.Path == "/ip" {
					if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
						t.Error("IP probe carried credentials")
					}
					if ipv4Exit.Load() {
						fmt.Fprint(w, "::ffff:203.0.113.7")
					} else {
						fmt.Fprint(w, `{"ip":"2001:0db8:0:0:0:0:0:77"}`)
					}
					return
				}
				if r.URL.Path == "/docs" {
					fmt.Fprint(w, "| Chat | chat | `https://docs-only.example/v1/chat/completions` |")
					return
				}
				if strings.HasPrefix(r.URL.Path, "/zen/go/v1/") && r.Header.Get("Authorization") != "Bearer opaque-go-secret-key" {
					t.Error("Go authentication lost through IPv6 proxy")
				}
				e.up.Config.Handler.ServeHTTP(w, r)
			}))
			_ = up.Listener.Close()
			up.Listener = ln
			up.StartTLS()
			t.Cleanup(up.Close)
			roots := x509.NewCertPool()
			roots.AddCert(up.Certificate())
			proxyURL, closeProxy := newIPv6TestProxy(t, scheme, ln.Addr().String(), roots)
			_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				c.OpenCode.GoBase = up.URL + "/zen/go/v1"
				c.OpenCode.CapabilitiesURL = up.URL + "/capabilities"
				c.OpenCode.ZenDocsURL, c.OpenCode.GoDocsURL = up.URL+"/docs", up.URL+"/docs"
				c.OpenCode.GoEgressCheckURL = up.URL + "/ip"
				for i := range c.Accounts {
					if c.Accounts[i].ID == "go1" {
						c.Accounts[i].ProxyURL, c.Accounts[i].EgressIP = proxyURL, "2001:db8::77"
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			tr := e.s.transport(proxyURL)
			tr.TLSClientConfig = &tls.Config{RootCAs: roots}
			t.Cleanup(tr.CloseIdleConnections)
			cfg := e.s.cfg.Get()
			a, _ := cfg.Account("go1")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := e.s.quota.RefreshNow(ctx, a); err != nil {
				t.Fatal(err)
			}
			body := `{"model":"chat","messages":[{"role":"user","content":"local IPv6 mock"}]}`
			if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, nil); st != http.StatusOK {
				t.Fatalf("Go inference through IPv6 proxy: %d", st)
			}
			q := e.s.quota.Get("go1")
			if q == nil || q.OpenCode == nil || q.OpenCode.Usage == nil || len(q.OpenCode.Models) == 0 {
				t.Fatal("IPv6-proxied metadata/usage missing")
			}
			rows := goLogRows(t, e.s, "requests", map[string]string{"provider": config.ProviderOpenCodeGo}, 1)
			if rows[0]["egressIp"] != "2001:db8::77" || rows[0]["egressStatus"] != "verified" || strings.Contains(fmt.Sprint(rows), "fake-proxy-password") {
				t.Fatal("IPv6 egress logging or credential redaction failed")
			}
			ipv4Exit.Store(true)
			if e.s.ensureGoEgress(ctx, cfg, a, true) == nil || e.s.goEgressView(cfg, a).Status != "mismatch" {
				t.Fatal("IPv6 pin accepted an IPv4 exit")
			}
			before := calls.Load()
			if st, _, _ := e.post(t, config.ProviderOpenCodeGo, "/v1/chat/completions", body, nil); st != 503 || calls.Load() != before {
				t.Fatal("mismatched IPv6 pin did not block inference locally")
			}
			tr.CloseIdleConnections()
			closeProxy()
			if e.s.ensureGoEgress(ctx, cfg, a, true) == nil || calls.Load() != before {
				t.Fatal("unavailable IPv6 proxy bypassed by a direct connection")
			}
		})
	}
}

// The proxy accepts only this test's IPv6 target, never an external address.
func newIPv6TestProxy(t *testing.T, scheme, targetAddr string, roots *x509.CertPool) (string, func()) {
	t.Helper()
	const user, password = "fake-proxy-user", "fake-proxy-password"
	ln := ipv6TestListener(t)
	var wg sync.WaitGroup
	var closeOnce sync.Once
	var closeProxy func()
	if scheme == "http" || scheme == "https" {
		proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			wg.Add(1)
			defer wg.Done()
			if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+password)) {
				t.Error("IPv6 CONNECT proxy authentication missing")
				w.WriteHeader(407)
				return
			}
			if r.Method != http.MethodConnect || r.Host != targetAddr {
				t.Error("proxy was not given the bracketed IPv6 target")
				w.WriteHeader(400)
				return
			}
			dst, err := net.DialTimeout("tcp6", targetAddr, time.Second)
			if err != nil {
				w.WriteHeader(502)
				return
			}
			client, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				_ = dst.Close()
				return
			}
			_ = client.SetDeadline(time.Now().Add(10 * time.Second))
			_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
			_ = rw.Flush()
			pipeIPv6TestTunnel(client, dst, rw.Reader)
		}))
		_ = proxy.Listener.Close()
		proxy.Listener = ln
		if scheme == "https" {
			proxy.StartTLS()
			roots.AddCert(proxy.Certificate())
		} else {
			proxy.Start()
		}
		closeProxy = func() { closeOnce.Do(func() { proxy.Close(); wg.Wait() }) }
	} else {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
					var head [2]byte
					if _, err := io.ReadFull(conn, head[:]); err != nil || head[0] != 5 {
						return
					}
					methods := make([]byte, head[1])
					if _, err := io.ReadFull(conn, methods); err != nil {
						return
					}
					_, _ = conn.Write([]byte{5, 2})
					if _, err := io.ReadFull(conn, head[:]); err != nil || head[0] != 1 {
						return
					}
					u := make([]byte, head[1])
					if _, err := io.ReadFull(conn, u); err != nil {
						return
					}
					var size [1]byte
					if _, err := io.ReadFull(conn, size[:]); err != nil {
						return
					}
					p := make([]byte, size[0])
					if _, err := io.ReadFull(conn, p); err != nil || string(u) != user || string(p) != password {
						t.Error("IPv6 SOCKS authentication failed")
						return
					}
					_, _ = conn.Write([]byte{1, 0})
					var request [22]byte // version/cmd/reserved/type + IPv6 + port
					if _, err := io.ReadFull(conn, request[:]); err != nil || request[0] != 5 || request[1] != 1 || request[3] != 4 {
						t.Error("SOCKS proxy was not given an IPv6 destination")
						return
					}
					addr := net.JoinHostPort(net.IP(request[4:20]).String(), fmt.Sprint(int(request[20])<<8|int(request[21])))
					if addr != targetAddr {
						t.Error("unexpected SOCKS destination")
						return
					}
					dst, err := net.DialTimeout("tcp6", targetAddr, time.Second)
					if err != nil {
						return
					}
					reply := make([]byte, 22)
					reply[0], reply[3], reply[19] = 5, 4, 1
					_, _ = conn.Write(reply)
					pipeIPv6TestTunnel(conn, dst, conn)
				}()
			}
		}()
		closeProxy = func() { closeOnce.Do(func() { _ = ln.Close(); wg.Wait() }) }
	}
	t.Cleanup(closeProxy)
	u := &url.URL{Scheme: scheme, Host: ln.Addr().String(), User: url.UserPassword(user, password)}
	return u.String(), closeProxy
}
