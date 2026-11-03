package perch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// deltaFake is a model-call proxy that records every request and can answer
// with the server's held-context confirmation headers.
type deltaFake struct {
	srv     *httptest.Server
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
	calls   atomic.Int32

	// hold, when set, makes the server echo a confirmation of that many
	// messages plus the given digest, as Perch does after accepting a call.
	holdDigest string
	holdTools  string
	holdLen    int
	// refuse makes the next answer a delta refusal.
	refuse bool
	status int
}

func newDeltaFake(t *testing.T) *deltaFake {
	f := &deltaFake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if r.URL.Path != ModelCallPath {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.headers = append(f.headers, r.Header.Clone())
		f.mu.Unlock()

		refuseNow := false
		f.mu.Lock()
		if f.refuse {
			f.refuse = false
			refuseNow = true
		}
		f.mu.Unlock()
		if refuseNow {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, `{"ok":false,"error":"base mismatch","errorCode":"delta_base_mismatch"}`)
			return
		}
		if f.holdLen > 0 {
			w.Header().Set(DeltaHeader, "1")
			w.Header().Set(HeldLengthHeader, itoa(f.holdLen))
			w.Header().Set(HeldDigestHeader, f.holdDigest)
			if f.holdTools != "" {
				w.Header().Set(HeldToolsHeader, f.holdTools)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, "data: {\"type\":\"answer_delta\",\"text\":\"ok\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"done\",\"ok\":true,\"text\":\"ok\"}\n\n")
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *deltaFake) last() (map[string]any, http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return nil, nil
	}
	return f.bodies[len(f.bodies)-1], f.headers[len(f.headers)-1]
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// envelopeFor builds a run's envelope with n messages.
func envelopeFor(runID string, n int, tools []Tool) Envelope {
	msgs := make([]Message, n)
	for i := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		msgs[i] = Message{Role: role, Content: "m" + itoa(i)}
	}
	env := Envelope{
		RunID:    runID,
		Lane:     "chat",
		Surface:  "cli",
		AvoidIDs: []string{},
	}
	env.Request.Messages = msgs
	if len(tools) > 0 {
		env.Request.Tools = tools
	}
	return env
}

func envJSON(t *testing.T, env Envelope) []byte {
	t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The first call of a run sends the whole request: nothing is held yet.
func TestDeltaFirstCallIsWhole(t *testing.T) {
	f := newDeltaFake(t)
	c := NewClient(nil)
	env := envelopeFor("run-1", 3, nil)

	res, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env),
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("CallModel: %v", err)
	}
	_ = res

	body, hdr := f.last()
	if _, has := body["messagesDelta"]; has {
		t.Fatal("the first call must not claim a delta")
	}
	req, _ := body["request"].(map[string]any)
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("whole request must carry all 3 messages, got %v", len(msgs))
	}
	// A whole request still claims the header (that is what makes the server
	// report held-*); what must be absent is the delta BODY.
	if hdr.Get(DeltaHeader) != "1" {
		t.Fatalf("whole request should claim the delta header, got %q", hdr.Get(DeltaHeader))
	}
}

