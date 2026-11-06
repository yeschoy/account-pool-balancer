package egressid

import "strconv"

// Named profiles are immutable: persist the name, not a modulo-table index.
// Updating the available pool must not change a saved session's identity.
// Chromium/Edge reduce desktop UA versions to major.0.0.0; inventing patch
// numbers or macOS/CPU tokens would make these less representative, not more.
// References: https://www.chromium.org/updates/ua-reduction/
// https://learn.microsoft.com/en-us/deployedge/microsoft-edge-relnote-stable-channel
var desktopProfiles = buildDesktopProfiles()

type desktopProfile struct {
	name string
	spot spot
}

func buildDesktopProfiles() []desktopProfile {
	platforms := []struct{ name, token, platform, version string }{
		{"windows", "Windows NT 10.0; Win64; x64", `"Windows"`, `"19.0.0"`},
		{"macos", "Macintosh; Intel Mac OS X 10_15_7", `"macOS"`, `"15.7.0"`},
		{"linux", "X11; Linux x86_64", `"Linux"`, `"6.8.0"`},
	}
	var out []desktopProfile
	// Published desktop releases as of 2026-11. Keep this cohort immutable;
	// future releases add new named profiles instead of reinterpreting names.
	for major := 154; major <= 157; major++ {
		version := strconv.Itoa(major)
		for _, p := range platforms {
			for _, browser := range []string{"chrome", "edge"} {
				ua := "Mozilla/5.0 (" + p.token + ") AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + version + ".0.0.0 Safari/537.36"
				brand := "Google Chrome"
				if browser == "edge" {
					ua += " Edg/" + version + ".0.0.0"
					brand = "Microsoft Edge"
				}
				out = append(out, desktopProfile{
					name: browser + "-" + version + "-" + p.name,
					spot: spot{userAgent: ua, brand: brand, version: version,
						platform: p.platform, platformVersion: p.version, acceptLanguage: "en-US,en;q=0.9"},
				})
			}
		}
	}
	return out
}

// PickProfile spreads cryptographically random account IDs across the current
// cohort. The caller persists the returned name once, before any site request.
// This is per-account selection, never per-request or failure-driven rotation.
func PickProfile(seed string) string {
	return desktopProfiles[indexFor("desktop-profile\x00"+seed, len(desktopProfiles))].name
}

// ForProfile restores a saved profile. Empty/unknown names retain the frozen
// legacy mapping so merely upgrading cannot relabel existing sessions.
func ForProfile(seed, profile string) Identity {
	for _, p := range desktopProfiles {
		if p.name == profile {
			return identityForSpot(seed, p.spot)
		}
	}
	return For(seed)
}
