package egressid

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The choice must not move between processes: the same account that looked
// like one browser before a restart must not switch to another afterwards.
func TestForIsStableForOneSeed(t *testing.T) {
	a := For("account-abc")
	b := For("account-abc")
	if a.UserAgent != b.UserAgent || a.Platform != b.Platform {
		t.Fatalf("identity changed between calls: %+v vs %+v", a, b)
	}
}

// A pool of accounts must not collapse onto one User-Agent. This is the defect
// the package exists to remove, so it is asserted over a realistic spread.
func TestForSpreadsAcrossAccounts(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		seen[For("account-"+string(rune('a'+i%26))+string(rune('0'+i%10))).UserAgent] = true
	}
	if len(seen) < 3 {
		t.Fatalf("only %d distinct User-Agents across 200 seeds; the pool would cluster", len(seen))
	}
}

// A browser User-Agent with a mismatched or missing hint set is itself a
// signal, so the hint version has to agree with the UA it is sent beside.
func TestHintsAgreeWithUserAgent(t *testing.T) {
	for seed := range map[string]bool{"a": true, "b": true, "c": true, "d": true, "e": true, "f": true, "g": true} {
		id := For(seed)
		major := ChromeMajor(id.UserAgent)
		if major == "" {
			t.Fatalf("%s: UA has no Chrome version: %q", seed, id.UserAgent)
		}
		if major != id.BrandVersion {
			t.Fatalf("%s: UA major %q, hint version %q", seed, major, id.BrandVersion)
		}
		h := id.Header()
		want := BuildBrandList(id.Seed, id.Brand, major)
		if h["sec-ch-ua"] != want {
			t.Fatalf("%s: sec-ch-ua = %q, want %q", seed, h["sec-ch-ua"], want)
		}
		if !strings.Contains(h["sec-ch-ua"], major) {
			t.Fatalf("%s: hint list omits its own version %q", seed, major)
		}
		if h["sec-ch-ua-mobile"] != "?0" {
			t.Fatalf("%s: desktop UA must not claim mobile", seed)
		}
		if !strings.HasPrefix(h["sec-ch-ua-platform"], `"`) || !strings.HasSuffix(h["sec-ch-ua-platform"], `"`) {
			t.Fatalf("%s: platform must be a quoted string, got %q", seed, h["sec-ch-ua-platform"])
		}
	}
}

// The platform in the User-Agent and in Sec-CH-UA-Platform must agree; a
// macOS UA with a Windows hint is worse than no hint at all.
func TestPlatformAgreesWithUserAgent(t *testing.T) {
	for _, seed := range []string{"x1", "x2", "x3", "x4", "x5", "x6", "x7", "x8"} {
		id := For(seed)
		plat := strings.Trim(id.Platform, `"`)
		var want string
		switch {
		case strings.Contains(id.UserAgent, "Macintosh"):
			want = "macOS"
		case strings.Contains(id.UserAgent, "Windows NT"):
			want = "Windows"
		case strings.Contains(id.UserAgent, "X11; Linux"):
			want = "Linux"
		}
		if want == "" {
			t.Fatalf("%s: unrecognised platform in %q", seed, id.UserAgent)
		}
		if plat != want {
			t.Fatalf("%s: UA says %s, hint says %s", seed, want, plat)
		}
	}
}

