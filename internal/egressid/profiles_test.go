package egressid

import (
	"fmt"
	"strings"
	"testing"
)

func TestNamedDesktopProfilesAreDistinctAndCoherent(t *testing.T) {
	if Len() != 30 || len(desktopProfiles) != 24 {
		t.Fatalf("want 24 current + 6 frozen legacy profiles, got %d", Len())
	}
	names, uas := map[string]bool{}, map[string]bool{}
	for _, p := range desktopProfiles {
		id := ForProfile("fixed-account", p.name)
		if names[p.name] || uas[id.UserAgent] {
			t.Fatal("duplicate profile or UA", p.name)
		}
		names[p.name], uas[id.UserAgent] = true, true
		major := ChromeMajor(id.UserAgent)
		if major != id.BrandVersion || atoi(t, major) < 154 || atoi(t, major) > 157 {
			t.Fatal("unpublished or inconsistent version", p.name)
		}
		if id.Mobile != "?0" || !strings.Contains(id.UserAgent, "Chrome/"+major+".0.0.0") {
			t.Fatal("must use the documented reduced desktop shape", p.name)
		}
		platform := strings.Trim(id.Platform, `"`)
		if (platform == "macOS") != strings.Contains(id.UserAgent, "Macintosh") ||
			(platform == "Windows") != strings.Contains(id.UserAgent, "Windows NT") ||
			(platform == "Linux") != strings.Contains(id.UserAgent, "X11; Linux") {
			t.Fatal("platform mismatch", p.name)
		}
		isEdge := strings.HasPrefix(p.name, "edge-")
		if isEdge != strings.Contains(id.UserAgent, " Edg/"+major+".0.0.0") || isEdge != (id.Brand == "Microsoft Edge") {
			t.Fatal("Edge brand and token disagree", p.name)
		}
		h := id.Headers(SameOriginXHR)
		if !strings.Contains(h["sec-ch-ua"], `"`+id.Brand+`";v="`+major+`"`) || h["sec-fetch-site"] != "same-origin" || h["sec-ch-ua-platform-version"] != "" {
			t.Fatal("hint shape mismatch or unnegotiated high entropy hint", p.name)
		}
	}
}

func TestNamedProfileSelectionStableAndCoversWholeCohort(t *testing.T) {
	seen := map[string]bool{}
	for i := range 3000 {
		seed := fmt.Sprintf("random-account-%d", i)
		profile := PickProfile(seed)
		seen[profile] = true
		a, b := ForProfile(seed, profile), ForProfile(seed, profile)
		if a != b || PickProfile(seed) != profile {
			t.Fatal("profile changed within one account")
		}
	}
	if len(seen) != 24 {
		t.Fatalf("cohort collapsed to %d profiles", len(seen))
	}
	// Changing the current list cannot relabel any legacy seed.
	for _, seed := range []string{"", "existing-account", "github:legacy"} {
		if ForProfile(seed, "") != For(seed) || ForProfile(seed, "unknown-future-name") != For(seed) {
			t.Fatal("legacy mapping changed", seed)
		}
	}
}
