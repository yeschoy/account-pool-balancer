package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/config"
)

func TestNativeDetailBodyLimitProviderIsolation(t *testing.T) {
	cfg := config.Default()
	for _, tc := range []struct {
		provider string
		globalKB int
		want     int
	}{
		{config.ProviderDeploy, 6144, 5 << 20},
		{config.ProviderDeploy, 128, 128 << 10},
		{config.ProviderTokenHarbor, 6144, 64 << 10},
		{config.ProviderMistral, 6144, 64 << 10},
		{config.ProviderTokenHarbor, 32, 32 << 10},
		{config.ProviderMistral, 32, 32 << 10},
	} {
		t.Run(fmt.Sprintf("%s/%dKiB", tc.provider, tc.globalKB), func(t *testing.T) {
			cfg.Logs.DetailBodyKB = tc.globalKB
			if got := nativeDetailBodyLimit(cfg, tc.provider); got != tc.want {
				t.Fatalf("got %d-byte limit, want %d", got, tc.want)
			}
		})
	}
}

func TestDeployDetailBodyLimit(t *testing.T) {
	const capBytes = 5 << 20
	for _, tc := range []struct {
		name         string
		status       int
		bodyBytes    int
		responseSize int
		sharedDetail bool
		enabled      bool
		globalKB     int
		requestCut   bool
		responseCut  bool
	}{
		{name: "failed-1.37MiB", status: 400, bodyBytes: 1372993, enabled: true, globalKB: 6144},
		{name: "success-1.37MiB", status: 200, bodyBytes: 1372993, enabled: true, globalKB: 6144},
		{name: "failed-exact-5MiB", status: 400, bodyBytes: capBytes, enabled: true, globalKB: 6144},
		{name: "failed-over-5MiB", status: 400, bodyBytes: capBytes + (128 << 10), enabled: true, globalKB: 6144, requestCut: true},
		{name: "success-over-5MiB", status: 200, bodyBytes: capBytes + (128 << 10), sharedDetail: true, enabled: true, globalKB: 6144, requestCut: true},
		{name: "shared-enabled-exact-5MiB", status: 200, bodyBytes: capBytes, sharedDetail: true, enabled: true, globalKB: 6144},
		{name: "response-exact-5MiB", status: 200, bodyBytes: 1024, responseSize: capBytes, enabled: true, globalKB: 6144},
		{name: "response-over-5MiB", status: 200, bodyBytes: 1024, responseSize: capBytes + (128 << 10), enabled: true, globalKB: 6144, responseCut: true},
		{name: "smaller-global-limit", status: 400, bodyBytes: 200 << 10, enabled: true, globalKB: 128, requestCut: true},
		{name: "disabled-overrides-shared", status: 400, bodyBytes: 1372993, sharedDetail: true, globalKB: 6144},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDeployEnv(t)
			_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				c.Deploy.DetailLog = tc.enabled
				c.Logs.Detail = tc.sharedDetail
				c.Logs.DetailBodyKB = tc.globalKB
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			response := `{"status_code":400,"message":"Received malformed JSON body"}`
			if tc.status == 200 {
				const prefix = `{"choices":[{"message":{"content":"`
				const suffix = `"},"finish_reason":"stop"}]}`
				size := tc.responseSize
				if size == 0 {
					size = len(prefix) + len(suffix) + 2
				}
				response = prefix + strings.Repeat("r", size-len(prefix)-len(suffix)) + suffix
			}
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, response)
			}
			e.mu.Unlock()
			const prefix = `{"model":"native","messages":[{"role":"user","content":"`
			const suffix = `"}]}`
			body := prefix + strings.Repeat("p", tc.bodyBytes-len(prefix)-len(suffix)) + suffix
			rsp, out := e.do(t, "/v1/chat/completions", body, nil)
			if rsp.StatusCode != tc.status || out != response {
				t.Fatal("detail limit changed the downstream response")
			}
			calls := e.snapshot()
			if len(calls) != 1 || calls[0].body != body {
				t.Fatal("detail limit changed or replayed the native request")
			}
			page, err := e.s.logs.Query("details", map[string]string{"requestId": rsp.Header.Get("X-Request-Id")}, time.Time{}, time.Time{}, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.enabled {
				if len(page.Items) != 0 {
					t.Fatal("disabled Deploy details inherited the shared switch")
				}
				return
			}
			if len(page.Items) != 1 {
				t.Fatalf("want exactly one detail, got %d", len(page.Items))
			}
			var d detailLog
			if err := json.Unmarshal(page.Items[0], &d); err != nil {
				t.Fatal(err)
			}
			if d.RequestBodyBytes != len(body) || d.RequestBodyTruncated != tc.requestCut || d.ResponseBodyTruncated != tc.responseCut {
				t.Fatalf("wrong lengths/truncation: request bytes=%d cut=%t response cut=%t", d.RequestBodyBytes, d.RequestBodyTruncated, d.ResponseBodyTruncated)
			}
			limit := min(tc.globalKB<<10, capBytes)
			if len(d.RequestBody) > limit+256 || len(d.ResponseBody) > limit+256 {
				t.Fatal("detail exceeded the body cap including bounded truncation marker")
			}
			if !tc.requestCut && (d.RequestBody != body || !json.Valid([]byte(d.RequestBody))) {
				t.Fatal("request within limit was not fully retained")
			}
			if !tc.responseCut {
				var want, got any
				if json.Unmarshal([]byte(response), &want) != nil || json.Unmarshal([]byte(d.ResponseBody), &got) != nil || !reflect.DeepEqual(want, got) {
					t.Fatal("response within limit was not fully retained")
				}
			}
		})
	}
}

