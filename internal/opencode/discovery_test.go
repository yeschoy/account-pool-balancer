package opencode

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const capabilitiesFixture = `{"opencode":{"npm":"@ai-sdk/openai-compatible","models":{"chat":{"limit":{"context":123,"output":45},"tool_call":true},"same":{"provider":{"npm":"@ai-sdk/openai"}}}},"opencode-go":{"npm":"@ai-sdk/anthropic","models":{"same":{"limit":{"context":999}},"unsupported":{"provider":{"npm":"@ai-sdk/google"}}}},"foreign":{"npm":["unrelated-schema"],"models":{"foreign":{"reasoning":{"different":true}}}}}`

func TestDiscoveryTierScopedAndCoalesced(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential sent to public metadata")
		}
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		fmt.Fprint(w, capabilitiesFixture)
	}))
	defer up.Close()
	var d Discovery
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Go(func() {
			all, err := d.Get(context.Background(), up.Client(), "proxy", up.URL, "", "", time.Minute)
			if err != nil {
				t.Error(err)
				return
			}
			if all["zen"]["same"].Protocol != Responses || all["go"]["same"].Protocol != Messages || all["zen"]["chat"].Context != 123 || all["go"]["unsupported"].Protocol != "" {
				t.Error("tier capability contamination")
			}
		})
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("reads not coalesced: %d", calls.Load())
	}
}
func TestProtocolDocsOverrideAndFailure(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/docs" {
			fmt.Fprint(w, "| Same | `same` | `https://opencode.ai/zen/v1/messages` |\n")
			return
		}
		fmt.Fprint(w, capabilitiesFixture)
	}))
	defer up.Close()
	all, err := FetchCapabilities(context.Background(), up.Client(), up.URL, up.URL+"/docs", "")
	if err != nil {
		t.Fatal(err)
	}
	if all["zen"]["same"].Protocol != Messages || all["go"]["same"].Context != 999 {
		t.Fatal("docs not tier scoped")
	}
	if _, err := FetchCapabilities(context.Background(), up.Client(), up.URL, up.URL+"/bad-docs", ""); err == nil {
		t.Fatal("explicit broken docs silently accepted")
	}
}
func TestGoUsageAndUnknown(t *testing.T) {
	good := `{"usage":{"rolling":{"status":"ok","percent":10,"resetsAt":"2099-01-01T00:00:00Z"},"weekly":{"status":"rate-limited","percent":100,"resetsAt":"2099-02-01T00:00:00Z"},"monthly":{"status":"ok","percent":40,"resetsAt":"2099-03-01T00:00:00Z"}}}`
	for _, body := range []string{good, `{"usage":{}}`, strings.Replace(good, `"percent":10`, `"percent":null`, 1), strings.Replace(good, `"percent":10`, `"percent":101`, 1)} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/go/v1/usage" || r.Header.Get("Authorization") != "Bearer opaque-key" {
				t.Errorf("usage contract %s %+v", r.URL, r.Header)
			}
			fmt.Fprint(w, body)
		}))
		u, err := FetchUsage(context.Background(), up.Client(), up.URL+"/go/v1", "opaque-key")
		up.Close()
		if body == good {
			if err != nil || u["rolling"].RemainingPercent != 90 {
				t.Fatalf("valid usage %+v %v", u, err)
			}
			s := &State{Usage: u, UsageAt: time.Now()}
			if !s.Exhausted(time.Now(), time.Minute) {
				t.Fatal("known limited not exhausted")
			}
			s.UsageError = "unavailable"
			if s.Exhausted(time.Now(), time.Minute) {
				t.Fatal("stale read treated as exhausted")
			}
		} else if err == nil {
			t.Fatal("incomplete quota accepted")
		}
	}
	if (*State)(nil).Exhausted(time.Now(), time.Minute) {
		t.Fatal("unknown is zero")
	}
}
func TestNoRedirectCredentialLeak(t *testing.T) {
	var leaked atomic.Int32
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer sink.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	if _, err := FetchUsage(context.Background(), origin.Client(), origin.URL, "secret"); err == nil {
		t.Fatal("redirect accepted")
	}
	if leaked.Load() != 0 {
		t.Fatal("followed credential redirect")
	}
}
