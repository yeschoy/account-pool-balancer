package rules

import (
	"net/http"
	"testing"
	"time"
)

func TestDeployDefaults(t *testing.T) {
	er, rr := DefaultDeployErrorRules(), DefaultDeployRetryRules()
	if err := ValidateErrorRules(er); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRetryRules(rr); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		status   int
		body, id string
	}{{401, "Invalid or expired token", "deploy-auth"}, {402, "payment required", "deploy-funding"}, {403, "no scope", "deploy-forbidden"}, {405, "Your Organization is currently paused. Please check your account balance, our terms and conditions", "deploy-organization-paused"}, {429, "Too many requests", "deploy-rate-limit"}, {503, "unavailable", "deploy-upstream"}} {
		r := MatchError(er, tc.status, tc.body, nil)
		if r == nil || r.ID != tc.id {
			t.Fatalf("%d matched %+v", tc.status, r)
		}
	}
	if r := MatchError(er, 405, "method not allowed", nil); r != nil {
		t.Fatal("generic 405 invented organization/balance evidence")
	}
	for _, st := range []int{400, 403, 404, 405, 409, 413, 422, 429} {
		if MatchRetry(rr, st, "", nil) == nil {
			t.Fatal("missing stop", st)
		}
	}
	r := MatchError(er, 429, "", http.Header{"Retry-After": []string{"172800"}})
	if r == nil || r.Reset == nil {
		t.Fatal("missing reset")
	}
	if got := Cooldown(r, http.Header{"Retry-After": []string{"172800"}}, "", time.Now()); got != 48*time.Hour {
		t.Fatal("full Retry-After lost", got)
	}
}
