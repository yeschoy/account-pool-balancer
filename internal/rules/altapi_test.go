package rules

import (
	"encoding/json"
	"testing"
)

// altapi answers HTTP 200 for many refusals, so a rule that only matches a
// status would let an invalid key through as a success. These cases are
// verbatim bodies captured from altapi.ai on 2026-11-15.
func TestALTAPIRulesMatchMeasuredBodies(t *testing.T) {
	errRules := DefaultALTAPIErrorRules()

	cases := []struct {
		name   string
		status int
		body   string
		wantID string
	}{
		{
			name:   "invalid token with HTTP 200",
			status: 200,
			body:   `{"error":{"message":"Invalid token (request id: 20261114214013669949624Z9djGfWX)","type":"new_api_error"}}`,
			wantID: "altapi-token-invalid",
		},
		{
			name:   "console session refused",
			status: 200,
			body:   `{"message":"无权进行此操作，access token 无效","success":false}`,
			wantID: "altapi-token-invalid",
		},
		{
			// Measured 2026-11-15: this arrives with HTTP 400, and the code
			// sits at error.code, which Match.BodyCodes cannot read (it only
			// looks at the top level). A status-bound or code-only rule
			// therefore never fires and the failure falls through to the
			// account-wide upstream rule, cooling the whole account for a
			// condition that only affects one model.
			name:   "model has no deployment node (HTTP 400, nested code)",
			status: 400,
			body:   `{"error":{"message":"Model DeepSeek-V3.2 not found, no deployment node exists","type":"upstream_error","param":"","code":"MODEL_NOT_SUPPORT"}}`,
			wantID: "altapi-model-not-support",
		},
		{
			name:   "upstream saturated",
			status: 400,
			body:   `{"error":{"message":"当前分组上游负载已饱和，请稍后再试","type":"new_api_error"}}`,
			wantID: "altapi-upstream-error",
		},
		{
			name:   "wallet empty",
			status: 403,
			body:   `{"error":{"message":"用户额度不足, 剩余额度: ＄0.000000","type":"new_api_error"}}`,
			wantID: "altapi-insufficient-quota",
		},
		{
			name:   "bad request must not cool the account",
			status: 400,
			body:   `{"error":{"message":"invalid_request_error: messages is required","type":"invalid_request_error"}}`,
			wantID: "altapi-bad-request",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchErrorRules(errRules, tc.status, tc.body, nil)
			if got == "" {
				t.Fatalf("no rule matched %s / %s", tc.name, tc.body)
			}
			if got != tc.wantID {
				t.Fatalf("matched %q, want %q", got, tc.wantID)
			}
		})
	}
}

