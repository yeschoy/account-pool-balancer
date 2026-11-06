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
	// Seed is the selector this identity was derived from. It is not sent
	// anywhere; the client-hint brand permutation uses it so two accounts do
	// not share one Sec-CH-UA string.
	Seed string
}

// zone is one plausible IANA timezone a real user of a given egress region
// would report. The registration form asks the browser for
// Intl.DateTimeFormat().resolvedOptions().timeZone, so the value describes
// where the person sits, not the account: a single fixed value across a pool
// says "one operator", which is the clustering this package exists to avoid.
//
// Only regions the pools actually egress from are modelled. A timezone that
// contradicts the exit IP is a stronger signal than a boring one, so an
// unknown region yields nothing at all: the caller then keeps a value that is
// merely unremarkable rather than actively wrong.
type zone struct {
	region string
	name   string
	lang   string
}

// zones is the timezone table. Several zones per region, so two accounts on one
// egress region still differ while both stay believable there.
var zones = []zone{
	{"US", "America/New_York", "en-US,en;q=0.9"},
	{"US", "America/Chicago", "en-US,en;q=0.9"},
	{"US", "America/Denver", "en-US,en;q=0.9"},
	{"US", "America/Los_Angeles", "en-US,en;q=0.9"},
	{"US", "America/Phoenix", "en-US,en;q=0.9"},
	{"CA", "America/Toronto", "en-CA,en;q=0.9,en-US;q=0.8"},
	{"CA", "America/Vancouver", "en-CA,en;q=0.9,en-US;q=0.8"},
	{"GB", "Europe/London", "en-GB,en;q=0.9"},
	{"GB", "Europe/Dublin", "en-IE,en;q=0.9,en-GB;q=0.8"},
	{"DE", "Europe/Berlin", "de-DE,de;q=0.9,en-US;q=0.8,en;q=0.7"},
	{"DE", "Europe/Vienna", "de-AT,de;q=0.9,en-US;q=0.8,en;q=0.7"},
	{"DE", "Europe/Zurich", "de-CH,de;q=0.9,en-US;q=0.8,en;q=0.7"},
	{"NL", "Europe/Amsterdam", "nl-NL,nl;q=0.9,en-US;q=0.8,en;q=0.7"},
	{"FR", "Europe/Paris", "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7"},
	{"SG", "Asia/Singapore", "en-SG,en;q=0.9,en-US;q=0.8"},
	{"JP", "Asia/Tokyo", "ja-JP,ja;q=0.9,en-US;q=0.8,en;q=0.7"},
	{"AU", "Australia/Sydney", "en-AU,en;q=0.9,en-US;q=0.8"},
	{"BR", "America/Sao_Paulo", "pt-BR,pt;q=0.9,en-US;q=0.8,en;q=0.7"},
}

// Zone returns the timezone and Accept-Language a region would plausibly
// report, chosen stably from the seed so one account always sends the same
// pair. An unknown or empty region returns "" for both, because inventing a
// zone that contradicts the exit IP is worse than the boring value it replaces.
func Zone(seed, region string) (name, lang string) {
	region = strings.ToUpper(strings.TrimSpace(region))
	if region == "" {
		return "", ""
	}
	var pool []zone
	for _, z := range zones {
		if z.region == region {
			pool = append(pool, z)
		}
	}
	if len(pool) == 0 {
		return "", ""
	}
	// A different salt than For() uses, so the UA and the timezone are not
	// correlated in a way the bare seed could expose.
	z := pool[indexFor("zone\x00"+seed, len(pool))]
	return z.name, z.lang
}

// Regions lists the egress regions the zone table can describe, for callers
// that validate a configured region before a run.
func Regions() []string {
	seen := map[string]bool{}
	var out []string
	for _, z := range zones {
		if !seen[z.region] {
			seen[z.region] = true
			out = append(out, z.region)
		}
	}
	return out
}

// FetchKind is what the request is from the browser's point of view. Chrome
// derives Sec-Fetch-* from it, and the values differ per kind: a top-level
// navigation is site "none", mode "navigate", dest "document"; a same-origin
// XHR is site "same-origin", mode "cors", dest "empty". Sending one blanket
// value for every request is itself a tell.
type FetchKind int

const (
	// Navigate is a top-level document load (typing a URL, a form POST).
	Navigate FetchKind = iota
	// SameOriginXHR is fetch() from the site's own page.
	SameOriginXHR
	// SameOriginDocument is a navigation started from the site's own page.
	SameOriginDocument
)

// fetchHeaders renders the Sec-Fetch-* set Chrome attaches for one kind.
// These are injected by the network stack, never by page script, so their
// absence is a harder signal than a stale version number.
func fetchHeaders(k FetchKind) map[string]string {
	switch k {
	case SameOriginDocument:
		return map[string]string{
			"sec-fetch-site":   "same-origin",
			"sec-fetch-mode":   "navigate",
			"sec-fetch-dest":   "document",
			"sec-fetch-user":   "?1",
			"priority":         "u=0, i",
			"sec-ch-ua-mobile": "?0",
		}
	case SameOriginXHR:
		return map[string]string{
			"sec-fetch-site": "same-origin",
			"sec-fetch-mode": "cors",
			"sec-fetch-dest": "empty",
			"priority":       "u=1, i",
		}
	default: // Navigate
		return map[string]string{
			"sec-fetch-site":            "none",
			"sec-fetch-mode":            "navigate",
			"sec-fetch-dest":            "document",
			"sec-fetch-user":            "?1",
			"upgrade-insecure-requests": "1",
			"priority":                  "u=0, i",
		}
	}
}

