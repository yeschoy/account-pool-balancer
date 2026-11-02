package perch

// Held-context delta: sending only the messages the server does not already
// have.
//
// Perch does not require a turn to resend its whole conversation. The published
// client remembers what the server confirmed it is holding and, when the next
// request of the same run repeats that prefix, sends only the new tail:
//
//	{..., "messagesDelta": {"baseLength": N, "baseDigest": "<sha256>",
//	                        "newMessages": [...]}, "toolsDigest": "<sha256>"}
//
// with the request header `x-perch-call-delta: 1`. When the tool list is also
// unchanged, `tools` is omitted from the body entirely. This file reproduces
// that behaviour, including the parts that are easy to get wrong:
//
//   - The cache key is endpoint + runId, NOT the conversation. A new run never
//     hits the cache and always sends the whole request.
//   - A snapshot is only trusted after the server confirms it, by answering
//     with x-perch-call-held-length / -digest / -tools. Storing a guess would
//     make the next request claim a base the server never agreed to.
//   - A refusal is detected on 409, or on 400 without the delta ack header, and
//     the caller must then resend the whole request. The pool repeats the
//     published client here rather than assuming the server accepted a delta.
//   - Snapshots expire after nine minutes and are capped per key and globally.
//
// See docs/perch-desktop-and-ban-analysis.md and
// .analysis/perch-turn-ticket-flow.md for the byte-level traces.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// Delta header/field names, exactly as the published client spells them.
const (
	// DeltaHeader marks a request as a held-context delta.
	DeltaHeader = "x-perch-call-delta"
	// HeldLength / HeldDigest / HeldTools are the server's confirmation that
	// it holds a given message prefix and tool list.
	HeldLengthHeader = "x-perch-call-held-length"
	HeldDigestHeader = "x-perch-call-held-digest"
	HeldToolsHeader  = "x-perch-call-held-tools"
)

// Delta cache limits (the client's Tqr / Iqr / Rqr).
const (
	// DeltaTTL is how long a confirmed snapshot is trusted.
	DeltaTTL = 9 * time.Minute
	// DeltaMaxKeys caps the number of cached endpoints+runs.
	DeltaMaxKeys = 32
	// DeltaMaxSnapshotsPerKey caps snapshots per key; the client keeps the
	// most recent four.
	DeltaMaxSnapshotsPerKey = 4
	// DeltaMinPrefix is the shortest prefix worth sending as a delta. A delta
	// of zero or one new message over a long held prefix is a win; a delta
	// that repeats most of the request is not, and the whole body is clearer.
	DeltaMinHeldMessages = 1
)

// deltaEntry is one confirmed prefix the server is holding.
type deltaEntry struct {
	// digests are the per-message SHA-256 values of the held prefix.
	digests []string
	// toolsDigest is the SHA-256 of the held tool list ("" = no tools sent).
	toolsDigest string
	// digest is the server's own summary digest of the prefix, echoed back so
	// the next request can name the same base.
	digest string
	// confirmedAt is when the server confirmed it.
	confirmedAt time.Time
}

// DeltaPlan is one request's delta decision: whether it is a delta, what body
// and headers to send, and how to record the answer.
type DeltaPlan struct {
	// IsDelta reports whether a held prefix was found and is being used.
	IsDelta bool
	// Headers are the extra headers the request must carry.
	Headers map[string]string
	// base is the held prefix, nil for a whole-request plan.
	base *deltaEntry
	// key / digests / toolsDigest describe THIS request, for settling.
	key         string
	digests     []string
	toolsDigest string
	// status / held / acked are filled by Observe from the server's answer.
	status int
	held   *heldInfo
	acked  bool
}

// deltaCache holds confirmed prefixes per endpoint+run.
type deltaCache struct {
	mu   sync.Mutex
	byK  map[string][]*deltaEntry
	seen time.Time
}

