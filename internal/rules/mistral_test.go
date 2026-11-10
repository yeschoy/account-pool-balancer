package rules

import (
	"net/http"
	"testing"
)

func TestMistralPreciseTierAndCapacityRules(t *testing.T) {
	errors, retries := DefaultMistralErrorRules(), DefaultMistralRetryRules()
	if err := ValidateErrorRules(errors); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRetryRules(retries); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body, errorID, retryID string
		status                       int
	}{
		{"tier string", `{"code":"1910","type":"tier_not_allowed"}`, "mistral-tier-not-allowed", "mistral-request-error", 403},
		{"tier number", `{"code":1910}`, "mistral-tier-not-allowed", "mistral-request-error", 403},
		{"capacity string", `{"code":"3505","type":"backend_out_of_capacity"}`, "mistral-backend-out-of-capacity", "mistral-backend-out-of-capacity", 429},
		{"capacity number", `{"code":3505}`, "mistral-backend-out-of-capacity", "mistral-backend-out-of-capacity", 429},
		{"other forbidden", `{"code":1911}`, "", "mistral-request-error", 403},
		{"tier message only", `{"message":"tier_not_allowed code1910"}`, "", "mistral-request-error", 403},
		{"tier nested", `{"error":{"code":1910}}`, "", "mistral-request-error", 403},
		{"ordinary account rate limit", `{"code":1300}`, "mistral-rate-limited", "", 429},
		{"capacity substring", `{"code":"35050"}`, "mistral-rate-limited", "", 429},
		{"capacity message only", `{"message":"backend_out_of_capacity 3505"}`, "mistral-rate-limited", "", 429},
		{"capacity nested", `{"error":{"code":3505}}`, "mistral-rate-limited", "", 429},
		{"capacity wrong status", `{"code":3505}`, "mistral-server-error", "", 500},
		{"tier wrong status", `{"code":1910}`, "mistral-rate-limited", "", 429},
		{"service unavailable unchanged", `{"code":3800}`, "mistral-service-unavailable", "mistral-service-unavailable", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := MatchError(errors, tc.status, tc.body, http.Header{})
			id := ""
			if r != nil {
				id = r.ID
			}
			if id != tc.errorID {
				t.Fatalf("error rule = %q, want %q", id, tc.errorID)
			}
			retry := MatchRetry(retries, tc.status, tc.body, http.Header{})
			id = ""
			if retry != nil {
				id = retry.ID
			}
			if id != tc.retryID {
				t.Fatalf("retry rule = %q, want %q", id, tc.retryID)
			}
		})
	}
}