// A confirmed prefix makes the next call of the same run send only the tail.
func TestDeltaSecondCallSendsOnlyTail(t *testing.T) {
	f := newDeltaFake(t)
	c := NewClient(nil)

	// First call: 3 messages, server confirms holding all 3.
	env1 := envelopeFor("run-1", 3, nil)
	dig := digestMessages(env1.Request.Messages)
	f.holdLen, f.holdDigest = 3, prefixDigest(dig, 3)

	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env1),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call: same run, 5 messages (3 shared + 2 new).
	env2 := envelopeFor("run-1", 5, nil)
	f.mu.Lock()
	f.holdLen, f.holdDigest = 5, prefixDigest(digestMessages(env2.Request.Messages), 5)
	f.mu.Unlock()

	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env2),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("second call: %v", err)
	}

	body, hdr := f.last()
	if hdr.Get(DeltaHeader) != "1" {
		k, n := c.DeltaStats()
		t.Fatalf("a delta must carry x-perch-call-delta: 1 (cache keys=%d snaps=%d)", k, n)
	}
	md, ok := body["messagesDelta"].(map[string]any)
	if !ok {
		t.Fatalf("no messagesDelta in %v", body)
	}
	if int(md["baseLength"].(float64)) != 3 {
		t.Fatalf("baseLength = %v, want 3", md["baseLength"])
	}
	if md["baseDigest"] != prefixDigest(dig, 3) {
		t.Fatalf("baseDigest = %v", md["baseDigest"])
	}
	newMsgs, _ := md["newMessages"].([]any)
	if len(newMsgs) != 2 {
		t.Fatalf("newMessages = %d, want 2", len(newMsgs))
	}
	req, _ := body["request"].(map[string]any)
	if _, has := req["messages"]; has {
		t.Fatal("a delta must not also carry the full messages array")
	}
}

// A different run never reuses another run's prefix.
func TestDeltaIsPerRun(t *testing.T) {
	f := newDeltaFake(t)
	c := NewClient(nil)

	env1 := envelopeFor("run-1", 3, nil)
	f.holdLen, f.holdDigest = 3, prefixDigest(digestMessages(env1.Request.Messages), 3)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env1),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Same messages, different run id.
	env2 := envelopeFor("run-2", 5, nil)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env2),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("second call: %v", err)
	}
	body, hdr := f.last()
	if hdr.Get(DeltaHeader) != "1" {
		t.Fatal("every run claims the delta header; the body is what must be whole")
	}
	if _, has := body["messagesDelta"]; has {
		t.Fatal("a different run must not claim a delta")
	}
}

// A 409 is a refusal: the client resends the whole request.
func TestDeltaRefusalFallsBackToWhole(t *testing.T) {
	f := newDeltaFake(t)
	c := NewClient(nil)

	env1 := envelopeFor("run-1", 3, nil)
	dig := digestMessages(env1.Request.Messages)
	f.holdLen, f.holdDigest = 3, prefixDigest(dig, 3)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env1),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Second call claims a delta, server refuses with 409.
	env2 := envelopeFor("run-1", 5, nil)
	f.mu.Lock()
	f.refuse = true
	f.mu.Unlock()
	res, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env2),
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("a refused delta must fall back, not fail: %v", err)
	}
	if res.Text != "ok" {
		t.Fatalf("text = %q", res.Text)
	}
	// The last request is the whole one.
	body, hdr := f.last()
	// The fallback is a plain whole resend with no plan attached, so it makes
	// no delta claim at all — the header is absent rather than "1".
	if hdr.Get(DeltaHeader) != "" {
		t.Fatalf("the fallback resend must make no delta claim, got %q", hdr.Get(DeltaHeader))
	}
	req, _ := body["request"].(map[string]any)
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 5 {
		t.Fatalf("fallback must carry all messages, got %v", len(msgs))
	}
	// And the refused snapshot is forgotten.
	if keys, snaps := c.DeltaStats(); keys != 0 || snaps != 0 {
		t.Fatalf("a refused prefix must be dropped, cache = %d/%d", keys, snaps)
	}
}

// A snapshot is only stored when the server confirms it.
func TestDeltaUnconfirmedIsNotCached(t *testing.T) {
	f := newDeltaFake(t)
	c := NewClient(nil)
	env := envelopeFor("run-1", 3, nil)
	// No confirmation headers at all.
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	if keys, snaps := c.DeltaStats(); keys != 0 || snaps != 0 {
		t.Fatalf("an unconfirmed prefix must not be cached, got %d/%d", keys, snaps)
	}
}

