package factoryauth

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"testing"
)

func claimsJWT(payload map[string]any) string {
	b, _ := json.Marshal(payload)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

// WorkOS may put the authorization organization in org_id, organization_id or
// an array of both; a device-login token can also carry none of them.
func TestJWTClaimsOfShapes(t *testing.T) {
	for _, tc := range []struct {
		payload map[string]any
		want    []string
	}{
		{map[string]any{"org_id": "org_a"}, []string{"org_a"}},
		{map[string]any{"organization_id": "org_b"}, []string{"org_b"}},
		{map[string]any{"external_org_id": "org_c"}, []string{"org_c"}},
		{map[string]any{"org_id": []string{"org_a", "org_b"}}, []string{"org_a", "org_b"}},
		{map[string]any{"organization_id": []any{"org_x"}}, []string{"org_x"}},
		{map[string]any{"sub": "u"}, nil},
	} {
		got := JWTClaimsOf(claimsJWT(tc.payload))
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%v => %v, want %v", tc.payload, got, tc.want)
		}
	}
}

func TestParseTokenPicksClaimOrganization(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"access_token":  claimsJWT(map[string]any{"sub": "u", "org_id": []string{"org_first", "org_second"}, "exp": 1}),
		"refresh_token": "r",
	})
	tok, err := parseToken(body, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if tok.OrgID != "org_first" || tok.UserID != "u" {
		t.Fatalf("token = %+v", tok)
	}
}

// The authorize response may nest the organization instead of naming it in
// active_organization_id.
func TestParseTokenNestedOrganization(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"access_token":  claimsJWT(map[string]any{"sub": "u", "exp": 1}),
		"refresh_token": "r",
		"organization":  map[string]string{"id": "org_nested"},
	})
	tok, err := parseToken(body, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if tok.OrgID != "org_nested" {
		t.Fatalf("org = %q", tok.OrgID)
	}
}
