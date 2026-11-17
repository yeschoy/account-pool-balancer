package rules

import (
	"net/http"
	"testing"
)

func TestFactoryDefaultsAndRetryAfter(t *testing.T) {
	er, rr := DefaultFactoryErrorRules(), DefaultFactoryRetryRules()
	if ValidateErrorRules(er) != nil || ValidateRetryRules(rr) != nil {
		t.Fatal("invalid defaults")
	}
	for _, st := range []int{400, 402, 403, 404, 405, 409, 413, 422, 429} {
		if MatchRetry(rr, st, "", http.Header{}) == nil {
			t.Fatal("missing stop", st)
		}
	}
	rule := MatchError(er, 403, `{"code":"content_policy_rejected"}`, http.Header{})
	if rule == nil || rule.Action != ActionIgnore {
		t.Fatal("policy health mutation")
	}
	rate := MatchError(er, 429, "", http.Header{"Retry-After": {"172800"}})
	if rate == nil || rate.Reset == nil || rate.Reset.Format != "retry-after" {
		t.Fatal("rate reset")
	}
}
