// Package egressid picks the client identity an account presents to an
// upstream: the User-Agent and the client-hint headers that must agree with it.
//
// Two rules shape this package.
//
// First, an identity must be self-consistent. A browser User-Agent without the
// matching Sec-CH-UA hints is a stronger bot signal than an honest library
// name, so the hint set is derived from the same table row as the User-Agent
// and never assembled independently.
//
// Second, an identity must be stable per account and differ between accounts.
// One process-wide browser string makes every account in the pool share a
// single fingerprint, which is the property an upstream can cluster on. The
// choice is therefore a deterministic function of a caller-supplied seed (the
// account's stable id), so the same account always looks the same across
// restarts while two accounts do not look like one another.
//
// This package deliberately does not claim to defeat fingerprinting. It gives
// each account a plausible, internally consistent identity; TLS and TCP
// fingerprints are out of its scope and are not changed by it.
package egressid

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"strconv"
	"strings"
)

// Identity is one coherent client identity: a User-Agent plus the hints a real
// browser of that build sends with it.
type Identity struct {
	// UserAgent is the value for the User-Agent request header.
	UserAgent string
	// Brand is the first brand in Sec-CH-UA, e.g. "Google Chrome".
	Brand string
	// BrandVersion is the significant version in Sec-CH-UA, e.g. "138".
	BrandVersion string
	// Platform is the Sec-CH-UA-Platform value, e.g. `"macOS"` (it is quoted).
	Platform string
	// PlatformVersion is the Sec-CH-UA-Platform-Version value.
	PlatformVersion string
	// Mobile is the Sec-CH-UA-Mobile value ("?0" or "?1").
	Mobile string
	// AcceptLanguage is the Accept-Language value that platform would send.
	AcceptLanguage string
}

// Header returns the request headers this identity presents. The returned map
// is a fresh copy each call so callers can add their own entries safely.
func (i Identity) Header() map[string]string {
	return map[string]string{
		"User-Agent":                 i.UserAgent,
		"sec-ch-ua":                  BuildBrandList(i.Brand, i.BrandVersion),
		"sec-ch-ua-mobile":           i.Mobile,
		"sec-ch-ua-platform":         i.Platform,
		"sec-ch-ua-platform-version": i.PlatformVersion,
	}
}

// BuildBrandList renders a Sec-CH-UA brand list in the order Chrome emits it:
// the browser's own brand first, then the Chromium alias, then "Not)A;Brand".
func BuildBrandList(brand, version string) string {
	return `"` + brand + `";v="` + version + `", "Chromium";v="` + version + `", "Not)A;Brand";v="99"`
}

// spot is one row of the table: a plausible, currently-supported desktop
// browser build. Entries are deliberately limited to evergreen desktop builds
// whose hint set is unambiguous; adding a mobile or an end-of-life build here
// would need a different Accept-Language and hint shape, which this table does
// not model.
type spot struct {
	userAgent       string
	brand           string
	version         string
	platform        string
	platformVersion string
	acceptLanguage  string
}

// spots is the identity table. Keeping several platform/version combinations
// is what makes two accounts distinguishable without looking implausible;
// every row is a real, self-consistent build.
var spots = []spot{
	{
		userAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "138",
		platform:        `"macOS"`,
		platformVersion: `"15.5.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "137",
		platform:        `"macOS"`,
		platformVersion: `"14.6.1"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "138",
		platform:        `"Windows"`,
		platformVersion: `"15.0.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "137",
		platform:        `"Windows"`,
		platformVersion: `"10.0.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "138",
		platform:        `"Linux"`,
		platformVersion: `"6.8.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/138.0.0.0 Safari/537.36 Edg/138.0.0.0",
		brand:           "Microsoft Edge",
		version:         "138",
		platform:        `"macOS"`,
		platformVersion: `"15.5.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
}

// For returns the identity an account presents. An empty seed still yields a
// valid identity so a caller without a stable id is not forced into the Go
// default; callers that have an account id should pass it.
func For(seed string) Identity {
	i := indexFor(seed, len(spots))
	s := spots[i]
	return Identity{
		UserAgent:       s.userAgent,
		Brand:           s.brand,
		BrandVersion:    s.version,
		Platform:        s.platform,
		PlatformVersion: s.platformVersion,
		Mobile:          "?0",
		AcceptLanguage:  s.acceptLanguage,
	}
}

// indexFor maps a seed to a table slot. crypto/sha256 rather than maphash keeps
// the choice stable across processes: two restarts must not change what an
// account looks like, or the upstream sees the same account switch identity.
func indexFor(seed string, n int) int {
	sum := sha256.Sum256([]byte(seed))
	return int(binary.BigEndian.Uint32(sum[:4]) % uint32(n))
}

// Len reports how many distinct identities the table offers, for callers that
// want to report or test the spread.
func Len() int { return len(spots) }

// UserAgentFor is For(seed).UserAgent, for call sites that only need the
// header value.
func UserAgentFor(seed string) string { return For(seed).UserAgent }

// Setter is the subset of http.Header these helpers need.
type Setter interface {
	Set(string, string)
}

// Apply writes an identity's headers onto h, replacing any value already
// present for those same keys so a caller-supplied generic library string can
// never survive alongside the chosen browser identity.
func Apply(h Setter, id Identity) {
	for k, v := range id.Header() {
		h.Set(k, v)
	}
}

// MarshalJSON encodes the non-secret display fields, so an identity can reach
// the console without leaking anything the upstream does not already see.
func (i Identity) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		UserAgent       string `json:"userAgent"`
		Brand           string `json:"brand"`
		Platform        string `json:"platform"`
		PlatformVersion string `json:"platformVersion"`
	}{
		UserAgent:       i.UserAgent,
		Brand:           i.Brand,
		Platform:        strings.Trim(i.Platform, `"`),
		PlatformVersion: strings.Trim(i.PlatformVersion, `"`),
	})
}

// ChromeMajor extracts the leading major version from a Chrome-like
// User-Agent, or "" when the string is not one. Used to check that a hint and
// its User-Agent still agree.
func ChromeMajor(ua string) string {
	const marker = "Chrome/"
	i := strings.Index(ua, marker)
	if i < 0 {
		return ""
	}
	rest := ua[i+len(marker):]
	end := strings.IndexAny(rest, ". ")
	if end < 0 {
		return ""
	}
	if _, err := strconv.Atoi(rest[:end]); err != nil {
		return ""
	}
	return rest[:end]
}
