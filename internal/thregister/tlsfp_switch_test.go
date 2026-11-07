package thregister

import (
	"testing"
)

const socksProxy = "socks5h://u:p@127.0.0.1:1080"

// TestTLSFingerprintSwitchOn proves the Chrome handshake is installed when the
// switch is on, and that h2 stays off so the wire format matches.
func TestTLSFingerprintSwitchOn(t *testing.T) {
	tr, err := TransportWithFingerprint(socksProxy, true)
	if err != nil {
		t.Fatalf("TransportWithFingerprint: %v", err)
	}
	if tr.DialTLSContext == nil {
		t.Fatal("fingerprint on: DialTLSContext must be installed for socks5")
	}
	if tr.ForceAttemptHTTP2 {
		t.Fatal("fingerprint on: ForceAttemptHTTP2 must be off")
	}
}

// TestTLSFingerprintSwitchOff proves the switch restores Go's own handshake.
func TestTLSFingerprintSwitchOff(t *testing.T) {
	tr, err := TransportWithFingerprint(socksProxy, false)
	if err != nil {
		t.Fatalf("TransportWithFingerprint: %v", err)
	}
	if tr.DialTLSContext != nil {
		t.Fatal("fingerprint off: DialTLSContext must not be installed")
	}
	if !tr.ForceAttemptHTTP2 {
		t.Fatal("fingerprint off: ForceAttemptHTTP2 stays as before")
	}
}

// TestTLSFingerprintIgnoredForHTTPProxy documents the scope: an HTTP proxy does
// its own TLS after CONNECT, so the switch cannot apply there.
func TestTLSFingerprintIgnoredForHTTPProxy(t *testing.T) {
	tr, err := TransportWithFingerprint("http://127.0.0.1:8080", true)
	if err != nil {
		t.Fatalf("TransportWithFingerprint: %v", err)
	}
	if tr.DialTLSContext != nil {
		t.Fatal("http proxy: DialTLSContext cannot take effect and must stay unset")
	}
}

// TestIsSocksProxy covers the scheme classifier the scope depends on.
func TestIsSocksProxy(t *testing.T) {
	cases := map[string]bool{
		"socks5://u:p@h:1":   true,
		"socks5h://u:p@h:1":  true,
		"http://127.0.0.1:8": false,
		"https://h:1":        false,
		"":                   false,
		"garbage":            false,
	}
	for in, want := range cases {
		if got := isSocksProxy(in); got != want {
			t.Errorf("isSocksProxy(%q)=%v want %v", in, got, want)
		}
	}
}