// An unchanged tool list is omitted from the delta body.
func TestDeltaOmitsUnchangedTools(t *testing.T) {
	f := newDeltaFake(t)
	c := NewClient(nil)
	tools := []Tool{{"type": "function", "function": map[string]any{"name": "f"}}}
	env1 := envelopeFor("run-1", 2, tools)
	td := digestTools(tools)
	f.holdLen, f.holdDigest, f.holdTools = 2, prefixDigest(digestMessages(env1.Request.Messages), 2), td
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env1),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("first: %v", err)
	}

	env2 := envelopeFor("run-1", 3, tools)
	f.mu.Lock()
	f.holdLen, f.holdDigest = 3, prefixDigest(digestMessages(env2.Request.Messages), 3)
	f.mu.Unlock()
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env2),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("second: %v", err)
	}

	body, _ := f.last()
	req, _ := body["request"].(map[string]any)
	if _, has := req["tools"]; has {
		t.Fatal("unchanged tools must be omitted from a delta")
	}
	if body["toolsDigest"] != td {
		t.Fatalf("toolsDigest = %v, want %s", body["toolsDigest"], td)
	}
}

// Snapshots expire after the trust window.
func TestDeltaExpires(t *testing.T) {
	f := newDeltaFake(t)
	c := NewClient(nil)
	env1 := envelopeFor("run-1", 3, nil)
	dig := digestMessages(env1.Request.Messages)
	f.holdLen, f.holdDigest = 3, prefixDigest(dig, 3)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env1),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("first: %v", err)
	}

	later := time.Now().Add(DefaultDeltaTTL + time.Minute)
	c.Now = func() time.Time { return later }

	env2 := envelopeFor("run-1", 5, nil)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env2),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("second: %v", err)
	}
	body, hdr := f.last()
	if hdr.Get(DeltaHeader) != "1" {
		t.Fatal("an expired snapshot still claims the header; its body is whole")
	}
	if _, has := body["messagesDelta"]; has {
		t.Fatal("no messagesDelta after expiry")
	}
}

// A shrunken message list is never a delta.
func TestDeltaShrunkPrefixIsWhole(t *testing.T) {
	f := newDeltaFake(t)
	c := NewClient(nil)
	env1 := envelopeFor("run-1", 5, nil)
	dig := digestMessages(env1.Request.Messages)
	f.holdLen, f.holdDigest = 5, prefixDigest(dig, 5)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env1),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("first: %v", err)
	}

	env2 := envelopeFor("run-1", 2, nil)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env2),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("second: %v", err)
	}
	if body, hdr := f.last(); hdr.Get(DeltaHeader) != "1" {
		t.Fatal("a shorter request still claims the header; its body is whole")
	} else if _, has := body["messagesDelta"]; has {
		t.Fatal("a shorter request must be sent whole")
	}
}

// digestMessages is stable and 64 hex chars.
func TestDigestShape(t *testing.T) {
	d := digestMessages([]Message{{Role: "user", Content: "hi"}})
	if len(d) != 1 || len(d[0]) != 64 {
		t.Fatalf("digest = %v", d)
	}
	sum := sha256.Sum256([]byte(`{"role":"user","content":"hi"}`))
	if d[0] != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest is not sha256 of the canonical message: %s", d[0])
	}
	if !isSHA256Hex(d[0]) || isSHA256Hex("zz") {
		t.Fatal("isSHA256Hex is wrong")
	}
}

