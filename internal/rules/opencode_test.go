package rules

import (
	"net/http"
	"testing"
	"time"
)

func TestOpenCodeLongRetryAfterIsNotShortened(t *testing.T) {
	header := http.Header{"Retry-After": []string{"2592000"}}
	rule := MatchError(DefaultOpenCodeErrorRules(), 429, "", header)
	if rule == nil || Cooldown(rule, header, "", time.Now()) != 30*24*time.Hour {
		t.Fatal("subscription reset shortened by generic cooldown ceiling")
	}
	if !IsOpenCodePolicyRefusal(403, "FreeTierError") || IsOpenCodePolicyRefusal(401, "FreeTierError") || IsOpenCodePolicyRefusal(403, "model permission denied") {
		t.Fatal("policy classifier over/under-matched")
	}
}