// Body renders the request for this plan.
//
// env is the complete envelope. In delta mode it becomes
//
//	{"request": {...without messages/tools...}, "messagesDelta": {...}}
//
// and tools are omitted when the tool list is unchanged, which is what the
// published client does. The returned envelope keeps every other field as-is.
func (p *DeltaPlan) Body(env Envelope) ([]byte, error) {
	if !p.IsDelta || p.base == nil {
		return json.Marshal(env)
	}
	// Carve messages and tools out of the inner request.
	full := env.Request
	held := len(p.base.digests)
	newMessages := []Message{}
	if held <= len(full.Messages) {
		newMessages = full.Messages[held:]
	} else {
		// The prefix shrank: this cannot be a delta over that base.
		p.IsDelta = false
		return json.Marshal(env)
	}
	toolsHeld := p.base.toolsDigest != "" && p.base.toolsDigest == p.toolsDigest

	trimmed := full
	trimmed.Messages = nil // omitted by `omitempty`
	if toolsHeld {
		trimmed.Tools = nil
		trimmed.ToolChoice = nil
	}

	out := struct {
		Request       RequestEnvelope `json:"request"`
		MessagesDelta *messagesDelta  `json:"messagesDelta,omitempty"`
		ToolsDigest   string          `json:"toolsDigest,omitempty"`
		RunID         string          `json:"runId"`
		ThreadID      string          `json:"threadId,omitempty"`
		ChatMode      string          `json:"chatMode,omitempty"`
		Lane          string          `json:"lane"`
		Surface       string          `json:"clientSurface"`
		Strict        bool            `json:"strictManual"`
		Attrib        map[string]any  `json:"attribution,omitempty"`
		AvoidIDs      []string        `json:"avoidModelIds"`
		Resend        bool            `json:"resendAfterOutput"`
		PreferredID   string          `json:"preferredModelId,omitempty"`
		ManualID      string          `json:"manualModelOptionId,omitempty"`
		Roost         string          `json:"roostModelChoice,omitempty"`
		Reasoning     any             `json:"reasoning,omitempty"`
	}{
		Request: trimmed,
		MessagesDelta: &messagesDelta{
			BaseLength:  held,
			BaseDigest:  p.base.digest,
			NewMessages: newMessages,
		},
		RunID: env.RunID, ThreadID: env.ThreadID, ChatMode: env.ChatMode,
		Lane: env.Lane, Surface: env.Surface, Strict: env.Strict,
		Attrib: env.Attrib, AvoidIDs: env.AvoidIDs, Resend: env.Resend,
		PreferredID: env.PreferredModelID, ManualID: env.ManualModelOptionID,
		Roost: env.RoostModelChoice, Reasoning: env.Reasoning,
	}
	if toolsHeld {
		out.ToolsDigest = p.toolsDigest
	}
	return json.Marshal(out)
}

// messagesDelta is the held-prefix claim.
type messagesDelta struct {
	BaseLength  int       `json:"baseLength"`
	BaseDigest  string    `json:"baseDigest"`
	NewMessages []Message `json:"newMessages"`
}

// Observe records the server's answer headers so the caller can decide whether
// the delta was refused and whether the snapshot may be kept.
func (p *DeltaPlan) Observe(status int, hdr map[string]string) {
	p.status = status
	p.held = parseHeld(hdr)
	p.acked = strings.TrimSpace(hdr[DeltaHeader]) == "1"
}

// Refused reports that the server did not accept this delta, so the caller must
// resend the whole request.
//
// The published client refuses on 409, or on 400 without the delta ack header.
func (p *DeltaPlan) Refused() bool {
	if !p.IsDelta || p.base == nil {
		return false
	}
	return p.status == 409 || (p.status == 400 && !p.acked)
}

// Settle decides whether the request's prefix may be cached for the next call.
//
// A snapshot is stored only when the server confirmed it: the held length must
// match this request's message count, the tool digest must match, and the
// echoed digest must equal the digest over the same prefix. A failed turn never
// stores one — the client's settle(ok) takes the same view.
func (c *Client) SettleDelta(p *DeltaPlan, ok bool) {
	if p == nil || p.key == "" || p.digests == nil {
		return
	}
	// A whole request can establish the baseline too: the first call of a run
	// sends everything, and the server's confirmation of how much it now holds
	// is exactly what makes the SECOND call a delta. Requiring a delta here
	// would mean a prefix could never be learned.
	if ok && p.held != nil &&
		p.held.length == len(p.digests) &&
		p.held.toolsDigest == p.toolsDigest &&
		p.held.digest == prefixDigest(p.digests, len(p.digests)) {
		c.deltas.put(p.key, &deltaEntry{
			digests:     p.digests,
			toolsDigest: p.toolsDigest,
			digest:      p.held.digest,
			confirmedAt: c.now(),
		})
		return
	}
	// The server does not hold this prefix: drop any older snapshot that
	// claims it, so the next call starts from the whole request.
	c.deltas.dropPrefix(p.key, p.digests)
}

// heldInfo is the server's confirmation.
type heldInfo struct {
	length      int
	digest      string
	toolsDigest string
}

