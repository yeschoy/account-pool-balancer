package perch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A CLI bundle stores the persona prompt as a template literal with escapes;
// the reader has to walk the literal properly, not regex it.
func TestPrefixFromFile(t *testing.T) {
	bundle := `var x,y;y="unrelated";L8t=` + "`" + `
You are Saffron \u2014 Perch's workspace operator.

Sharp, experienced, been around long enough to know what matters and what's noise.
You work with analysts, operators, and founders on financial analysis, audits,
document review, and whatever the workspace needs. You have opinions. You're direct.
You genuinely enjoy finding something interesting buried in a mess.

You're not an assistant. You're the operator who gets the work done.

` + "`" + `,z=1;`
	path := filepath.Join(t.TempDir(), "perch.mjs")
	if err := os.WriteFile(path, []byte(bundle), 0o600); err != nil {
		t.Fatal(err)
	}
	got := prefixFromFile(path)
	if !strings.Contains(got, "You are Saffron — Perch's workspace operator.") {
		t.Fatalf("prefix = %q", got)
	}
	if strings.Contains(got, `\u2014`) {
		t.Fatal("the escaped em dash was not decoded")
	}
	if len([]rune(got)) < GatePrefixMin {
		t.Fatalf("prefix too short: %d", len([]rune(got)))
	}
	if err := CheckGatePrefix(got); err != nil {
		t.Fatalf("check: %v", err)
	}
}

// A bundle without the anchor, a missing file, and a too-short literal all
// yield nothing rather than a misleading partial prefix.
func TestPrefixFromFileRejects(t *testing.T) {
	dir := t.TempDir()
	noAnchor := filepath.Join(dir, "a.mjs")
	_ = os.WriteFile(noAnchor, []byte("var s=`hello`;"), 0o600)
	short := filepath.Join(dir, "b.mjs")
	_ = os.WriteFile(short, []byte("s=`"+GatePrefixAnchor+" is here.`;"), 0o600)
	for _, p := range []string{noAnchor, short, filepath.Join(dir, "missing.mjs")} {
		if got := prefixFromFile(p); got != "" {
			t.Fatalf("%s: got %q, want empty", filepath.Base(p), got)
		}
	}
}

func TestGatePrefixEnvWins(t *testing.T) {
	long := GatePrefixAnchor + " — " + strings.Repeat("x", GatePrefixMin)
	t.Setenv(GatePrefixEnv, long)
	got, src := DeriveGatePrefix()
	if got != strings.TrimSpace(long) || !strings.HasPrefix(src, "env:") {
		t.Fatalf("got %q from %q", got[:20], src)
	}
}

func TestCheckGatePrefix(t *testing.T) {
	if err := CheckGatePrefix(""); err == nil {
		t.Fatal("an empty prefix must be refused")
	}
	if err := CheckGatePrefix("too short " + GatePrefixAnchor); err == nil {
		t.Fatal("a prefix under the gate length must be refused")
	}
	if err := CheckGatePrefix(strings.Repeat("x", GatePrefixMin)); err == nil {
		t.Fatal("a prefix without the anchor must be refused")
	}
	ok := GatePrefixAnchor + " — " + strings.Repeat("y", GatePrefixMin)
	if err := CheckGatePrefix(ok); err != nil {
		t.Fatalf("a good prefix was refused: %v", err)
	}
}

// The derived prefix, when a client happens to be installed, must satisfy the
// same check the runtime applies. Skipped where no client exists.
func TestDerivedPrefixIsUsable(t *testing.T) {
	p, src := DeriveGatePrefix()
	if p == "" {
		t.Skip("no Perch client installed on this machine")
	}
	if err := CheckGatePrefix(p); err != nil {
		t.Fatalf("%s derived an unusable prefix: %v", src, err)
	}
	t.Logf("derived %d chars from %s", len([]rune(p)), src)
}
