package thregister

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// siteSession builds the cookie the site actually sets: "base64-" + raw
// URL-safe base64 of the session JSON, chunked at 3180 bytes.
func siteSession(t *testing.T, userID string, expires int64) []NamedCookie {
	t.Helper()
	sess := map[string]any{
		"access_token": "eyJhbGciOiJFUzI1NiJ9.payload.sig",
		"token_type":   "bearer",
		"expires_at":   expires,
		"user":         map[string]any{"id": userID, "email": "x@icloud.com"},
	}
	raw, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	value := "base64-" + base64.RawURLEncoding.EncodeToString(raw)
	const chunk = 3180
	var out []NamedCookie
	if len(value) <= chunk {
		out = append(out, NamedCookie{Name: "sb-auth-auth-token", Value: value})
		return out
	}
	for i, off := 0, 0; off < len(value); i, off = i+1, off+chunk {
		end := off + chunk
		if end > len(value) {
			end = len(value)
		}
		out = append(out, NamedCookie{Name: "sb-auth-auth-token." + itoa(i), Value: value[off:end]})
	}
	return out
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestSessionIdentitySingleCookie(t *testing.T) {
	cookies := siteSession(t, "9441a9f5-39a0-41b8-a1d9-013ebe8453f6", 1793819110)
	uid, exp := SessionIdentity(cookies)
	if uid != "9441a9f5-39a0-41b8-a1d9-013ebe8453f6" {
		t.Fatalf("user id: %q", uid)
	}
	if !exp.Equal(time.Unix(1793819110, 0)) {
		t.Fatalf("expiry: %v", exp)
	}
}

// The real session is bigger than one cookie and arrives as .0/.1 chunks; the
// chunks must be rejoined in index order, not in the order they were seen.
func TestSessionIdentityChunked(t *testing.T) {
	padding := make([]byte, 4000)
	for i := range padding {
		padding[i] = byte('a' + i%26)
	}
	sess := map[string]any{
		"access_token": string(padding),
		"expires_at":   int64(1793819200),
		"user":         map[string]any{"id": "u-1"},
	}
	raw, _ := json.Marshal(sess)
	value := "base64-" + base64.RawURLEncoding.EncodeToString(raw)
	var cookies []NamedCookie
	for i, off := 0, 0; off < len(value); off += 3180 {
		end := off + 3180
		if end > len(value) {
			end = len(value)
		}
		cookies = append(cookies, NamedCookie{Name: "sb-auth-auth-token." + itoa(i), Value: value[off:end]})
		i++
	}
	if len(cookies) < 2 {
		t.Fatalf("test needs a chunked session, got %d chunks", len(cookies))
	}
	// Hand them over reversed on purpose: rejoining by seen order would break
	// a >2-chunk session, which is exactly the regression this guards.
	for i, j := 0, len(cookies)-1; i < j; i, j = i+1, j-1 {
		cookies[i], cookies[j] = cookies[j], cookies[i]
	}
	uid, _ := SessionIdentity(cookies)
	if uid != "u-1" {
		t.Fatalf("chunks rejoined in the wrong order: %q", uid)
	}
}

func TestSessionIdentityWithoutSession(t *testing.T) {
	if uid, exp := SessionIdentity([]NamedCookie{{Name: "th_geo", Value: "1"}}); uid != "" || !exp.IsZero() {
		t.Fatalf("no session cookie must yield nothing, got %q %v", uid, exp)
	}
}
