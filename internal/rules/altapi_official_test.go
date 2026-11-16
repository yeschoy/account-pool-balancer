package rules

import (
	"net/http"
	"testing"
	"time"
)

func TestALTAPIOfficialAuthenticationVariants(t *testing.T) {
	for _, body := range []string{
		`{"error":{"message":"Invalid API key provided","type":"authentication_error"}}`,
		`{"error":{"code":"invalid_api_key"}}`,
		`{"error":{"message":"Invalid token","type":"new_api_error"}}`,
		`{"error":{"message":"Token has expired"}}`,
		`{"error":{"message":"无效的令牌"}}`,
	} {
		for _, status := range []int{200, 401} {
			er := MatchError(DefaultALTAPIErrorRules(), status, body, nil)
			rr := MatchRetry(DefaultALTAPIRetryRules(), status, body, nil)
			if er == nil || er.ID != "altapi-token-invalid" || er.Action != ActionHardQuarantine || rr == nil || rr.Decision != DecisionStop {
				t.Fatalf("auth not quarantined/stopped: %d %s", status, body)
			}
		}
	}
}

func TestALTAPIModelPermissionPrecedesGenericRequest(t *testing.T) {
	const body = `{"error":{"message":"The model does not exist or you do not have access to it","type":"invalid_request_error"}}`
	for _, status := range []int{200, 400, 401, 403, 404} {
		h := http.Header{"Cf-Ray": []string{"normal-transit"}}
		er := MatchError(DefaultALTAPIErrorRules(), status, body, h)
		rr := MatchRetry(DefaultALTAPIRetryRules(), status, body, h)
		if er == nil || er.ID != "altapi-model-forbidden" || er.Scope != ScopeModel || er.Action != ActionCooldown {
			t.Fatalf("model permission misclassified: status=%d rule=%+v", status, er)
		}
		if rr != nil {
			t.Fatalf("model permission matched generic stop: %+v", rr)
		}
	}
}

func TestALTAPICFChallengeNeedsPositiveEvidence(t *testing.T) {
	for _, status := range []int{403, 429} {
		r := MatchError(DefaultALTAPIErrorRules(), status, `{"error":{"message":"unrecognized upstream refusal"}}`, http.Header{"Cf-Ray": []string{"normal-transit"}})
		if r != nil && r.ID == "altapi-cf-block" {
			t.Fatal("CF-Ray alone is not a challenge")
		}
		h := http.Header{"Cf-Mitigated": []string{"challenge"}}
		r = MatchError(DefaultALTAPIErrorRules(), status, "<html>challenge</html>", h)
		if r == nil || r.ID != "altapi-cf-block" {
			t.Fatalf("challenge not classified before generic rate rule: %+v", r)
		}
		if rr := MatchRetry(DefaultALTAPIRetryRules(), status, "<html>challenge</html>", h); rr == nil || rr.Decision != DecisionStop {
			t.Fatal("challenge must stop account rotation")
		}
	}
}

func TestALTAPIRateLimitFallbackBackoff(t *testing.T) {
	r := MatchError(DefaultALTAPIErrorRules(), 429, "", nil)
	if r == nil || r.ID != "altapi-rate-limit" {
		t.Fatal("bare 429 must have a cooldown")
	}
	for strikes, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute, 8: 15 * time.Minute} {
		if got := CooldownAfter(r, nil, "", time.Now(), strikes); got != want {
			t.Fatalf("strike=%d duration=%s want=%s", strikes, got, want)
		}
	}
	if got := CooldownAfter(r, http.Header{"Retry-After": []string{"123"}}, "", time.Now(), 4); got != 123*time.Second {
		t.Fatalf("Retry-After did not override fallback: %s", got)
	}
}