func TestDeployDetailRedactionExpansionMarksTruncation(t *testing.T) {
	e := newDeployEnv(t)
	_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
		c.Deploy.DetailLog = true
		c.Deploy.Routing.SessionHeaders = []string{"X-Deploy-Session"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		fmt.Fprint(w, `{"message":"Received malformed JSON body"}`)
	}
	e.mu.Unlock()
	// Replacing eight-byte identities with [REDACTED] can exceed the capture
	// budget even when the original JSON was within it.
	body := `{"model":"native","messages":[{"role":"user","content":"` + strings.Repeat("short-id", (5<<20)/8-32) + `"}]}`
	rsp, _ := e.do(t, "/v1/chat/completions", body, map[string]string{"X-Deploy-Session": "short-id"})
	page, _ := e.s.logs.Query("details", map[string]string{"requestId": rsp.Header.Get("X-Request-Id")}, time.Time{}, time.Time{}, 10, 0)
	if len(page.Items) != 1 {
		t.Fatal("missing redacted detail")
	}
	var d detailLog
	if json.Unmarshal(page.Items[0], &d) != nil || !d.RequestBodyTruncated || len(d.RequestBody) > (5<<20)+256 || strings.Contains(d.RequestBody, "short-id") {
		t.Fatal("redaction growth hid truncation, exceeded the cap or leaked an identity")
	}
}

func TestDeployLargeDetailRedactsIdentities(t *testing.T) {
	for _, status := range []int{200, 400} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			e := newDeployEnv(t)
			_, err := e.s.cfg.Update(-1, func(c *config.Config) error {
				c.Deploy.DetailLog = true
				c.Deploy.Routing.SessionHeaders = []string{"X-Deploy-Session"}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			e.mu.Lock()
			e.handler = func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"choices":[{"message":{"content":"OK"}}],"echo":"large-private-session","api_key":"mock_deploy_secret_1"}`)
			}
			e.mu.Unlock()
			body := `{"model":"native","messages":[{"role":"user","content":"` + strings.Repeat("p", 1400<<10) + `"}],"metadata":{"session_id":"large-private-session"},"api_key":"mock_deploy_secret_1","prompt_cache_key":"large-private-cache"}`
			rsp, _ := e.do(t, "/v1/chat/completions", body, map[string]string{"X-Deploy-Session": "large-private-session", "Cookie": "diagnostic=large-private-cookie"})
			page, _ := e.s.logs.Query("details", map[string]string{"requestId": rsp.Header.Get("X-Request-Id")}, time.Time{}, time.Time{}, 10, 0)
			if len(page.Items) != 1 {
				t.Fatal("missing large detail")
			}
			for _, secret := range []string{"mock_deploy_secret_1", "downstream-ms", "large-private-session", "large-private-cache", "large-private-cookie"} {
				if strings.Contains(string(page.Items[0]), secret) {
					t.Fatal("secret leaked in enlarged detail")
				}
			}
			var d detailLog
			if json.Unmarshal(page.Items[0], &d) != nil || d.RequestBodyTruncated || len(d.RequestBody) <= 64<<10 || !json.Valid([]byte(d.RequestBody)) {
				t.Fatal("redacted request was still truncated at the old cap")
			}
		})
	}
}
