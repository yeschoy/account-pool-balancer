package egressid

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOllamaClientPrivacyAndNativeBytes(t *testing.T) {
	body := ` {"model":"m","messages":[{"role":"user","content":"unchanged"}],"metadata":{"user_id":"explicit-body-data"}} `
	received := make(chan *http.Request, 1)
	receivedBody := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		received <- r.Clone(r.Context())
		receivedBody <- string(b)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	base := srv.Client()
	client := OllamaClient(OllamaClient(base)) // wrapping twice must be harmless
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages?beta=true", strings.NewReader(body))
	for name, value := range map[string]string{
		"Authorization": "Bearer upstream-key", "Content-Type": "application/json", "Accept": "text/event-stream",
		"Anthropic-Version": "2023-06-01", "Anthropic-Beta": "tools-test",
		"User-Agent": "Mozilla/5.0 private-device", "Sec-CH-UA-Platform": "private-os",
		"X-Stainless-OS": "private-os", "X-Device-ID": "private-device", "X-Session-ID": "private-session",
		"Cookie": "private-cookie", "Origin": "https://private.example", "Referer": "https://private.example/page",
		"X-Forwarded-For": "192.0.2.1", "Baggage": "private-trace", "Traceparent": "private-trace",
		"X-Custom-Unknown": "private-future-header", "X-Api-Key": "foreign-key", "Accept-Language": "private-language",
	} {
		req.Header.Set(name, value)
	}
	before := req.Header.Clone()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got := <-received
	if <-receivedBody != body || got.URL.RawQuery != "beta=true" {
		t.Fatal("native body/query changed")
	}
	for _, name := range []string{"Authorization", "Content-Type", "Accept", "Anthropic-Version", "Anthropic-Beta"} {
		if got.Header.Get(name) != req.Header.Get(name) {
			t.Errorf("protocol header %s changed", name)
		}
	}
	if got.UserAgent() != OllamaUserAgent {
		t.Errorf("UA = %q", got.UserAgent())
	}
	for name := range before {
		if strings.Contains(before.Get(name), "private") || name == "X-Api-Key" {
			if name != "User-Agent" && got.Header.Get(name) != "" {
				t.Errorf("private header %s leaked", name)
			}
		}
	}
	if !reflect.DeepEqual(before, req.Header) || base.CheckRedirect != nil {
		t.Fatal("mutated caller request/client")
	}
}

func TestOllamaClientRefusesEveryRedirect(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var targetCalls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirected" {
					targetCalls.Add(1)
					return
				}
				http.Redirect(w, r, "/redirected", status)
			}))
			defer srv.Close()
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer private-key")
			resp, err := OllamaClient(srv.Client()).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != status || targetCalls.Load() != 0 {
				t.Fatal("credential request followed redirect")
			}
		})
	}
}