// The retry rules decide when rotating accounts is pointless. An invalid key
// is the account's own defect: switching accounts would replay the same bad
// key, so the request must stop instead of walking the whole pool.
func TestALTAPIRetryRulesStopOnAccountDefects(t *testing.T) {
	ret := DefaultALTAPIRetryRules()
	for _, tc := range []struct{ name, body string }{
		{"invalid token", `{"error":{"message":"Invalid token (request id: x)","type":"new_api_error"}}`},
		{"bad request", `{"error":{"message":"invalid_request_error: max_tokens","type":"invalid_request_error"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := matchRetryRules(ret, 400, tc.body, nil); got != DecisionStop {
				t.Fatalf("decision %q, want %q", got, DecisionStop)
			}
		})
	}
}

// A 429 means the account's own key set shares the limit; rotating keys or
// accounts cannot escape it, so the client is told to wait.
func TestALTAPIRateLimitStopsRotation(t *testing.T) {
	if got := matchRetryRules(DefaultALTAPIRetryRules(), 429,
		`{"error":{"message":"当前分组上游负载已饱和"}}`, nil); got != DecisionStop {
		t.Fatalf("429 decision %q, want stop", got)
	}
}

// Match helpers: keep these local so the test does not depend on unexported
// engine internals.
func matchErrorRules(rs []ErrorRule, status int, body string, hdr map[string]string) string {
	for _, r := range rs {
		if r.Disabled || !matchOK(r.When, status, body, hdr) {
			continue
		}
		return r.ID
	}
	return ""
}

func matchRetryRules(rs []RetryRule, status int, body string, hdr map[string]string) string {
	for _, r := range rs {
		if r.Disabled || !matchOK(r.When, status, body, hdr) {
			continue
		}
		return r.Decision
	}
	return ""
}

func matchOK(m Match, status int, body string, hdr map[string]string) bool {
	if len(m.Statuses) == 0 && len(m.BodyContains) == 0 && len(m.BodyCodes) == 0 && len(m.Headers) == 0 {
		return false
	}
	if len(m.Statuses) > 0 {
		ok := false
		for _, s := range m.Statuses {
			if s == status {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(m.BodyContains) > 0 {
		ok := false
		for _, sub := range m.BodyContains {
			if containsFold(body, sub) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if len(m.BodyCodes) > 0 {
		var env struct {
			Error struct {
				Code json.RawMessage `json:"code"`
			} `json:"error"`
			Code json.RawMessage `json:"code"`
		}
		_ = json.Unmarshal([]byte(body), &env)
		code := ""
		for _, raw := range []json.RawMessage{env.Error.Code, env.Code} {
			if len(raw) == 0 {
				continue
			}
			var s string
			if json.Unmarshal(raw, &s) == nil {
				code = s
			} else {
				var n json.Number
				if json.Unmarshal(raw, &n) == nil {
					code = n.String()
				}
			}
			if code != "" {
				break
			}
		}
		ok := false
		for _, want := range m.BodyCodes {
			if code == want {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for k, v := range m.Headers {
		got, present := hdr[k]
		if !present {
			return false
		}
		if v != "" && !containsFold(got, v) {
			return false
		}
	}
	return true
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 || indexFold(s, sub) >= 0
}

func indexFold(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 {
		return 0
	}
	for i := 0; i+m <= n; i++ {
		ok := true
		for j := 0; j < m; j++ {
			if lower(s[i+j]) != lower(sub[j]) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func lower(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + 32
	}
	return b
}

// altapi answers HTTP 200 for a rejected key. The proxy normalizes that body
// to 401 (proxy.errorStatus), and the rule then must hard-quarantine instead
// of letting the request through as a success.
func TestALTAPITokenInvalidIsQuarantineNotIgnore(t *testing.T) {
	var got *ErrorRule
	for _, r := range DefaultALTAPIErrorRules() {
		if r.ID == "altapi-token-invalid" {
			rr := r
			got = &rr
			break
		}
	}
	if got == nil {
		t.Fatal("altapi-token-invalid rule missing")
	}
	if got.Action != ActionHardQuarantine {
		t.Fatalf("action %q, want %q", got.Action, ActionHardQuarantine)
	}
	if got.Scope != ScopeAccount {
		t.Fatalf("scope %q, want account", got.Scope)
	}
}

// A model without a deployment node cools only that account×model: the same
// account serves other models fine, so a whole-account cooldown would waste a
// paid seat.
func TestALTAPIModelNotSupportIsModelScoped(t *testing.T) {
	for _, r := range DefaultALTAPIErrorRules() {
		if r.ID != "altapi-model-not-support" {
			continue
		}
		if r.Scope != ScopeModel {
			t.Fatalf("scope %q, want model", r.Scope)
		}
		if r.Action != ActionCooldown {
			t.Fatalf("action %q, want cooldown", r.Action)
		}
		return
	}
	t.Fatal("altapi-model-not-support rule missing")
}

// An empty quota must refresh the real balance right away instead of guessing.
func TestALTAPIInsufficientQuotaRefreshesBalance(t *testing.T) {
	for _, r := range DefaultALTAPIErrorRules() {
		if r.ID != "altapi-insufficient-quota" {
			continue
		}
		if !r.RefreshQuota {
			t.Fatal("insufficient-quota rule does not request a quota refresh")
		}
		if r.Reset == nil || !r.Reset.Escalate {
			t.Fatal("insufficient-quota rule should escalate on consecutive hits")
		}
		return
	}
	t.Fatal("altapi-insufficient-quota rule missing")
}
