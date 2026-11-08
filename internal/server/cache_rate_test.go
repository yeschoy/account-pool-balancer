package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"account-pool-balancer/internal/stats"
)

func TestResetCacheRateAdminScopeAndPersistence(t *testing.T) {
	e := newMistralEnv(t)
	input, cached := int64(100), int64(60)
	for _, provider := range []string{"mistral", "river"} {
		e.s.stats.Record(stats.Sample{Provider: provider, AccountID: provider, Input: &input, Cached: &cached, Prompt: 100})
	}
	before := e.s.stats.SummaryFor("mistral").Total
	other := e.s.stats.SummaryFor("river").Total
	configBefore, _ := os.ReadFile(filepath.Join(e.s.dataDir, "config.json"))
	login := httptest.NewRecorder()
	session := e.s.admin.create(login, httptest.NewRequest("POST", "/api/login", nil))
	for _, tc := range []struct {
		provider    string
		admin, csrf bool
		status      int
	}{
		{"mistral", false, false, http.StatusUnauthorized},
		{"mistral", true, false, http.StatusForbidden},
		{"", true, true, http.StatusBadRequest},
		{"unknown", true, true, http.StatusBadRequest},
		{"perch", true, true, http.StatusBadRequest},
		{"mistral", true, true, http.StatusOK},
	} {
		r := httptest.NewRequest("POST", "/api/stats/reset-cache-rate?provider="+tc.provider, nil)
		r.Header.Set("Authorization", "Bearer mistral-client")
		if tc.admin {
			for _, cookie := range login.Result().Cookies() {
				r.AddCookie(cookie)
			}
		}
		if tc.csrf {
			r.Header.Set("X-CSRF-Token", session.csrf)
		}
		w := httptest.NewRecorder()
		e.s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("provider=%q admin=%v csrf=%v: %d %s", tc.provider, tc.admin, tc.csrf, w.Code, w.Body.String())
		}
		if tc.status != http.StatusOK && e.s.stats.SummaryFor("mistral").Total != before {
			t.Fatal("rejected request reset counters")
		}
	}
	after := e.s.stats.SummaryFor("mistral").Total
	if after.CachePrompt != 0 || after.CacheCached != 0 || after.CacheKnownRequests != 0 || after.Input != before.Input || after.Cached != before.Cached || after.Requests != before.Requests {
		t.Fatalf("incorrect reset: %+v", after)
	}
	if e.s.stats.SummaryFor("river").Total != other {
		t.Fatal("other provider reset")
	}
	var md metadata
	data, err := os.ReadFile(filepath.Join(e.s.dataDir, "metadata.json"))
	if err != nil || json.Unmarshal(data, &md) != nil {
		t.Fatal("reset not saved immediately")
	}
	restored := stats.New()
	restored.Import(md.Stats)
	if restored.SummaryFor("mistral").Total != after || restored.SummaryFor("river").Total != other {
		t.Fatal("disk reset/other pool counters changed")
	}
	configAfter, _ := os.ReadFile(filepath.Join(e.s.dataDir, "config.json"))
	if !reflect.DeepEqual(configBefore, configAfter) {
		t.Fatal("reset changed configuration")
	}
}

func TestResetCacheRateReportsPersistenceFailure(t *testing.T) {
	e := newMistralEnv(t)
	input, cached := int64(100), int64(80)
	e.s.stats.Record(stats.Sample{Provider: "mistral", Input: &input, Cached: &cached, Prompt: 100})
	// A directory at the metadata destination forces atomic rename to fail.
	e.s.dataDir = t.TempDir()
	if err := os.Mkdir(filepath.Join(e.s.dataDir, "metadata.json"), 0700); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	e.s.apiResetCacheRate(w, httptest.NewRequest("POST", "/api/stats/reset-cache-rate?provider=mistral", nil))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "已在内存清空，但保存失败") {
		t.Fatalf("persistence failure hidden: %d %s", w.Code, w.Body.String())
	}
	if b := e.s.stats.SummaryFor("mistral").Total; b.CacheKnownRequests != 0 || b.Cached != 80 || b.Requests != 1 {
		t.Fatalf("incorrect in-memory failure outcome: %+v", b)
	}
}
