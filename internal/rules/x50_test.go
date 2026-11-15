package rules

import (
	"net/http"
	"testing"
	"time"
)

func TestX50Defaults(t *testing.T) {
	er, rr := DefaultX50ErrorRules(), DefaultX50RetryRules()
	if e := ValidateErrorRules(er); e != nil {
		t.Fatal(e)
	}
	if e := ValidateRetryRules(rr); e != nil {
		t.Fatal(e)
	}
	for _, st := range []int{401, 402, 403, 429, 500, 502, 503, 504} {
		if MatchError(er, st, "", nil) == nil {
			t.Fatal(st)
		}
	}
	if MatchError(er, 405, "Your Organization is currently paused", nil) != nil {
		t.Fatal("borrowed Deploy semantics")
	}
	for _, st := range []int{400, 403, 404, 405, 429} {
		if r := MatchRetry(rr, st, "", nil); r == nil || r.Decision != DecisionStop {
			t.Fatal(st)
		}
	}
	h := http.Header{"Retry-After": []string{"172800"}}
	r := MatchError(er, 429, "", h)
	if Cooldown(r, h, "", time.Now()) != 48*time.Hour {
		t.Fatal("Retry-After truncated")
	}
}
