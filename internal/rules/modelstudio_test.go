package rules

import (
	"net/http"
	"testing"
)

// The shapes below are the ones the Model Studio endpoints were measured to
// return (2026-11-11) and the ones the official error catalogue documents.
// DashScope nests the error code inside "error", which Match.BodyCodes does
// not read, so every rule must key off text that actually appears.
func TestModelStudioRuleMatching(t *testing.T) {
	errs := DefaultModelStudioErrorRules()
	retries := DefaultModelStudioRetryRules()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string // expected error rule id, "" for none
	}{
		{"exhausted", 429, `{"error":{"message":"Your token-plan quota has been exhausted.","type":"invalid_request_error","code":"insufficient_quota"}}`, "modelstudio-quota-exhausted"},
		{"throttle", 429, `{"error":{"message":"Too many requests.","type":"throttling","code":"Throttling.ResourceExhausted"}}`, "modelstudio-rate-limited"},
		{"concurrency", 429, `{"error":{"message":"concurrency allocated quota exceeded","code":"Throttling.Concurrency"}}`, "modelstudio-concurrency-limit"},
		// the metered host's wording for a wrong key family
		{"invalid key", 401, `{"error":{"message":"Incorrect API key provided. For details, see: https://www.alibabacloud.com/help/en/model-studio/error-code#apikey-error","type":"invalid_request_error","param":null,"code":"invalid_api_key"},"request_id":"e999f2a6"}`, "modelstudio-invalid-key"},
		// Token Plan key against a Coding Plan host
		{"wrong host", 401, `{"error":{"code":"invalid_api_key","message":"invalid access token or token expired","param":null,"type":"invalid_request_error"},"request_id":"eae918cb"}`, "modelstudio-invalid-key"},
		// the Model Studio Token Plan answers a model outside the plan this way
		{"model not exist anthropic", 400, `{"request_id":"35884866","code":"InvalidParameter","message":"Model not exist."}`, "modelstudio-model-not-entitled"},
		{"model not exist openai", 404, `{"error":{"message":"Model not exist.","id":"a8ce4de9","type":"invalid_request_error","code":"model_not_found"}}`, "modelstudio-model-not-entitled"},
		{"unpurchased", 403, `{"request_id":"x","code":"AccessDenied.Unpurchased","message":"Access to model denied"}`, "modelstudio-model-not-entitled"},
		{"service unavailable", 503, `{"request_id":"x","code":"ServiceUnavailable","message":"The service is unavailable"}`, "modelstudio-service-unavailable"},
		{"server error", 500, `{"request_id":"x","code":"InternalError","message":"boom"}`, "modelstudio-server-error"},
		{"unmatched", 400, `{"error":{"message":"Unexpected item type in content.","code":"invalid_parameter_error"}}`, ""},
	} {
		h := http.Header{}
		got := MatchError(errs, tc.status, tc.body, h)
		if tc.want == "" {
			if got != nil && got.ID == "modelstudio-model-not-entitled" {
				t.Errorf("%s: unexpectedly matched %s", tc.name, got.ID)
			}
			continue
		}
		if got == nil || got.ID != tc.want {
			t.Errorf("%s: matched %v, want %s", tc.name, got, tc.want)
			continue
		}
		if tc.name == "exhausted" && (got.Scope != ScopeAccount || got.Action != ActionCooldown || got.DownstreamStatus != 503 || !got.RefreshQuota) {
			t.Errorf("exhausted rule must cool the account to 503 with a refresh: %+v", got)
		}
		if tc.name == "model not exist anthropic" && got.Scope != ScopeModel {
			t.Errorf("model-not-entitled must be model-scoped, not quarantine the account")
		}
		if tc.name == "invalid key" && got.Action != ActionHardQuarantine {
			t.Errorf("invalid key must quarantine: %+v", got)
		}
	}
	// the retry rules must stop on a definitively unentitled model
	if r := MatchRetry(retries, 404, `{"error":{"message":"Model not exist.","code":"model_not_found"}}`, nil); r == nil || r.Decision != DecisionStop {
		t.Errorf("model_not_found should stop failover, got %v", r)
	}
	// and must keep retrying on a plain rate limit
	if r := MatchRetry(retries, 429, `{"error":{"message":"Too many requests."}}`, nil); r != nil {
		t.Errorf("a plain 429 must keep failing over, got %v", r)
	}
}