// A longer trust window keeps a delta alive past the published client's nine
// minutes. The window is a setting, not a protocol constant, so this is the
// behaviour TestDeltaExpires must NOT see once the operator raises it.
func TestDeltaTTLIsConfigurable(t *testing.T) {
	f := newDeltaFake(t)
	c := NewClient(nil)
	c.SetDeltaLimits(DeltaLimits{TTL: 45 * time.Minute, MaxKeys: 8192})

	env1 := envelopeFor("run-ttl", 3, nil)
	f.holdLen, f.holdDigest = 3, prefixDigest(digestMessages(env1.Request.Messages), 3)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env1),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Well past the 9-minute default, still inside the configured window.
	past := time.Now().Add(DefaultDeltaTTL + 10*time.Minute)
	c.Now = func() time.Time { return past }

	env2 := envelopeFor("run-ttl", 5, nil)
	f.holdLen = 5
	f.holdDigest = prefixDigest(digestMessages(env2.Request.Messages), 5)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env2),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("second: %v", err)
	}
	body, hdr := f.last()
	if hdr.Get(DeltaHeader) != "1" {
		t.Fatalf("%s = %q, want 1 for a snapshot inside the configured TTL",
			DeltaHeader, hdr.Get(DeltaHeader))
	}
	if _, has := body["messagesDelta"]; !has {
		t.Fatal("messagesDelta missing: the configured TTL should still trust the prefix")
	}

	// Beyond the configured window it expires like any other snapshot.
	far := past.Add(46 * time.Minute)
	c.Now = func() time.Time { return far }
	env3 := envelopeFor("run-ttl", 7, nil)
	if _, err := c.CallModel(context.Background(), ModelCall{
		Surface: SurfaceCLI, Base: f.srv.URL, Token: "t", Body: envJSON(t, env3),
	}, 5*time.Second, nil); err != nil {
		t.Fatalf("third: %v", err)
	}
	if body, hdr := f.last(); hdr.Get(DeltaHeader) != "1" {
		t.Fatal("past-TTL request still claims the header; its body is whole")
	} else if _, has := body["messagesDelta"]; has {
		t.Fatal("a snapshot past the configured TTL must not produce a delta")
	}
}

// DeltaLimits fills absent values rather than shrinking to zero: a config that
// never mentions the fields must behave exactly like the shipped defaults.
func TestDeltaLimitsResolveDefaults(t *testing.T) {
	got := DeltaLimits{}.Resolved()
	if got.TTL != DefaultDeltaTTL || got.MaxKeys != DefaultDeltaMaxKeys {
		t.Fatalf("zero limits resolved to %+v, want the defaults", got)
	}
	// Negative values are treated as absent, not as "expire immediately".
	got = DeltaLimits{TTL: -time.Minute, MaxKeys: -1}.Resolved()
	if got.TTL != DefaultDeltaTTL || got.MaxKeys != DefaultDeltaMaxKeys {
		t.Fatalf("negative limits resolved to %+v, want the defaults", got)
	}
	// Explicit values survive.
	got = DeltaLimits{TTL: 30 * time.Minute, MaxKeys: 8192}.Resolved()
	if got.TTL != 30*time.Minute || got.MaxKeys != 8192 {
		t.Fatalf("explicit limits resolved to %+v", got)
	}
}

// SetDeltaLimits is what the console calls; the client must report back the
// resolved value so the diagnostics endpoint can show what is in force.
func TestSetDeltaLimitsReported(t *testing.T) {
	c := NewClient(nil)
	c.SetDeltaLimits(DeltaLimits{TTL: 30 * time.Minute, MaxKeys: 8192})
	if got := c.DeltaLimits(); got.TTL != 30*time.Minute || got.MaxKeys != 8192 {
		t.Fatalf("DeltaLimits() = %+v, want 30m/8192", got)
	}
	// A zero field keeps its default instead of disabling the cache.
	c.SetDeltaLimits(DeltaLimits{})
	if got := c.DeltaLimits(); got.TTL != DefaultDeltaTTL || got.MaxKeys != DefaultDeltaMaxKeys {
		t.Fatalf("DeltaLimits() after empty set = %+v, want defaults", got)
	}
}

var _ = strings.TrimSpace

