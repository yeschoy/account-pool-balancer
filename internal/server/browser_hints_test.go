package server

import (
	"fmt"
	"net/http"
	"testing"
)

// A browser User-Agent must not reach an upstream without the hints a browser
// always sends beside it.
func TestCompleteBrowserHints(t *testing.T) {
	cases := []struct {
		ua        string
		wantHints bool
		wantUA    string
	}{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36", true, ""},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:132.0) Gecko/20100101 Firefox/132.0", false, ""},
		{"codex_cli_rs/0.51.0", false, "codex_cli_rs/0.51.0"},
		{"claude-cli/2.0.1 (external, cli)", false, "claude-cli/2.0.1 (external, cli)"},
		{"opencode/0.9.1", false, "opencode/0.9.1"},
		{"Go-http-client/1.1", false, "Go-http-client/1.1"},
	}
	for _, c := range cases {
		h := http.Header{}
		h.Set("User-Agent", c.ua)
		before := h.Get("User-Agent")
		completeBrowserHints(h)
		fmt.Printf("UA=%.40s | ch-ua=%q lang=%q fetch=%q\n", c.ua, h.Get("Sec-Ch-Ua"), h.Get("Accept-Language"), h.Get("Sec-Fetch-Mode"))
		if h.Get("User-Agent") != before {
			t.Fatalf("User-Agent rewritten for %q", c.ua)
		}
		if c.wantHints && h.Get("Sec-Ch-Ua") == "" {
			t.Fatalf("no hints added for browser UA %q", c.ua)
		}
		if !c.wantHints && h.Get("Sec-Ch-Ua") != "" {
			t.Fatalf("hints added for non-browser UA %q", c.ua)
		}
	}
}