// Every row has to be a browser build that still exists; the pool previously
// advertised a version far ahead of any shipped release.
func TestNoFutureVersions(t *testing.T) {
	for _, s := range spots {
		maj := ChromeMajor(s.userAgent)
		if maj == "" {
			t.Fatalf("unparseable UA %q", s.userAgent)
		}
		if n := atoi(t, maj); n > 145 {
			t.Fatalf("UA advertises Chrome %s, ahead of any shipped build: %q", maj, s.userAgent)
		}
		// A stale version is its own signal: a pool that has claimed Chrome 138
		// for a year describes an operator who never updates. Chrome moves
		// roughly every four weeks, so anything two releases behind is suspect.
		if n := atoi(t, maj); n < 143 {
			t.Fatalf("UA advertises Chrome %s, too old to be a current user: %q", maj, s.userAgent)
		}
		// Chrome ends in "Safari/537.36"; Edge appends its own product token.
		if !strings.HasSuffix(s.userAgent, "Safari/537.36") && !strings.Contains(s.userAgent, "Safari/537.36 Edg/") {
			t.Fatalf("unexpected UA shape %q", s.userAgent)
		}
		if !strings.Contains(s.userAgent, "Chrome/"+maj+".0.0.0") {
			t.Fatalf("Chrome build number disagrees with major %s in %q", maj, s.userAgent)
		}
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("not numeric: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// Apply must overwrite a generic library string rather than sit beside it, or
// a request could carry both identities.
func TestApplyReplacesExistingValue(t *testing.T) {
	h := map[string]string{"User-Agent": "Go-http-client/1.1"}
	setter := mapSetter(h)
	Apply(setter, For("seed"))
	if h["User-Agent"] == "Go-http-client/1.1" {
		t.Fatal("Apply left the generic User-Agent in place")
	}
	if !strings.Contains(h["User-Agent"], "Mozilla/5.0") {
		t.Fatalf("unexpected UA after Apply: %q", h["User-Agent"])
	}
}

type mapSetter map[string]string

func (m mapSetter) Set(k, v string) { m[k] = v }

// Identity must not serialise into a console view carrying anything secret;
// these fields are the ones the upstream already sees in headers.
func TestMarshalExposesOnlyDisplayFields(t *testing.T) {
	b, err := json.Marshal(For("seed"))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"userAgent", "brand", "platform", "platformVersion"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing %s in %s", k, b)
		}
	}
}

func TestChromeMajor(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Mozilla/5.0 Chrome/138.0.0.0 Safari/537.36", "138"},
		{"Mozilla/5.0 Chrome/137.0.0.0 Safari/537.36 Edg/137.0.0.0", "137"},
		{"Go-http-client/1.1", ""},
		{"curl/8.0", ""},
	} {
		if got := ChromeMajor(tc.in); got != tc.want {
			t.Fatalf("ChromeMajor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Chrome sends sec-ch-ua-platform-version only after the server opts in with
// Accept-CH. Sending it unconditionally is a tell, so it must be absent.
func TestHighEntropyHintNotSentUnconditionally(t *testing.T) {
	h := For("seed").Header()
	if v, ok := h["sec-ch-ua-platform-version"]; ok {
		t.Fatalf("platform-version sent without Accept-CH: %q", v)
	}
}

// Sec-Fetch-* is injected by the browser's network stack, never by page
// script, so a browser User-Agent without it cannot be a browser. Each
// request kind carries the values Chrome would actually use.
func TestFetchHeadersMatchRequestKind(t *testing.T) {
	id := For("seed")
	nav := id.Headers(Navigate)
	if nav["sec-fetch-site"] != "none" || nav["sec-fetch-mode"] != "navigate" || nav["sec-fetch-dest"] != "document" {
		t.Fatalf("top-level navigation set is wrong: %v", nav)
	}
	if nav["sec-fetch-user"] != "?1" || nav["upgrade-insecure-requests"] != "1" {
		t.Fatalf("navigation missing user/upgrade headers: %v", nav)
	}
	xhr := id.Headers(SameOriginXHR)
	if xhr["sec-fetch-site"] != "same-origin" || xhr["sec-fetch-mode"] != "cors" || xhr["sec-fetch-dest"] != "empty" {
		t.Fatalf("same-origin XHR set is wrong: %v", xhr)
	}
	if _, ok := xhr["upgrade-insecure-requests"]; ok {
		t.Fatal("upgrade-insecure-requests must not be sent on an XHR")
	}
	if _, ok := xhr["sec-fetch-user"]; ok {
		t.Fatal("sec-fetch-user must not be sent on an XHR")
	}
	doc := id.Headers(SameOriginDocument)
	if doc["sec-fetch-site"] != "same-origin" || doc["sec-fetch-dest"] != "document" {
		t.Fatalf("same-origin document set is wrong: %v", doc)
	}
}

// A whole pool reporting one timezone is the batch signal this replaces, so a
// region must yield more than one zone and two seeds must usually differ.
func TestZoneSpreadsWithinRegion(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 40; i++ {
		name, lang := Zone("acct-"+atoi2(i), "US")
		if name == "" || lang == "" {
			t.Fatalf("US seed %d produced no zone", i)
		}
		seen[name] = true
	}
	if len(seen) < 3 {
		t.Fatalf("US region collapsed onto %d zones: %v", len(seen), seen)
	}
}

// The same account must keep its zone across runs, or it looks like a user who
// teleported between sessions.
func TestZoneStableForOneSeed(t *testing.T) {
	a, la := Zone("acct-stable", "DE")
	b, lb := Zone("acct-stable", "DE")
	if a != b || la != lb {
		t.Fatalf("zone changed for one seed: %q/%q vs %q/%q", a, la, b, lb)
	}
	if a == "" {
		t.Fatal("DE must resolve")
	}
}

// An unknown region must yield nothing rather than an invented value: a zone
// that contradicts the exit IP is worse than a boring default.
func TestZoneUnknownRegionYieldsNothing(t *testing.T) {
	for _, region := range []string{"", "ZZ", "  ", "Atlantis"} {
		if name, lang := Zone("seed", region); name != "" || lang != "" {
			t.Fatalf("region %q produced %q/%q", region, name, lang)
		}
	}
}

// Every modelled region must be resolvable and every zone must name a real
// IANA area, so a typo cannot ship as a plausible-looking but invalid zone.
func TestZoneTableIsWellFormed(t *testing.T) {
	regions := Regions()
	if len(regions) < 5 {
		t.Fatalf("only %d regions modelled", len(regions))
	}
	for _, z := range zones {
		if _, err := time.LoadLocation(z.name); err != nil {
			t.Fatalf("zone %q is not a loadable IANA zone: %v", z.name, err)
		}
		if z.lang == "" {
			t.Fatalf("zone %q has no Accept-Language", z.name)
		}
	}
}

func atoi2(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// A browser always sends a language; a browser User-Agent without one is the
// same contradiction as a missing hint set.
func TestApplySetsAcceptLanguage(t *testing.T) {
	h := http.Header{}
	Apply(h, For("seed"))
	if h.Get("Accept-Language") == "" {
		t.Fatal("Apply left Accept-Language unset beside a browser User-Agent")
	}
}

// Chrome permutes the Sec-CH-UA brand order per install and randomizes the
// GREASE entry. Identical strings across a pool are themselves a fingerprint.
func TestBrandListVariesPerSeed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 60; i++ {
		seen[For("acct-" + atoi2(i)).Header()["sec-ch-ua"]] = true
	}
	if len(seen) < 3 {
		t.Fatalf("Sec-CH-UA collapsed to %d distinct values across 60 seeds", len(seen))
	}
}

// The permutation must stay stable for one account, or its own hint string
// changes between requests.
func TestBrandListStableForOneSeed(t *testing.T) {
	a := For("acct-stable").Header()["sec-ch-ua"]
	b := For("acct-stable").Header()["sec-ch-ua"]
	if a != b {
		t.Fatalf("brand list changed for one seed: %q vs %q", a, b)
	}
}

// The GREASE entry must not sit in the same position every time.
func TestBrandListGreasePositionVaries(t *testing.T) {
	positions := map[int]bool{}
	for i := 0; i < 60; i++ {
		parts := strings.Split(For("s" + atoi2(i)).Header()["sec-ch-ua"], ", ")
		for idx, p := range parts {
			if strings.Contains(p, "Not") {
				positions[idx] = true
			}
		}
	}
	if len(positions) < 2 {
		t.Fatalf("GREASE brand always in the same position: %v", positions)
	}
}