// parseHeld reads the three confirmation headers. A missing or malformed length
// or digest means "not confirmed".
func parseHeld(hdr map[string]string) *heldInfo {
	if len(hdr) == 0 {
		return nil
	}
	rawLen := strings.TrimSpace(hdr[HeldLengthHeader])
	if rawLen == "" {
		return nil
	}
	n := 0
	for _, r := range rawLen {
		if r < '0' || r > '9' {
			return nil
		}
		n = n*10 + int(r-'0')
		if n > 1<<20 {
			return nil
		}
	}
	if n < 1 {
		return nil
	}
	dig := strings.ToLower(strings.TrimSpace(hdr[HeldDigestHeader]))
	if !isSHA256Hex(dig) {
		return nil
	}
	tools := strings.ToLower(strings.TrimSpace(hdr[HeldToolsHeader]))
	if !isSHA256Hex(tools) {
		tools = ""
	}
	return &heldInfo{length: n, digest: dig, toolsDigest: tools}
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// PlanDelta decides how to send one request.
//
// It returns a plan; a nil base means "send the whole request", which is what
// happens for a run with no confirmed prefix, an expired snapshot, or a request
// whose messages do not extend a held prefix.
//
// key is the cache key: endpoint + "\n" + runId, exactly as the client builds
// it. Empty runId never produces a delta.
func (c *Client) PlanDelta(endpoint, runID string, env Envelope) *DeltaPlan {
	plan := &DeltaPlan{}
	run := strings.TrimSpace(runID)
	if run == "" {
		return plan
	}
	key := strings.TrimSpace(endpoint) + "\n" + run
	plan.key = key
	plan.digests = digestMessages(env.Request.Messages)
	plan.toolsDigest = digestTools(env.Request.Tools)

	base := c.deltas.find(key, plan.digests, c.now())
	if base == nil || len(base.digests) == 0 {
		return plan
	}
	if len(base.digests) > len(plan.digests) {
		// The request is shorter than what the server holds: not a continuation.
		return plan
	}
	plan.IsDelta = true
	plan.base = base
	plan.Headers = map[string]string{DeltaHeader: "1"}
	return plan
}

// find returns the newest confirmed snapshot whose digests are a prefix of
// digests, preferring the longest, and only within DeltaTTL.
func (c *deltaCache) find(key string, digests []string, now time.Time) *deltaEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := c.byK[key]
	if len(entries) == 0 {
		return nil
	}
	var best *deltaEntry
	for _, e := range entries {
		if now.Sub(e.confirmedAt) > DeltaTTL {
			continue
		}
		if len(e.digests) > len(digests) || !hasPrefix(digests, e.digests) {
			continue
		}
		if best == nil || len(e.digests) > len(best.digests) {
			best = e
		}
	}
	return best
}

// put stores a confirmed snapshot, keeping the newest few per key and capping
// the number of keys.
func (c *deltaCache) put(key string, e *deltaEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byK == nil {
		c.byK = map[string][]*deltaEntry{}
	}
	c.dropPrefixLocked(key, e.digests)
	list := append(c.byK[key], e)
	if len(list) > DeltaMaxSnapshotsPerKey {
		list = list[len(list)-DeltaMaxSnapshotsPerKey:]
	}
	c.byK[key] = list
	// Evict the oldest keys when the cap is exceeded.
	for len(c.byK) > DeltaMaxKeys {
		var oldestKey string
		var oldest time.Time
		for k, es := range c.byK {
			if k == key || len(es) == 0 {
				continue
			}
			if oldestKey == "" || es[0].confirmedAt.Before(oldest) {
				oldestKey, oldest = k, es[0].confirmedAt
			}
		}
		if oldestKey == "" {
			break
		}
		delete(c.byK, oldestKey)
	}
}

// dropPrefix forgets snapshots of key whose prefix equals digests (the client's
// t9e), which is how a rejected prefix stops being offered.
func (c *deltaCache) dropPrefix(key string, digests []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropPrefixLocked(key, digests)
}

func (c *deltaCache) dropPrefixLocked(key string, digests []string) {
	list := c.byK[key]
	if len(list) == 0 {
		return
	}
	kept := list[:0]
	for _, e := range list {
		if hasPrefix(digests, e.digests) {
			continue
		}
		kept = append(kept, e)
	}
	if len(kept) > 0 {
		c.byK[key] = kept
		return
	}
	delete(c.byK, key)
}

// Reset forgets every snapshot (used when the console changes settings).
func (c *Client) ResetDeltaCache() {
	c.deltas.mu.Lock()
	c.deltas.byK = nil
	c.deltas.mu.Unlock()
}

// DeltaStats reports cached prefix counts, for the diagnostics endpoint.
func (c *Client) DeltaStats() (keys, snapshots int) {
	c.deltas.mu.Lock()
	defer c.deltas.mu.Unlock()
	for _, es := range c.deltas.byK {
		snapshots += len(es)
	}
	return len(c.deltas.byK), snapshots
}

// hasPrefix reports whether full starts with prefix.
func hasPrefix(full, prefix []string) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if full[i] != prefix[i] {
			return false
		}
	}
	return true
}

// prefixDigest is the client's ZQt: the digests of the first n messages,
// concatenated with no separator.
func prefixDigest(digests []string, n int) string {
	if n > len(digests) {
		n = len(digests)
	}
	sum := sha256.Sum256([]byte(strings.Join(digests[:n], "")))
	return hex.EncodeToString(sum[:])
}

// digestMessages is the client's XQt + SHA-256 over each message's canonical
// JSON.
func digestMessages(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		out[i] = hex.EncodeToString(sum[:])
	}
	return out
}

// digestTools is the client's JQt + SHA-256 over the tool list. An empty list
// yields "", which keeps the field out of the body.
func digestTools(tools []Tool) string {
	if len(tools) == 0 {
		return ""
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
