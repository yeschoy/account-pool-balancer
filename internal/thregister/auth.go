package thregister

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var sessionCookieName = regexp.MustCompile(`^sb-[A-Za-z0-9_-]+-auth-token(?:\.[0-9]+)?$`)

// IsSessionCookie excludes the PKCE code verifier: it is an authentication
// input, not the authenticated session produced by a successful sign-in.
func IsSessionCookie(name string) bool {
	return sessionCookieName.MatchString(name)
}

// HasSession reports only the presence of an auth-token cookie, not whether
// the upstream accepts it. The authenticated /api/keys probe remains required.
func HasSession(cookies []NamedCookie) bool {
	for _, c := range cookies {
		if c.Value != "" && IsSessionCookie(c.Name) {
			return true
		}
	}
	return false
}

// React server actions return business errors with HTTP 200. Accept only the
// structured error/needCaptcha fields, never arbitrary HTML or Flight text.
func authActionError(body []byte) (string, bool) {
	parse := func(raw []byte) (string, bool) {
		var result struct {
			NeedCaptcha bool `json:"needCaptcha"`
		}
		if json.Unmarshal(raw, &result) != nil {
			return "", false
		}
		return jsonError(raw), result.NeedCaptcha
	}
	if json.Valid(body) {
		return parse(body)
	}
	for _, line := range strings.Split(string(body), "\n") {
		id, raw, ok := strings.Cut(line, ":")
		if !ok || len(id) == 0 || len(id) > 8 {
			continue
		}
		if _, err := strconv.ParseUint(id, 16, 32); err != nil {
			continue
		}
		if message, captcha := parse([]byte(raw)); message != "" || captcha {
			return message, captcha
		}
	}
	return "", false
}
