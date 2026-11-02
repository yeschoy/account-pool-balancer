package perch

import (
	"strings"
	"testing"
	"time"
)

// The CLI's identity is small and deterministic. These tests pin the exact
// shapes from analysis.md §设备、指纹及遥测 so a refactor cannot quietly change
// what the pool claims to be.

func TestMachineIDLooksLikeAUUID(t *testing.T) {
	id := NewMachineID()
	if len(id) != 36 {
		t.Fatalf("machine id %q is not 36 characters", id)
	}
	if id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		t.Fatalf("machine id %q has the wrong dash positions", id)
	}
	// version 4, variant 10xx — the CLI mints a random UUID
	if id[14] != '4' {
		t.Fatalf("machine id %q is not version 4", id)
	}
	if !strings.ContainsRune("89ab", rune(id[19])) {
		t.Fatalf("machine id %q has the wrong RFC 4122 variant", id)
	}
	if other := NewMachineID(); other == id {
		t.Fatal("two machine ids are equal")
	}
}

func TestEnsureMachineIDKeepsAnExistingOne(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	first, changed := DeviceIdentity{}.EnsureMachineID(now)
	if !changed || first.MachineID == "" {
		t.Fatal("a fresh identity should mint a machine id")
	}
	if !first.CreatedAt.Equal(now) {
		t.Fatalf("createdAt = %v, want %v", first.CreatedAt, now)
	}
	// reading it back must return the same value: that is the whole point
	again, changed := first.EnsureMachineID(now.Add(time.Hour))
	if changed {
		t.Fatal("an identity with a machine id must not be re-minted")
	}
	if again.MachineID != first.MachineID {
		t.Fatalf("machine id changed: %q → %q", first.MachineID, again.MachineID)
	}
}

func TestMarkFirstPromptFiresOnce(t *testing.T) {
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	d := DeviceIdentity{MachineID: "x"}
	d, first := d.MarkFirstPrompt(now)
	if !first {
		t.Fatal("the first prompt should report first")
	}
	if _, first := d.MarkFirstPrompt(now.Add(time.Minute)); first {
		t.Fatal("the second prompt reported first again")
	}
}

func TestCwdThreadID(t *testing.T) {
	// "cli-cwd-" + 16 hex characters of SHA-256 over the resolved path.
	id := CwdThreadID("/Users/example/work")
	if !strings.HasPrefix(id, ThreadIDPrefix) {
		t.Fatalf("thread id %q lacks the CLI prefix", id)
	}
	if got := len(strings.TrimPrefix(id, ThreadIDPrefix)); got != 16 {
		t.Fatalf("thread id digest is %d characters, want 16", got)
	}
	if id != CwdThreadID("/Users/example/work") {
		t.Fatal("the same path must yield the same thread id")
	}
	// a trailing slash is the same directory once resolved
	if id != CwdThreadID("/Users/example/work/") {
		t.Fatal("a trailing slash changed the thread id")
	}
	if id == CwdThreadID("/Users/example/other") {
		t.Fatal("different paths yielded the same thread id")
	}
	// the raw path must not survive in the id
	if strings.Contains(id, "example") {
		t.Fatalf("thread id %q leaks the path", id)
	}
	if CwdThreadID("") != "" {
		t.Fatal("an empty cwd must yield no thread id")
	}
}

func TestLocaleAndUserAgent(t *testing.T) {
	cases := map[string]string{
		"en":      "en",
		"":        DefaultLocale,
		"zh-CN":   "zh-CN",
		"en US":   DefaultLocale,
		"en\r\nX": DefaultLocale,
	}
	for in, want := range cases {
		if got := Locale(in); got != want {
			t.Errorf("Locale(%q) = %q, want %q", in, got, want)
		}
	}
	if got := UserAgentFor("2.4.121"); got != "perchai-cli/2.4.121" {
		t.Fatalf("UserAgentFor = %q", got)
	}
	if got := UserAgentFor(""); got != "perchai-cli/unknown" {
		t.Fatalf("an empty version should name the CLI's own fallback, got %q", got)
	}
	if err := VerifyUserAgent("2.4.121"); err != nil {
		t.Fatalf("a plain version was refused: %v", err)
	}
	if err := VerifyUserAgent("2.4\r\nX-Evil: 1"); err == nil {
		t.Fatal("a header-injecting version was accepted")
	}
}

func TestModelCallDefaultsToTheCLIIdentity(t *testing.T) {
	var m ModelCall
	if m.UAFor() != UserAgent || m.LocaleFor() != DefaultLocale {
		t.Fatalf("defaults = %q / %q", m.UAFor(), m.LocaleFor())
	}
	m = ModelCall{UserAgent: "perchai-cli/9.9.9", Locale: "zh-CN"}
	if m.UAFor() != "perchai-cli/9.9.9" || m.LocaleFor() != "zh-CN" {
		t.Fatalf("per-call identity ignored: %q / %q", m.UAFor(), m.LocaleFor())
	}
}

func TestPromptOf(t *testing.T) {
	body := map[string]any{"messages": []any{
		map[string]any{"role": "system", "content": "be brief"},
		map[string]any{"role": "user", "content": "first"},
		map[string]any{"role": "assistant", "content": "ok"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "second"},
		}},
	}}
	p := PromptOf(body)
	for _, want := range []string{"system: be brief", "user: first", "assistant: ok", "user: second"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt %q lacks %q", p, want)
		}
	}
}