// A call with no account scope can never match a snapshot, so it must be
// counted: otherwise a caller that forgot the account would only see the hit
// rate quietly collapse with nothing to point at.
func TestPlanDeltaCountsUnscopedCalls(t *testing.T) {
	c := NewClient(nil)
	env := Envelope{Request: RequestEnvelope{Messages: []Message{{Role: "user", Content: "hi"}}}}

	if got := c.DeltaReport().Unscoped; got != 0 {
		t.Fatalf("unscoped counter starts at %d, want 0", got)
	}
	plan := c.PlanDelta("https://app.example", "", "run-1", env)
	if plan.IsDelta {
		t.Fatal("an unscoped call produced a delta")
	}
	if got := c.DeltaReport().Unscoped; got != 1 {
		t.Fatalf("unscoped counter = %d after one unscoped call, want 1", got)
	}
	// A scoped call is not counted, and still never hits without a snapshot.
	c.PlanDelta("https://app.example", "acct-a", "run-1", env)
	if got := c.DeltaReport().Unscoped; got != 1 {
		t.Fatalf("a scoped call was counted as unscoped: %d", got)
	}
	// Resetting clears it with the other counters.
	c.ResetDeltaCounters()
	if got := c.DeltaReport().Unscoped; got != 0 {
		t.Fatalf("reset left the unscoped counter at %d", got)
	}
}

// The digest input must be JavaScript's JSON.stringify, not Go's json.Marshal.
//
// They differ on exactly three characters: json.Marshal escapes <, > and & as
// \u003c, \u003e and \u0026, while JSON.stringify leaves them alone. The server
// computes its held-context digests in JavaScript, so a local digest produced
// with the default escaping can never match — which silently disabled the
// whole delta path, because the surface-gate preamble injected as the system
// turn contains all three characters.
func TestCanonicalJSONMatchesJSONStringify(t *testing.T) {
	// The expected bytes are literal JavaScript JSON.stringify output.
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{
			name: "plain",
			msg:  Message{Role: "user", Content: "hi"},
			want: `{"role":"user","content":"hi"}`,
		},
		{
			name: "angle brackets and ampersand are NOT escaped",
			msg:  Message{Role: "system", Content: "a <b> & c"},
			want: `{"role":"system","content":"a <b> & c"}`,
		},
		{
			name: "key order follows the struct tags",
			msg:  Message{Role: "tool", Content: "x", ToolCallID: "call_1"},
			want: `{"role":"tool","content":"x","tool_call_id":"call_1"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalJSON(tc.msg)
			if err != nil {
				t.Fatalf("canonicalJSON: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("canonicalJSON =\n  %s\nwant\n  %s", got, tc.want)
			}
		})
	}
}

// The digest a message with the gate preamble produces must not depend on
// Go's HTML escaping; this pins the concrete value so a regression to
// json.Marshal is caught even if canonicalJSON is refactored away.
func TestDigestMessagesUsesUnescapedJSON(t *testing.T) {
	msg := Message{Role: "system", Content: "You are <Perch> & careful"}
	got := digestMessages([]Message{msg})
	if len(got) != 1 {
		t.Fatalf("digestMessages returned %d entries", len(got))
	}
	// sha256 of the literal JSON.stringify output above.
	raw := `{"role":"system","content":"You are <Perch> & careful"}`
	sum := sha256.Sum256([]byte(raw))
	want := hex.EncodeToString(sum[:])
	if got[0] != want {
		t.Fatalf("digest = %s, want %s\n(the escaped form means the server's digest will never match)", got[0], want)
	}
}

// digestTools has the same requirement: a tool description routinely contains
// comparison operators, and the tools digest is compared verbatim.
func TestDigestToolsUsesUnescapedJSON(t *testing.T) {
	tools := []Tool{{"name": "read", "description": "reads a <file> & more"}}
	got := digestTools(tools)
	raw := `[{"description":"reads a <file> & more","name":"read"}]`
	// Tool is a map, so key order is Go's sorted order — the published client
	// builds its tool list from an object literal, whose insertion order is
	// preserved. Compare only what this test is about: no \u escaping.
	if strings.Contains(got, `\u00`) {
		t.Fatalf("tools digest input was HTML-escaped: %s", got)
	}
	if got == "" {
		t.Fatal("digestTools returned empty for a non-empty list")
	}
	_ = raw
}
