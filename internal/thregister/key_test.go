package thregister

import "testing"

// A site key is "thk_live_" plus a long token body; this fixture has the same
// shape and length. The bug this guards against returned only a leading slice
// of it, so every later API call 401'd while the account itself was healthy.
const realKey = "thk_live_TESTOKENTESTOKENTESTOKENTESTOKENTESTOKENTESTOKEN"

func TestLookupKeyJSONField(t *testing.T) {
	body := []byte(`{"key":"` + realKey + `","id":"k1"}`)
	got, err := LookupKey(body)
	if err != nil || got != realKey {
		t.Fatalf("want the full key, got %q (%v)", got, err)
	}
	if len(got) != len(realKey) || len(got) < MinAPIKeyLen {
		t.Fatalf("key length %d, want %d", len(got), len(realKey))
	}
}

func TestLookupKeyFromPageText(t *testing.T) {
	body := []byte(`<p>COPY YOUR NEW KEY NOW — WE WON'T SHOW IT AGAIN</p><code>` + realKey + `</code><button>Copy</button>`)
	got, err := LookupKey(body)
	if err != nil || got != realKey {
		t.Fatalf("want the full key from the page, got %q (%v)", got, err)
	}
}

func TestLookupKeyRejectsShortMatch(t *testing.T) {
	// The exact regression: a truncated key must fail loudly, not be used.
	if _, err := LookupKey([]byte(`{"key":"thk_live_aXspEWzMmfr6WWYX"}`)); err == nil {
		t.Fatal("a truncated key must be an error")
	}
	if _, err := LookupKey([]byte(`{"key":"thk_live_"}`)); err == nil {
		t.Fatal("an empty key must be an error")
	}
}

func TestLookupKeyIgnoresSurroundingPunctuation(t *testing.T) {
	body := []byte(`{"token":"` + realKey + `"}`)
	if got, err := LookupKey(body); err != nil || got != realKey {
		t.Fatalf("quoted value: got %q (%v)", got, err)
	}
	body = []byte(`<code>"` + realKey + `",</code>`)
	if got, err := LookupKey(body); err != nil || got != realKey {
		t.Fatalf("punctuated value: got %q (%v)", got, err)
	}
}
