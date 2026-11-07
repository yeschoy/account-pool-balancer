package thregister

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"strings"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/proxy"
)

// chromeClientHelloSpec is the ClientHello a macOS Chrome presents.
//
// Why registration needs this at all: Transport below hands https traffic to
// Go's own crypto/tls, whose ClientHello (cipher order, extension set, no GREASE
// values) is a well-known signature. Cloudflare publishes JA3/JA4 rules for the
// Go HTTP client, so every registration arrived flagged as "not a browser"
// before the site had read a single header.
//
// The spec reproduces Chrome's handshake shape: GREASE first, AES-128-GCM
// leading the TLS 1.3 suites (browser preference, unlike OpenSSL), the browser
// extension order, and the ALPN list Chrome offers. Only http/1.1 is advertised
// because *http.Transport always writes an HTTP/1.1 request line: offering h2
// would let the server pick a protocol Go cannot speak on that connection
// ("malformed HTTP response" surfaced as a TLS error).
//
// A fresh spec is returned on every call because uTLS mutates the spec it is
// handed; sharing one pointer makes the second handshake fail with
// "tls: internal error".
func chromeClientHelloSpec() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS13,
		CipherSuites: []uint16{
			// GREASE, then TLS 1.3 suites in Chrome's order.
			0x0a0a,
			utls.TLS_AES_128_GCM_SHA256,
			utls.TLS_AES_256_GCM_SHA384,
			utls.TLS_CHACHA20_POLY1305_SHA256,
			// TLS 1.2 ECDHE suites, Chrome's preference.
			0xc02b, 0xc02f, 0xc02c, 0xc030, 0xcca9, 0xcca8, 0xc013, 0xc014,
			0x009c, 0x009d, 0x002f, 0x0035, 0x000a,
		},
		CompressionMethods: []byte{0},
		Extensions: []utls.TLSExtension{
			&utls.UtlsGREASEExtension{},
			&utls.SNIExtension{},
			&utls.UtlsExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.GREASE_PLACEHOLDER,
				utls.X25519,
				utls.CurveP256,
				utls.CurveP384,
			}},
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&utls.SessionTicketExtension{},
			&utls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&utls.StatusRequestExtension{},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.PSSWithSHA256,
				utls.PKCS1WithSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.PSSWithSHA384,
				utls.PKCS1WithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA512,
			}},
			&utls.SCTExtension{},
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
				{Group: utls.CurveID(utls.GREASE_PLACEHOLDER), Data: []byte{0}},
				{Group: utls.X25519},
				{Group: utls.CurveP256},
			}},
			&utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}},
			&utls.SupportedVersionsExtension{Versions: []uint16{
				utls.GREASE_PLACEHOLDER,
				utls.VersionTLS13,
				utls.VersionTLS12,
			}},
			&utls.UtlsCompressCertExtension{},
			&utls.ApplicationSettingsExtension{SupportedProtocols: []string{"h2"}},
			&utls.UtlsGREASEExtension{},
			&utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle},
		},
	}
}

// isSocksProxy reports whether the proxy scheme is one Go dials itself.
//
// This matters because http.Transport handles the two families differently.
// With an HTTP proxy it performs TLS on its own after the CONNECT tunnel is
// open, and never calls DialTLSContext. With a SOCKS5 proxy it installs the
// socks dialer as DialContext and *does* call DialTLSContext for https URLs,
// which is what lets the fingerprinted handshake run behind the proxy.
func isSocksProxy(proxyURL string) bool {
	s := strings.TrimSpace(proxyURL)
	if s == "" {
		return false
	}
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return u.Scheme == "socks5" || u.Scheme == "socks5h"
}

// socksDialTLS returns a DialTLSContext that tunnels through the socks proxy
// and then presents the Chrome ClientHello.
//
// socks5 resolves names locally; socks5h asks the proxy to resolve. Both end up
// as the same tunnel here, because the address handed to us is already the
// host:port the dialer will pass to the proxy.
func socksDialTLS(proxyURL, serverName string) (func(context.Context, string, string) (net.Conn, error), error) {
	u, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || u.Host == "" {
		return nil, errors.New("代理地址无效")
	}
	auth := (*proxy.Auth)(nil)
	if u.User != nil {
		pw, _ := u.User.Password()
		auth = &proxy.Auth{User: u.User.Username(), Password: pw}
	}
	fwd, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
	if err != nil {
		return nil, err
	}
	ctxDial, ok := fwd.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS5 拨号器不支持 context")
	}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, err := ctxDial.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		host, _, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			host = addr
		}
		if serverName != "" && net.ParseIP(host) == nil {
			host = serverName
		}
		cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		fp := &utls.Config{
			ServerName: host,
			// Must match the ALPN the spec advertises; uTLS rejects a
			// negotiated protocol that is not listed here.
			NextProtos: []string{"http/1.1"},
			MinVersion: utls.VersionTLS12,
			// Verification stays on: the certificate must be the real one.
			RootCAs: cfg.RootCAs,
		}
		uc := utls.UClient(raw, fp, utls.HelloCustom)
		if err := uc.ApplyPreset(chromeClientHelloSpec()); err != nil {
			raw.Close()
			return nil, err
		}
		if err := uc.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, err
		}
		return uc, nil
	}, nil
}
