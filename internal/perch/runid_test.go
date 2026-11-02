package perch

import (
	"strings"
	"testing"
	"time"
)

// Same conversation → same run id, across calls.
func TestRunForIsStablePerConversation(t *testing.T) {
	c := NewClient(nil)
	a := c.RunFor("conv-A")
	b := c.RunFor("conv-A")
	if a != b {
		t.Fatalf("the same conversation got two run ids: %s vs %s", a, b)
	}
	if !strings.HasPrefix(a, "cli-turn-") {
		t.Fatalf("run id = %q, want the client's cli-turn- shape", a)
	}
}

// Different conversations never share a run id.
func TestRunForIsolatesConversations(t *testing.T) {
	c := NewClient(nil)
	if c.RunFor("conv-A") == c.RunFor("conv-B") {
		t.Fatal("two conversations shared a run id")
	}
}

// No conversation → a fresh run id every time. Reusing one here would claim a
// prefix two unrelated requests do not share.
func TestRunForEmptyMintsFresh(t *testing.T) {
	c := NewClient(nil)
	a, b := c.RunFor(""), c.RunFor("")
	if a == b {
		t.Fatalf("an unnamed conversation must not reuse a run id: %s", a)
	}
	if c.RunStats() != 0 {
		t.Fatalf("an empty fingerprint must not be tracked, got %d", c.RunStats())
	}
}

// A quiet conversation is dropped after RunIdTTL, so two unrelated sessions do
// not inherit each other's prefix forever.
func TestRunForExpires(t *testing.T) {
	c := NewClient(nil)
	first := c.RunFor("conv-A")

	later := time.Now().Add(RunIdTTL + time.Minute)
	c.Now = func() time.Time { return later }

	if got := c.RunFor("conv-A"); got == first {
		t.Fatal("an expired conversation must get a new run id")
	}
}

// Reuse is refreshed on each use: a long conversation is not evicted mid-run.
func TestRunForRefreshesRecency(t *testing.T) {
	c := NewClient(nil)
	start := time.Now()
	c.Now = func() time.Time { return start }
	first := c.RunFor("conv-A")

	// Use it repeatedly, each time just under the TTL apart.
	cur := start
	for i := 0; i < 5; i++ {
		cur = cur.Add(RunIdTTL - time.Minute)
		c.Now = func() time.Time { return cur }
		if got := c.RunFor("conv-A"); got != first {
			t.Fatalf("step %d: run id changed while in active use", i)
		}
	}
}

// ForgetRun drops the mapping.
func TestForgetRun(t *testing.T) {
	c := NewClient(nil)
	first := c.RunFor("conv-A")
	c.ForgetRun("conv-A")
	if got := c.RunFor("conv-A"); got == first {
		t.Fatal("ForgetRun did not drop the mapping")
	}
}

// The derivation is deterministic for a given fingerprint, so two processes
// that share a fingerprint agree on the run id.
func TestConversationRunIDIsDeterministic(t *testing.T) {
	if ConversationRunID("x", 1) != ConversationRunID("x", 1) {
		t.Fatal("ConversationRunID is not deterministic")
	}
	if ConversationRunID("x", 1) == ConversationRunID("y", 1) {
		t.Fatal("different fingerprints produced the same run id")
	}
	if len(ConversationRunID("x", 1)) != len("cli-turn-")+12+1+6 {
		t.Fatalf("unexpected run id shape: %q", ConversationRunID("x", 1))
	}
}

// The whole point: two requests of one conversation, arriving as separate HTTP
// transactions, produce the same delta key so the second sends only the tail.
func TestDeltaHitsAcrossSeparateRequestsOfOneConversation(t *testing.T) {
	c := NewClient(nil)
	conv := "fingerprint-of-one-chat"

	// Request 1: 3 messages, whole body.
	run1 := c.RunFor(conv)
	env1 := envelopeFor(run1, 3, nil)
	dig := digestMessages(env1.Request.Messages)
	plan1 := c.PlanDelta("https://app/api/perch-turn", run1, env1)
	if plan1.IsDelta {
		t.Fatal("the first request must be whole")
	}
	// The server confirms it now holds 3 messages.
	plan1.Observe(200, map[string]string{
		DeltaHeader:      "1",
		HeldLengthHeader: "3",
		HeldDigestHeader: prefixDigest(dig, 3),
	})
	c.SettleDelta(plan1, true)

	// Request 2: SAME conversation, now 5 messages, arriving as a new request.
	run2 := c.RunFor(conv)
	if run2 != run1 {
		t.Fatal("the same conversation must reuse its run id")
	}
	env2 := envelopeFor(run2, 5, nil)
	plan2 := c.PlanDelta("https://app/api/perch-turn", run2, env2)
	if !plan2.IsDelta {
		t.Fatal("the second request of the same conversation must be a delta")
	}
	if plan2.base == nil || len(plan2.base.digests) != 3 {
		t.Fatalf("delta base = %+v, want a 3-message prefix", plan2.base)
	}
	body, err := plan2.Body(env2)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "messagesDelta") {
		t.Fatalf("body has no messagesDelta: %s", body)
	}
	if strings.Contains(string(body), `"messages":`) {
		t.Fatalf("delta body still carries messages: %s", body)
	}
}

