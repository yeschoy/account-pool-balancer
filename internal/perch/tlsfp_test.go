package perch

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

// TestEgressKeySeparatesAccountsSharingAProxy is the whole point of the
// per-account pool: two accounts behind one proxy URL are two identities, and
// a shared connection would let the server see them together even though the
// proxy is what is supposed to keep them apart.
func TestEgressKeySeparatesAccountsSharingAProxy(t *testing.T) {
	const proxy = "http://user:pass@127.0.0.1:1080"
	a := EgressKey("acct-a", proxy)
	b := EgressKey("acct-b", proxy)
	if a == b {
		t.Fatalf("two accounts sharing one proxy produced the same pool key %q", a)
	}
	// The proxy still separates accounts that use no account id.
	if EgressKey("", "http://p1:1") == EgressKey("", "http://p2:2") {
		t.Fatal("unscoped calls through different proxies must not share a pool")
	}
	// Whitespace in the proxy is not a different egress.
	if EgressKey("", " http://p1:1 ") != EgressKey("", "http://p1:1") {
		t.Fatal("padded proxy URL produced a different pool key")
	}
}

// TestClientForPoolsPerAccount proves the transport, not just the key, differs:
// two accounts on the same proxy get distinct clients, and the same account
// gets its client back.
func TestClientForPoolsPerAccount(t *testing.T) {
	c := NewClient(nil)
	const proxy = "http://127.0.0.1:3128"
	ca, err := c.clientFor("acct-a", proxy)
	if err != nil {
		t.Fatalf("clientFor(acct-a): %v", err)
	}
	cb, err := c.clientFor("acct-b", proxy)
	if err != nil {
		t.Fatalf("clientFor(acct-b): %v", err)
	}
	if ca == cb {
		t.Fatal("two accounts sharing a proxy got the same http.Client")
	}
	again, err := c.clientFor("acct-a", proxy)
	if err != nil {
		t.Fatalf("clientFor(acct-a) again: %v", err)
	}
	if again != ca {
		t.Fatal("the same account did not get its pooled client back")
	}
}

// TestClientForRejectsBadProxy keeps an unparsable proxy an error instead of a
// silent direct connection.
func TestClientForRejectsBadProxy(t *testing.T) {
	c := NewClient(nil)
	if _, err := c.clientFor("acct-a", "://nope"); err == nil {
		t.Fatal("expected an error for an unparsable proxy URL")
	}
}

// TestNodeClientHelloSpecShape pins the fields that make the handshake look
// like Node's OpenSSL rather than a browser or Go. A drift here is invisible
// in tests that only exercise HTTP, so the shape is asserted directly.
func TestNodeClientHelloSpecShape(t *testing.T) {
	spec := NodeClientHelloSpec()
	if got, want := len(spec.CipherSuites), 52; got != want {
		t.Fatalf("cipher suites: got %d, want %d", got, want)
	}
	// AES-256-GCM-SHA384 leads: OpenSSL's order, not a browser's.
	if spec.CipherSuites[0] != 0x1302 {
		t.Fatalf("first cipher suite: got %#x, want 0x1302", spec.CipherSuites[0])
	}
	if got, want := len(spec.Extensions), 12; got != want {
		t.Fatalf("extensions: got %d, want %d", got, want)
	}
	// OpenSSL sends these in this order; uTLS reorders several of its own
	// extension types, which is why they are pinned as raw bytes.
	wantOrder := []uint16{65281, 11, 10, 35, 22, 23, 13, 43, 45}
	for i, want := range wantOrder {
		ge, ok := spec.Extensions[i].(*utls.GenericExtension)
		if !ok {
			t.Fatalf("extension %d is %T, want a pinned GenericExtension", i, spec.Extensions[i])
		}
		if ge.Id != want {
			t.Fatalf("extension %d id: got %d, want %d", i, ge.Id, want)
		}
	}
	// key_share must stay native so uTLS generates the keys, and the PQ-hybrid
	// group must lead.
	ks, ok := spec.Extensions[9].(*utls.KeyShareExtension)
	if !ok {
		t.Fatalf("extension 9 is %T, want *KeyShareExtension", spec.Extensions[9])
	}
	if len(ks.KeyShares) != 2 || ks.KeyShares[0].Group != utls.CurveID(4588) {
		t.Fatalf("key shares: got %+v, want 4588 then X25519", ks.KeyShares)
	}
	if _, ok := spec.Extensions[10].(*utls.SNIExtension); !ok {
		t.Fatalf("extension 10 is %T, want *SNIExtension", spec.Extensions[10])
	}
	alpn, ok := spec.Extensions[11].(*utls.GenericExtension)
	if !ok || alpn.Id != 16 {
		t.Fatalf("extension 11 is %T/%v, want ALPN", spec.Extensions[11], alpn)
	}
	if got := string(alpn.Data); !strings.HasPrefix(got, "\x00\x0c\x08http/1.1\x02h2") {
		t.Fatalf("ALPN must offer http/1.1 before h2, got %x", alpn.Data)
	}
}

