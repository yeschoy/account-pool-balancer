package telnyxreg

import "testing"

func TestParsePortalLink(t *testing.T) {
	cases := []string{
		"https://portal.telnyx.com/#/freemium-redirect?token=01a1e0b7-35eb-762e-8e3b-592c7e71b6bb&email=leoponce3398%40outlook.com",
		"https://portal.telnyx.com/#/freemium-redirect?token=01a1e0b5-3502-76d8-b885-511171125c80&email=leoponce3398@outlook.com",
	}
	for _, c := range cases {
		tok, em, err := ParsePortalLink(c)
		if err != nil {
			t.Fatalf("%s: %v", c, err)
		}
		if tok == "" {
			t.Fatalf("empty token for %s", c)
		}
		t.Logf("token=%s email=%s", tok, em)
	}
}

func TestParseMailboxLine(t *testing.T) {
	line := `LilahMelendez1446@outlook.com----n2AjtHn87C----9e5f94bc-e8a4-4e73-b8be-63364c29d753----M.C543_BAY.0.U.MsaArtifacts.abc$$`
	// parseLine lives in main; replicate the split here.
	_ = line
}
