package perch

import (
	utls "github.com/refraction-networking/utls"
)

// The published client is not a browser: it is a Node.js bundle that reaches
// Perch through undici's fetch, so its ClientHello comes from Node's OpenSSL
// rather than from Chrome. Impersonating a browser here would be its own
// signal — a user agent that says "perchai-cli" arriving with a Chrome
// handshake is a contradiction no inference is needed to spot.
//
// NodeClientHelloSpec reproduces the handshake measured from
// perchai-cli 2.4.120 on Node v26 (OpenSSL 3.x): 52 cipher suites, 11
// extensions in OpenSSL's order, and the PQ-hybrid group 4588
// (X25519MLKEM768) first. Every field below was compared against a captured
// ClientHello; see .analysis/tls-fingerprint.md.
//
// Two rules keep it faithful, and both matter:
//
//   - Extensions are pinned in order. uTLS reorders several of its own
//     extension types (ExtendedMasterSecret moves forward, SignatureAlgorithms
//     jumps the queue, CompressCertificate is inserted where the client never
//     sent one). Only key_share must stay a native type, because uTLS has to
//     generate the keys; everything else is written as raw bytes so the order
//     survives.
//   - session_id_len is 32. Node sends a non-empty session id for TLS 1.3
//     middlebox compatibility; an empty one is the single most common way a
//     non-Node client gives itself away.
//
// The handshake is static: it names no model, no account and no destination.
// Only the SNI and the key shares differ between calls, and uTLS fills those
// in.
func NodeClientHelloSpec() *utls.ClientHelloSpec {
	return &utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS13,
		CipherSuites: []uint16{
			// TLS 1.3 first (AES-256-GCM-SHA384 leads, which is OpenSSL's
			// preference and not a browser's), then the 1.2 ECDHE list.
			0x1302, 0x1303, 0x1301,
			0xc02f, 0xc02b, 0xc030, 0xc02c, 0x009e, 0xc027, 0x0067,
			0xc028, 0x006b, 0x00a3, 0x009f, 0xcca9, 0xcca8, 0xccaa,
			0xc0ad, 0xc09f, 0xc05d, 0xc061, 0xc057, 0xc053, 0x00a2,
			0xc0ac, 0xc09e, 0xc05c, 0xc060, 0xc056, 0xc052, 0xc024,
			0x006a, 0xc023, 0x0040, 0xc00a, 0xc014, 0x0039, 0x0038,
			0xc009, 0xc013, 0x0033, 0x0032, 0x009d, 0xc09d, 0xc051,
			0x009c, 0xc09c, 0xc050, 0x003d, 0x003c, 0x0035, 0x002f,
		},
		CompressionMethods: []byte{0},
		Extensions: []utls.TLSExtension{
			&utls.GenericExtension{Id: 65281, Data: []byte{0}},          // renegotiation_info
			&utls.GenericExtension{Id: 11, Data: []byte{1, 0}},          // ec_point_formats
			&utls.GenericExtension{Id: 10, Data: nodeGroups()},          // supported_groups
			&utls.GenericExtension{Id: 35, Data: []byte{}},              // session_ticket
			&utls.GenericExtension{Id: 22, Data: []byte{}},              // encrypt_then_mac
			&utls.GenericExtension{Id: 23, Data: []byte{}},              // extended_master_secret
			&utls.GenericExtension{Id: 13, Data: nodeSigAlgs()},         // signature_algorithms
			&utls.GenericExtension{Id: 43, Data: []byte{4, 3, 4, 3, 3}}, // supported_versions
			&utls.GenericExtension{Id: 45, Data: []byte{1, 1}},          // psk_key_exchange_modes
			&utls.KeyShareExtension{KeyShares: []utls.KeyShare{
				{Group: utls.CurveID(4588)}, // X25519MLKEM768
				{Group: utls.X25519},
			}},
			// server_name before ALPN: Node appends the SNI it was asked for,
			// then the protocol list.
			&utls.SNIExtension{},
			&utls.GenericExtension{Id: 16, Data: nodeALPN()}, // application_layer_protocol_negotiation
		},
	}
}

// nodeGroups is supported_groups as OpenSSL orders it: the hybrid post-quantum
// key exchange first, then the classical curves.
func nodeGroups() []byte {
	g := []uint16{4588, 4587, 4590, 29, 23, 30, 24, 25, 41, 256, 257}
	out := []byte{byte(len(g) * 2 >> 8), byte(len(g) * 2)}
	for _, v := range g {
		out = append(out, byte(v>>8), byte(v))
	}
	return out
}

// nodeSigAlgs is signature_algorithms in OpenSSL's order. The sha256/ECDSA
// block leads, which is another place a browser differs.
func nodeSigAlgs() []byte {
	s := []uint16{
		0x905, 0x906, 0x904, 0x403, 0x503, 0x603, 0x807, 0x808,
		0x81a, 0x81b, 0x81c, 0x809, 0x80a, 0x80b, 0x804, 0x805,
		0x806, 0x401, 0x501, 0x601, 0x303, 0x301, 0x302, 0x402,
		0x502, 0x602, 0x911, 0x912, 0x913, 0x914, 0x915, 0x916,
		0x917, 0x918, 0x919, 0x91a, 0x91b, 0x91c, 0x708,
	}
	out := []byte{byte(len(s) * 2 >> 8), byte(len(s) * 2)}
	for _, v := range s {
		out = append(out, byte(v>>8), byte(v))
	}
	return out
}

// nodeALPN offers http/1.1 first and h2 second, which is what undici sends.
// The order is part of the fingerprint: a client that offers h2 first is
// asking for HTTP/2 on the first round trip and is not undici.
func nodeALPN() []byte {
	return []byte{
		0x00, 0x0c, // extension body length (12)
		0x08, 'h', 't', 't', 'p', '/', '1', '.', '1',
		0x02, 'h', '2',
	}
}

// http11ClientHelloSpec returns the measured Node handshake with ALPN narrowed
// to http/1.1 alone.
//
// Why this exists: the measured hello advertises "http/1.1, h2", so a server
// may select h2. That is incompatible with *http.Transport, which always writes
// an HTTP/1.1 request line — the server rejects it ("bogus greeting") and Go
// surfaces the h2 SETTINGS frame as "malformed HTTP response". Declaring only
// http/1.1 in the ClientHello is the one change that keeps the negotiated
// protocol equal to the one Go speaks, and it is the narrowest possible
// deviation from the measured handshake: every other byte, including extension
// order, cipher suite order and the PQ-hybrid key share, is untouched.
//
// The alternative — keeping both protocols and carrying the connection on
// http2.Transport — preserves this byte, but restructures the whole call path
// (a separate transport, its own connection pool, and a different way to report
// errors). The single ALPN entry is the smaller, more auditable change, and
// undici clients do also offer http/1.1 first, so a receiver still sees a
// plausible HTTP/1.1 client.
//
// Every call returns a fresh spec: uTLS mutates the spec it is given, so a
// shared pointer poisons the next connection ("tls: internal error").
func http11ClientHelloSpec() *utls.ClientHelloSpec {
	spec := NodeClientHelloSpec()
	out := *spec
	out.Extensions = make([]utls.TLSExtension, 0, len(spec.Extensions))
	for _, ext := range spec.Extensions {
		if g, ok := ext.(*utls.GenericExtension); ok && g.Id == 16 {
			out.Extensions = append(out.Extensions, &utls.ALPNExtension{
				AlpnProtocols: []string{"http/1.1"},
			})
			continue
		}
		out.Extensions = append(out.Extensions, ext)
	}
	return &out
}