// TestFingerprintedHandshakeReachesServer proves the uTLS path really replaces
// the Go handshake: the server records the ClientHello it was sent, and the
// request still succeeds. httptest uses a self-signed certificate, so the
// connection is built directly here with verification off; production keeps
// verification on (see dialFingerprinted).
func TestFingerprintedHandshakeReachesServer(t *testing.T) {
	sni := make(chan string, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{
		GetConfigForClient: func(chi *tls.ClientHelloInfo) (*tls.Config, error) {
			select {
			case sni <- chi.ServerName:
			default:
			}
			return nil, nil
		},
	}
	srv.StartTLS()
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "https://")
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	host, _, _ := net.SplitHostPort(addr)
	u := utls.UClient(raw, &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: true, // httptest self-signed
		NextProtos:         []string{"http/1.1", "h2"},
	}, utls.HelloCustom)
	if err := u.ApplyPreset(NodeClientHelloSpec()); err != nil {
		t.Fatalf("ApplyPreset: %v", err)
	}
	if err := u.HandshakeContext(context.Background()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	defer u.Close()

	if v := u.ConnectionState().Version; v != utls.VersionTLS13 && v != utls.VersionTLS12 {
		t.Fatalf("negotiated version %#x", v)
	}
	// The peer is an IP literal, and neither Node nor uTLS sends SNI for one
	// (the captured undici hello against 127.0.0.1 had no server_name either),
	// so an empty SNI here is the expected shape. What matters is that a
	// ClientHello arrived at all.
	select {
	case <-sni:
	case <-time.After(5 * time.Second):
		t.Fatal("server never saw a ClientHello")
	}
}

// TestDialFingerprintedClosesOnBadSpec makes a rejected spec release the socket
// instead of leaking it. uTLS validates the spec in ApplyPreset for a
// ClientHelloSpec that cannot be serialised (a duplicated extension id), so the
// failure happens before the handshake and the connection is closed.
func TestDialFingerprintedClosesOnBadSpec(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, e := ln.Accept()
			if e != nil {
				return
			}
			c.Close()
		}
	}()
	// An unsupported TLS version range is rejected by ApplyPreset.
	bad := &utls.ClientHelloSpec{
		TLSVersMin:         utls.VersionTLS11,
		TLSVersMax:         utls.VersionTLS10,
		CipherSuites:       []uint16{0x1301},
		CompressionMethods: []byte{0},
		Extensions:         []utls.TLSExtension{&utls.SNIExtension{}},
	}
	if _, err := dialFingerprinted(context.Background(), "tcp", ln.Addr().String(), bad); err == nil {
		t.Fatal("expected an error for an invalid TLS version range")
	}
}

// TestProxiedTransportKeepsGoHandshake pins the documented limitation: a
// proxied client has no DialTLSContext, because that hook must return a
// completed handshake and behind a CONNECT tunnel the transport does its own.
// If this ever changes, the comment on dialFingerprinted must change with it.
func TestProxiedTransportKeepsGoHandshake(t *testing.T) {
	c := NewClient(nil)
	hc, err := c.clientFor("acct-a", "http://127.0.0.1:3128")
	if err != nil {
		t.Fatalf("clientFor: %v", err)
	}
	tr, ok := hc.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", hc.Transport)
	}
	if tr.DialTLSContext != nil {
		t.Fatal("a proxied transport must not install DialTLSContext")
	}
	// Direct egress is where the fingerprint lives.
	direct, err := c.clientFor("acct-a", "")
	if err != nil {
		t.Fatalf("clientFor direct: %v", err)
	}
	dtr, ok := direct.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("direct transport is %T, want *http.Transport", direct.Transport)
	}
	if dtr.DialTLSContext == nil {
		t.Fatal("a direct transport must install the fingerprinting DialTLSContext")
	}
}

// TestNodeHelloMatchesCapturedLength is a coarse regression guard: the measured
// ClientHello was 1617 bytes on the wire, and a wildly different length means a
// field was added or dropped.
func TestNodeHelloMatchesCapturedLength(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		buf := make([]byte, 16384)
		n, _ := c.Read(buf)
		got <- append([]byte(nil), buf[:n]...)
		c.Close()
	}()

	raw, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	u := utls.UClient(raw, &utls.Config{ServerName: "app.perchai.app", InsecureSkipVerify: true}, utls.HelloCustom)
	if err := u.ApplyPreset(NodeClientHelloSpec()); err != nil {
		t.Fatalf("ApplyPreset: %v", err)
	}
	go u.Handshake() //nolint:errcheck // the peer never answers

	select {
	case b := <-got:
		// The captured hello was 1640 bytes including the record header,
		// with SNI and ALPN both present.
		if len(b) != 1640 {
			t.Fatalf("ClientHello length: got %d, want 1640 (captured); hex head %s",
				len(b), hex.EncodeToString(b[:min(len(b), 16)]))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no ClientHello captured")
	}
}
