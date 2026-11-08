package rules

import "testing"

func TestBodyCodesStringsAndLosslessNumbers(t *testing.T) {
	for _, tc := range []struct {
		body, code string
		want       bool
	}{
		{`{"code":3800}`, "3800", true},
		{`{"code":"3800"}`, "3800", true},
		{`{"errorCode":"BANNED"}`, "banned", true},
		{`{"errorCode":3800}`, "3800", true},
		{`{"code":9007199254740993}`, "9007199254740993", true},
		{`{"code":9007199254740993}`, "9007199254740992", false},
		{`{"code":true}`, "true", false},
		{`{"code":null}`, "null", false},
		{`{"code":{"code":"3800"}}`, "3800", false},
		{`{"error":{"code":3800}}`, "3800", false},
		{`not JSON`, "3800", false},
	} {
		m := Match{BodyCodes: []string{tc.code}}
		if got := m.matches(503, tc.body, nil); got != tc.want {
			t.Errorf("code %q in %s: %v", tc.code, tc.body, got)
		}
	}
}
