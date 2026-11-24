package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Factory's 402 names the wait itself: "You've reached your 5-hour standard
// usage limit (resets in 0h 52min)". A fixed cooldown would retry inside the
// window, be refused again, and tell the caller a wait that is not the real one.
func TestFactoryFundingUsesStatedReset(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		body string
		want time.Duration
		ok   bool
	}{
		{`{"detail":"You've reached your 5-hour standard usage limit (resets in 0h 52min).\nSwitch to Droid Core or enable Extra Usage to continue."}`, 52 * time.Minute, true},
		{"weekly usage limit (resets in 4h 5min).", 4*time.Hour + 5*time.Minute, true},
		{"(resets in 0h 0min)", 0, false},
		{`{"detail":"billing refused"}`, 0, false},
	} {
		got, ok := factoryQuotaReset(strings.ToLower(tc.body), now)
		if ok != tc.ok {
			t.Fatalf("%q: ok=%t want %t", tc.body, ok, tc.ok)
		}
		if ok && (got.Before(now.Add(tc.want-time.Minute)) || got.After(now.Add(tc.want+time.Minute))) {
			t.Fatalf("%q: got %v want ~%v", tc.body, got.Sub(now), tc.want)
		}
	}

	e := newFactoryEnv(t)
	const body = `{"detail":"You've reached your 5-hour standard usage limit (resets in 0h 52min).\nSwitch to Droid Core or enable Extra Usage to continue.","status":402,"title":"Payment Required","displayToUser":true,"requestId":"yul1::x"}`
	e.mu.Lock()
	e.handler = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(402)
		fmt.Fprint(w, body)
	}
	e.mu.Unlock()
	rsp, out := e.do(t, "/v1/chat/completions", `{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`, nil)
	if rsp.StatusCode != 503 {
		t.Fatalf("402 mapped to %d %s", rsp.StatusCode, out)
	}
	page, err := e.s.logs.Query("errors", nil, time.Time{}, time.Time{}, 10, 0)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("errors: %v rows=%d", err, len(page.Items))
	}
	var el errorLog
	if err := json.Unmarshal(page.Items[0], &el); err != nil {
		t.Fatal(err)
	}
	if el.RuleID != "factory-funding" || el.RetryAfterMs == nil {
		t.Fatalf("funding rule: %+v", el)
	}
	// 52 minutes, not the rule's 10m fallback.
	if ms := *el.RetryAfterMs; ms < int64(50*time.Minute/time.Millisecond) || ms > int64(54*time.Minute/time.Millisecond) {
		t.Fatalf("retry-after %dms, want ~52m", ms)
	}
	view := e.s.pool.Views(e.s.cfg.Get())["f1"]
	if view.State == nil || view.State.CooldownUntil.Before(time.Now().Add(50*time.Minute)) {
		t.Fatalf("account cooled for the window, not the fallback: %+v", view.State)
	}
}
