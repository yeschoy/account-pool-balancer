package server

import (
	"net/http"
	"testing"

	"account-pool-balancer/internal/thsignupstore"
)

// Registering with no proxy at all runs from this host's own address. That
// must not be silent: the row is still queued (a deployment may intend it), but
// it carries a proxy-less egress the start of the run reports loudly.
func TestTHAssignProxiesLeavesEmptyEgressObservable(t *testing.T) {
	rows := []thsignupstore.Row{{}, {}}
	if err := thAssignProxies(rows, nil, "", "sequential"); err != nil {
		t.Fatalf("assign: %v", err)
	}
	for i, r := range rows {
		if r.ProxyURL != "" {
			t.Fatalf("row %d invented a proxy %q", i, r.ProxyURL)
		}
		if r.Egress("") != "" {
			t.Fatalf("row %d resolves to a non-empty egress without configuration", i)
		}
	}
	if NoEgressWarning == "" {
		t.Fatal("there must be a warning to report for a proxy-less run")
	}
}

// An explicit per-row proxy is still an egress even when no default exists.
func TestTHAssignProxiesAcceptsExplicitRowProxy(t *testing.T) {
	rows := []thsignupstore.Row{{ProxyURL: "http://only:8080"}}
	if err := thAssignProxies(rows, nil, "", "sequential"); err != nil {
		t.Fatalf("explicit proxy refused: %v", err)
	}
	if got := rows[0].Egress(""); got != "http://only:8080" {
		t.Fatalf("egress = %q, want the explicit proxy", got)
	}
}

// The upstream must see our egress, never the downstream client's address.
// These spellings are what common proxies and CDNs emit.
func TestCopyRequestHeadersDropsEveryClientAddressSpelling(t *testing.T) {
	for _, name := range []string{
		"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded",
		"X-Real-IP", "True-Client-IP", "CF-Connecting-IP", "X-Client-IP",
		"Client-IP", "X-Originating-IP", "X-Cluster-Client-IP", "X-Remote-IP",
		"X-Remote-Addr", "Via", "X-ProxyUser-IP", "Fastly-Client-IP",
		"X-Akamai-Client-IP", "X-Bluecoat-Via", "X-Hermes-Client-IP",
	} {
		t.Run(name, func(t *testing.T) {
			src := http.Header{}
			src.Set(name, "203.0.113.9")
			dst := http.Header{}
			copyRequestHeaders(dst, src)
			if got := dst.Get(name); got != "" {
				t.Fatalf("%s reached the upstream as %q", name, got)
			}
		})
	}
}

// The filter must not become so broad that it drops what the upstream needs.
func TestCopyRequestHeadersKeepsClientPayload(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("Anthropic-Version", "2023-06-01")
	src.Set("Accept", "application/json")
	dst := http.Header{}
	copyRequestHeaders(dst, src)
	for _, k := range []string{"Content-Type", "Anthropic-Version", "Accept"} {
		if dst.Get(k) == "" {
			t.Fatalf("%s was dropped from a legitimate client request", k)
		}
	}
}
