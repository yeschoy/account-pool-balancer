package thregister

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func passModelsFixture() string {
	raw, _ := json.Marshal(map[string]any{"models": map[string]any{"agent-model": map[string]string{"tier": "agent"}, "office-model": map[string]string{"tier": "office"}, "free-model": map[string]string{"tier": "free"}}, "costs": map[string]int{}})
	chunk, _ := json.Marshal([]any{1, "b:" + string(raw) + "\n"})
	return "<script>self.__next_f.push(" + string(chunk) + ")</script>"
}
func TestObserveAuthenticatedPlanAndIndependentWeeklyWindow(t *testing.T) {
	calls := 0
	reset := time.Now().UTC().Add(72 * time.Hour).Truncate(time.Second)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.Header.Get("Cookie") != "session=saved" {
			t.Error("observation changed state or lost saved session")
		}
		switch r.URL.Path {
		case "/pricing":
			fmt.Fprint(w, pricingFixture("plus")+passModelsFixture())
		case "/api/me/free-tier":
			fmt.Fprintf(w, `{"ok":true,"on_plan":true,"activated":true,"plan_used_pct":25,"plan_exhausted":false,"overage_billing":false,"period_ends_at":%q,"plan":{"name":"Agent Pass","window_days":7}}`, reset.Format(time.RFC3339Nano))
		default:
			t.Error("unexpected upstream path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer up.Close()
	f, err := New(Config{BaseURL: up.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.SeedCookies([]NamedCookie{{Name: "session", Value: "saved"}})
	got, err := f.Observe(context.Background())
	if err != nil || got.Subscription.Tier != "plus" || got.Models["agent-model"] != "agent" || got.Usage.UsedPercent == nil || *got.Usage.UsedPercent != 25 || !got.Usage.ResetsAt.Equal(reset) || got.Usage.Error != "" || calls != 2 {
		t.Fatalf("bad observation %v %+v calls=%d", err, got, calls)
	}
}
func TestPassUsageRejectsUnknownOrInconsistentMeasurements(t *testing.T) {
	base := `{"ok":true,"on_plan":true,"activated":false,"plan_used_pct":0,"plan_exhausted":false,"overage_billing":false,"plan":{"name":"Agent Pass","window_days":7}}`
	good, err := parsePassUsage([]byte(base), "Agent Pass")
	if err != nil || good.UsedPercent == nil || *good.UsedPercent != 0 || !good.ResetsAt.IsZero() {
		t.Fatal("unstarted window should be measured without an invented reset")
	}
	for _, body := range []string{`{}`, strings.Replace(base, `"plan_used_pct":0,`, "", 1), strings.Replace(base, `"plan_used_pct":0`, `"plan_used_pct":101`, 1), strings.Replace(base, `"activated":false`, `"activated":true`, 1), strings.Replace(base, `"on_plan":true`, `"on_plan":false`, 1), strings.Replace(base, `"plan_exhausted":false`, `"plan_exhausted":true`, 1), strings.Replace(base, "Agent Pass", "Office Pass", 1)} {
		if _, err := parsePassUsage([]byte(body), "Agent Pass"); err == nil {
			t.Fatal("invalid/unknown usage accepted", body)
		}
	}
}
func TestPassMappingFailsClosedOnAmbiguityAndUnknownTier(t *testing.T) {
	for _, body := range []string{"<html>Agent Pass</html>", passModelsFixture() + passModelsFixture(), strings.ReplaceAll(passModelsFixture(), "agent", "low")} {
		if _, err := ParsePassModels([]byte(body)); err == nil {
			t.Fatal("invalid coverage accepted")
		}
	}
}