// Header returns the request headers this identity presents. The returned map
// is a fresh copy each call so callers can add their own entries safely.
//
// sec-ch-ua-platform-version is deliberately omitted: it is a high-entropy
// hint Chrome sends only after the server opts in with Accept-CH, so sending
// it unconditionally is the opposite of what a real browser does.
func (i Identity) Header() map[string]string {
	return map[string]string{
		"User-Agent":         i.UserAgent,
		"sec-ch-ua":          BuildBrandList(i.Seed, i.Brand, i.BrandVersion),
		"sec-ch-ua-mobile":   i.Mobile,
		"sec-ch-ua-platform": i.Platform,
	}
}

// Headers returns the full header set for one request kind: the identity
// itself plus the Sec-Fetch-* headers a real browser would add for it.
func (i Identity) Headers(k FetchKind) map[string]string {
	h := i.Header()
	// Chrome does not send UA-CH hints on a cross-origin no-cors fetch, but it
	// does on both navigations and same-origin XHR, which is all this does.
	for name, value := range fetchHeaders(k) {
		h[name] = value
	}
	return h
}

// BuildBrandList renders a Sec-CH-UA brand list the way Chrome emits it: three
// entries, of which the GREASE entry ("Not)A;Brand" or a sibling) carries a
// random major version, and the order of the three is permuted per build.
//
// Chrome does not send a fixed order across users: the permutation is chosen
// per install, so a pool where every account sends the identical triple is
// itself a fingerprint. seed selects one stable permutation per account.
func BuildBrandList(seed, brand, version string) string {
	h := sha256.Sum256([]byte("ua\x00" + seed))
	// The GREASE brand and its version are both randomized by Chrome.
	greaseBrand := []string{"Not)A;Brand", "Not;A=Brand", "Not_A Brand"}[int(h[0])%3]
	greaseVer := []string{"8", "24", "99"}[int(h[1])%3]
	brands := []string{
		`"` + brand + `";v="` + version + `"`,
		`"Chromium";v="` + version + `"`,
		`"` + greaseBrand + `";v="` + greaseVer + `"`,
	}
	// Rotate by a per-seed offset: Chrome always keeps the GREASE entry in one
	// of the three positions, never appended last every time.
	shift := int(h[2]) % 3
	out := make([]string, 3)
	for i := range brands {
		out[(i+shift)%3] = brands[i]
	}
	return strings.Join(out, ", ")
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
		userAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "145",
		platform:        `"macOS"`,
		platformVersion: `"15.5.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "144",
		platform:        `"macOS"`,
		platformVersion: `"14.6.1"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "145",
		platform:        `"Windows"`,
		platformVersion: `"15.0.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/144.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "144",
		platform:        `"Windows"`,
		platformVersion: `"10.0.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36",
		brand:           "Google Chrome",
		version:         "145",
		platform:        `"Linux"`,
		platformVersion: `"6.8.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
	{
		userAgent:       "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36 Edg/145.0.0.0",
		brand:           "Microsoft Edge",
		version:         "145",
		platform:        `"macOS"`,
		platformVersion: `"15.5.0"`,
		acceptLanguage:  "en-US,en;q=0.9",
	},
}

// For returns the identity an account presents. An empty seed still yields a
// valid identity so a caller without a stable id is not forced into the Go
// default; callers that have an account id should pass it.
func For(seed string) Identity {
	return identityForSpot(seed, spots[indexFor(seed, len(spots))])
}

func identityForSpot(seed string, s spot) Identity {
	return Identity{
		UserAgent:       s.userAgent,
		Brand:           s.brand,
		BrandVersion:    s.version,
		Platform:        s.platform,
		PlatformVersion: s.platformVersion,
		Mobile:          "?0",
		AcceptLanguage:  s.acceptLanguage,
		Seed:            seed,
	}
}

// indexFor maps a seed to a table slot. crypto/sha256 rather than maphash keeps
// the choice stable across processes: two restarts must not change what an
// account looks like, or the upstream sees the same account switch identity.
func indexFor(seed string, n int) int {
	sum := sha256.Sum256([]byte(seed))
	return int(binary.BigEndian.Uint32(sum[:4]) % uint32(n))
}

// Len includes the frozen legacy table and the current named profiles.
func Len() int { return len(spots) + len(desktopProfiles) }

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
//
// It also sets Accept-Language. A browser always sends one, so leaving it out
// while sending a browser User-Agent is the same contradiction as a missing
// hint: the caller looks like a browser in one header and like a bare library
// in the next.
func Apply(h Setter, id Identity) {
	for k, v := range id.Header() {
		h.Set(k, v)
	}
	h.Set("Accept-Language", id.AcceptLanguage)
}

// ApplyKind is Apply plus the Sec-Fetch-* set a browser attaches for a request
// of this kind. Every outbound request that claims a browser identity should
// use it; Apply alone is only adequate where the caller cannot know the kind.
func ApplyKind(h Setter, id Identity, k FetchKind) {
	for name, value := range id.Headers(k) {
		h.Set(name, value)
	}
	h.Set("Accept-Language", id.AcceptLanguage)
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
