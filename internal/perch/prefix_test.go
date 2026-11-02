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
	// Measured on a live account (2026-11-01): the gate reads the length and
	// the head of the first system message, not Perch's exact wording — 200
	// runes of any preamble pass, 195 are refused. So a prefix that is long
	// enough is usable even without Perch's own persona text.
	if err := CheckGatePrefix(strings.Repeat("x", GatePrefixMin)); err != nil {
		t.Fatalf("a long enough preamble must be accepted: %v", err)
	}
	ok := GatePrefixAnchor + " — " + strings.Repeat("y", GatePrefixMin)
	if err := CheckGatePrefix(ok); err != nil {
		t.Fatalf("a good prefix was refused: %v", err)
	}
	if err := CheckGatePrefix(strings.Repeat("x", GatePrefixMin-1)); err == nil {
		t.Fatal("195 runes is below the measured threshold")
	}
}

// The shipped default must satisfy the same check, or a fresh install could
// not serve the CLI surface at all.
func TestDefaultGatePrefixIsUsable(t *testing.T) {
	if err := CheckGatePrefix(DefaultGatePrefix); err != nil {
		t.Fatalf("the shipped default is unusable: %v", err)
	}
	text, src := ResolveGatePrefix("")
	if text == "" || src == "" {
		t.Fatalf("ResolveGatePrefix(empty) returned nothing")
	}
	if text, src = ResolveGatePrefix("configured preamble " + strings.Repeat("z", GatePrefixMin)); !strings.HasPrefix(text, "configured preamble") || src != "settings" {
		t.Fatalf("a configured prefix must win, got %q from %q", text[:20], src)
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
