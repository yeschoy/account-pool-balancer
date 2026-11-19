package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"account-pool-balancer/internal/config"
)

func gzipBody(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func brotliBody(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := brotli.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zstdBody(t *testing.T, s string) []byte {
	t.Helper()
	var b bytes.Buffer
	w, err := zstd.NewWriter(&b)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

// droid asks for gzip, deflate, br and zstd, so a compressed Factory answer
// must still reach the parser as plain text.
func TestFactoryDecodesCompressedUpstream(t *testing.T) {
	const payload = `{"choices":[{"message":{"content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1}}`
	for _, tc := range []struct {
		name, encoding string
		body           func(*testing.T, string) []byte
	}{
		{"gzip", "gzip", gzipBody},
		{"brotli", "br", brotliBody},
		{"zstd", "zstd", zstdBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newFactoryEnv(t)
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != factoryAcceptEncoding {
					t.Errorf("Accept-Encoding = %q", r.Header.Get("Accept-Encoding"))
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Content-Encoding", tc.encoding)
				_, _ = w.Write(tc.body(t, payload))
			}
			e.mu.Unlock()
			rsp, out := e.do(t, "/v1/chat/completions", `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`, nil)
			if rsp.StatusCode != 200 || !strings.Contains(out, "OK") {
				t.Fatalf("%s: status=%d body=%q", tc.name, rsp.StatusCode, out)
			}
			if rsp.Header.Get("Content-Encoding") != "" {
				t.Fatal("compressed encoding leaked downstream")
			}
		})
	}
}

func TestFactoryRejectsUnknownEncoding(t *testing.T) {
	body, err := decodeFactoryBody(io.NopCloser(strings.NewReader("x")), "compress")
	if err == nil {
		body.Close()
		t.Fatal("unknown encoding accepted")
	}
}

// A browser User-Agent is refused at configuration time: the gateway sends no
// Sec-CH-UA, Accept-Language or Sec-Fetch-* with it, and droid never claimed to
// be a browser.
func TestFactoryRejectsBrowserUserAgent(t *testing.T) {
	const chrome = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/157.0.0.0 Safari/537.36"
	c := config.Default()
	c.Factory.UserAgents = []string{chrome}
	if err := c.Normalize(); err == nil {
		t.Fatal("browser UA accepted in pool")
	}
	c = config.Default()
	c.Factory.UserAgent = chrome
	if err := c.Normalize(); err == nil {
		t.Fatal("browser UA accepted as default")
	}
	c = config.Default()
	c.Accounts = []config.Account{{ID: "f", Provider: config.ProviderFactory, Group: "factory", RuntimeKey: "fk-valid_key_123", UserAgent: chrome}}
	if err := c.Normalize(); err == nil {
		t.Fatal("browser UA accepted on account")
	}
	c = config.Default()
	c.Factory.UserAgents = []string{"factory-cli/1.15.0", "factory-cli/1.14.0"}
	if err := c.Normalize(); err != nil {
		t.Fatalf("real CLI UA refused: %v", err)
	}
}

// The wire never carries a browser identity, with or without stored overrides.
func TestFactorySendsCLIIdentityOnly(t *testing.T) {
	e := newFactoryEnv(t)
	e.do(t, "/v1/chat/completions", `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`, map[string]string{
		"User-Agent": "Mozilla/5.0 (X11; Linux x86_64) Chrome/157.0.0.0 Safari/537.36",
		"Cookie":     "sid=1", "Origin": "https://x.example", "Referer": "https://x.example/",
		"Sec-Fetch-Mode": "cors", "Accept-Language": "zh-CN", "Sec-Ch-Ua": `"Chromium"`,
	})
	h := e.snapshot()[0].header
	if ua := h.Get("User-Agent"); ua != "factory-cli/"+config.Default().Factory.EffectiveDroidVersion() {
		t.Fatalf("browser UA reached upstream: %q", ua)
	}
	for _, name := range []string{"Accept-Language", "Sec-Ch-Ua", "Sec-Fetch-Mode", "Origin", "Referer"} {
		if h.Get(name) != "" {
			t.Fatalf("%s leaked with the CLI identity", name)
		}
	}
	if h.Get("Accept-Encoding") != factoryAcceptEncoding {
		t.Fatalf("Accept-Encoding = %q", h.Get("Accept-Encoding"))
	}
}