// The counters split every call into delta or whole, and a confirmed prefix is
// what turns the next one into a delta — the numbers the console shows.
func TestDeltaCountersReport(t *testing.T) {
	c := NewClient(nil)
	conv := "conv-stats"

	// First request: whole, and the server confirms 3 messages.
	run1 := c.RunFor(conv)
	env1 := envelopeFor(run1, 3, nil)
	dig := digestMessages(env1.Request.Messages)
	plan1 := c.PlanDelta("ep", run1, env1)
	c.RecordSending(plan1, env1)
	plan1.Observe(200, map[string]string{
		DeltaHeader:      "1",
		HeldLengthHeader: "3",
		HeldDigestHeader: prefixDigest(dig, 3),
	})
	c.SettleDelta(plan1, true)

	// Second request: delta over 3 held messages, 2 new.
	run2 := c.RunFor(conv)
	env2 := envelopeFor(run2, 5, nil)
	plan2 := c.PlanDelta("ep", run2, env2)
	if !plan2.IsDelta {
		t.Fatal("expected a delta on the second request")
	}
	c.RecordSending(plan2, env2)
	plan2.Observe(200, map[string]string{
		DeltaHeader:      "1",
		HeldLengthHeader: "5",
		HeldDigestHeader: prefixDigest(digestMessages(env2.Request.Messages), 5),
	})
	c.SettleDelta(plan2, true)

	r := c.DeltaReport()
	if r.Whole != 1 || r.Delta != 1 {
		t.Fatalf("whole/delta = %d/%d, want 1/1", r.Whole, r.Delta)
	}
	if r.HitRate != 0.5 {
		t.Fatalf("hitRate = %v, want 0.5", r.HitRate)
	}
	if r.MessagesOmitted != 3 || r.MessagesSent != 2 {
		t.Fatalf("omitted/sent = %d/%d, want 3/2", r.MessagesOmitted, r.MessagesSent)
	}
	if r.BytesOmitted <= 0 || r.Confirmed != 2 {
		t.Fatalf("bytesOmitted=%d confirmed=%d", r.BytesOmitted, r.Confirmed)
	}
	if r.CachedKeys != 1 {
		t.Fatalf("cachedKeys = %d", r.CachedKeys)
	}

	// A refusal is counted and the cache entry dropped.
	env3 := envelopeFor(run2, 6, nil)
	plan3 := c.PlanDelta("ep", run2, env3)
	if plan3.IsDelta {
		// The 5-message prefix was confirmed, so this is a delta; force the
		// refusal path the way the server would.
		plan3.Observe(409, map[string]string{})
		c.RecordRefused()
		c.SettleDelta(plan3, false)
	}
	if got := c.DeltaReport().Refused; got != 1 {
		t.Fatalf("refused = %d, want 1", got)
	}

	// Rebuild a snapshot so the reset can be shown to keep the cache.
	env4 := envelopeFor(run2, 7, nil)
	plan4 := c.PlanDelta("ep", run2, env4)
	plan4.Observe(200, map[string]string{
		DeltaHeader:      "1",
		HeldLengthHeader: "7",
		HeldDigestHeader: prefixDigest(digestMessages(env4.Request.Messages), 7),
	})
	c.SettleDelta(plan4, true)
	keysBefore, _ := c.DeltaStats()

	c.ResetDeltaCounters()
	if r2 := c.DeltaReport(); r2.Delta != 0 || r2.Whole != 0 || r2.HitRate != 0 {
		t.Fatalf("counters not reset: %+v", r2)
	}
	if k, _ := c.DeltaStats(); k != keysBefore || k == 0 {
		t.Fatalf("ResetDeltaCounters must keep the cache: %d -> %d", keysBefore, k)
	}
}
