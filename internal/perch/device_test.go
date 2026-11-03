package perch

import (
	"fmt"
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

// Two accounts served from one process must not present the same thread id.
// The published CLI derives it from the working directory alone, so an
// unscoped pool would put every identity on one machine id.
func TestCwdThreadIDIsPerAccount(t *testing.T) {
	dir := "/Users/example/work"
	a := CwdThreadIDFor(dir, "account-a")
	b := CwdThreadIDFor(dir, "account-b")
	if a == b {
		t.Fatal("two accounts share one thread id")
	}
	if !strings.HasPrefix(a, ThreadIDPrefix) || len(strings.TrimPrefix(a, ThreadIDPrefix)) != 16 {
		t.Fatalf("scoped thread id %q does not have the CLI shape", a)
	}
	// Stable per account across calls, or the id is useless as a thread.
	if a != CwdThreadIDFor(dir, "account-a") {
		t.Fatal("a scoped thread id must be stable for the same account")
	}
	// An empty account keeps the published, unscoped value.
	if CwdThreadIDFor(dir, "") != CwdThreadID(dir) {
		t.Fatal("an empty account must keep the published thread id")
	}
	// A different directory is still a different thread for the same account.
	if CwdThreadIDFor(dir, "account-a") == CwdThreadIDFor("/Users/example/other", "account-a") {
		t.Fatal("the directory stopped affecting the thread id")
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

// A conversation must keep one thread id: it stands for a project directory,
// and a directory does not change name between requests.
func TestThreadSlotIsStablePerConversation(t *testing.T) {
	a := ThreadSlot("conv-alpha", ThreadPoolSize)
	if a == "" {
		t.Fatal("a named conversation got no slot")
	}
	if a != ThreadSlot("conv-alpha", ThreadPoolSize) {
		t.Fatal("the same conversation moved slots")
	}
	if a == ThreadSlot("conv-beta", ThreadPoolSize) {
		t.Fatal("two conversations landed on one slot")
	}
	// No conversation, or no pool: no slot, so the caller keeps one thread.
	if ThreadSlot("", ThreadPoolSize) != "" || ThreadSlot("   ", ThreadPoolSize) != "" {
		t.Fatal("an empty conversation produced a slot")
	}
	if ThreadSlot("conv-alpha", 0) != "" {
		t.Fatal("a zero-size pool produced a slot")
	}
}

// The pool is what makes one account look like a developer with several
// projects: many conversations, a bounded set of thread ids, all of them used.
func TestThreadPoolSpreadsConversationsAcrossSlots(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 400; i++ {
		seen[ThreadSlot(fmt.Sprintf("conv-%d", i), ThreadPoolSize)] = true
	}
	if len(seen) != ThreadPoolSize {
		t.Fatalf("%d conversations covered %d slots, want all %d",
			400, len(seen), ThreadPoolSize)
	}
	// Slots must stay inside the pool: a slot name above size would mean the
	// modulo was skipped and the account could grow unbounded.
	for slot := range seen {
		var n int
		if _, err := fmt.Sscanf(slot, "p%d", &n); err != nil {
			t.Fatalf("slot %q is not of the form p<n>", slot)
		}
		if n < 0 || n >= ThreadPoolSize {
			t.Fatalf("slot %q is outside the pool", slot)
		}
	}
}

// Every slot yields a distinct thread id, and the ids stay per account.
func TestThreadPoolIdsAreDistinctAndAccountScoped(t *testing.T) {
	dir := "/data"
	seen := map[string]bool{}
	for n := 0; n < ThreadPoolSize; n++ {
		id := CwdThreadIDForSlot(dir, "account-a", fmt.Sprintf("p%d", n))
		if !strings.HasPrefix(id, ThreadIDPrefix) {
			t.Fatalf("slot p%d produced %q without the CLI prefix", n, id)
		}
		if seen[id] {
			t.Fatalf("slot p%d repeated the thread id %s", n, id)
		}
		seen[id] = true
	}
	// Another account on the same slot is a different thread id.
	if CwdThreadIDForSlot(dir, "account-a", "p0") == CwdThreadIDForSlot(dir, "account-b", "p0") {
		t.Fatal("two accounts share a thread id on the same slot")
	}
	// An empty slot keeps the plain account-scoped id.
	if CwdThreadIDForSlot(dir, "account-a", "") != CwdThreadIDFor(dir, "account-a") {
		t.Fatal("an empty slot changed the plain thread id")
	}
}
