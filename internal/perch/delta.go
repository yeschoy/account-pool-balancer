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
	"bytes"
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

// Delta cache limits.
//
// The published client hard-codes these (its uYr / dYr / pYr): a 9-minute
// trust window, 32 keys, 4 snapshots per key. The first two are fitting
// parameters, not protocol facts — one process there serves one account — so
// both are fields on the cache and an operator can raise them.
//
// What a stale TTL costs is worth stating plainly, because raising it is not
// free: an EXPIRED snapshot never causes an error. find() skips it and
// PlanDelta returns a whole-request plan, so the pool resends the full message
// list — one request, no extra ticket, no extra spend. A TTL longer than the
// server's own memory is the expensive direction: the pool claims a prefix the
// server has since dropped, takes a 409, and resends the whole request anyway.
// That is two round trips where the shorter window sent one.
const (
	// DefaultDeltaTTL is how long a confirmed snapshot is trusted out of the
	// box. Nine minutes matches the published client, whose turns are seconds
	// apart; a pool serving chat and agent sessions keeps a working set open
	// longer, which is why the setting exists.
	DefaultDeltaTTL = 9 * time.Minute
	// DefaultDeltaMaxKeys caps cached endpoint+account+run entries.
	//
	// The client keeps 32 because it serves one account from one process.
	// This pool serves many, so the same cache has to hold a working set per
	// account; 4096 also lines up with maxRunIds, so a conversation whose run
	// id is still tracked is not pushed out of the prefix cache.
	DefaultDeltaMaxKeys = 4096
	// DeltaMaxSnapshotsPerKey caps snapshots per key; the client keeps the
	// most recent four. Kept at four: it bounds the work find() does per key,
	// and the longest prefix wins regardless.
	DeltaMaxSnapshotsPerKey = 4
	// DeltaMinPrefix is the shortest prefix worth sending as a delta. A delta
	// of zero or one new message over a long held prefix is a win; a delta
	// that repeats most of the request is not, and the whole body is clearer.
	DeltaMinHeldMessages = 1
)

// DeltaLimits is the operator's view of the cache sizing.
//
// It is resolved when the Perch setting is saved (SetDeltaLimits), not read per
// request: the cache consults ttl/maxKeys on the hot path under its own lock,
// and a settings lookup there would put a lock under a lock.
type DeltaLimits struct {
	// TTL is how long a confirmed snapshot is trusted.
	TTL time.Duration
	// MaxKeys caps cached keys.
	MaxKeys int
}

// Resolved fills a missing or nonsensical value with the default.
func (l DeltaLimits) Resolved() DeltaLimits {
	if l.TTL <= 0 {
		l.TTL = DefaultDeltaTTL
	}
	if l.MaxKeys <= 0 {
		l.MaxKeys = DefaultDeltaMaxKeys
	}
	return l
}

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

// deltaCache holds confirmed prefixes per endpoint+run, plus the counters the
// diagnostics endpoint reports.
type deltaCache struct {
	mu   sync.Mutex
	byK  map[string][]*deltaEntry
	seen time.Time

	// limits is the trust window and key cap in force. Zero means the
	// defaults (see DeltaLimits.Resolved); SetDeltaLimits assigns it under mu,
	// and find/put read it under the same lock.
	limits DeltaLimits

	// Counters. Every model call lands in exactly one of whole/delta; a delta
	// that the server refused is counted separately so a persistently failing
	// prefix is visible instead of just looking like traffic.
	statDelta       int64 // requests sent as a held-context delta
	statWhole       int64 // requests sent with the full message list
	statRefused     int64 // deltas the server refused (409, or 400 without ack)
	statConfirmed   int64 // snapshots the server confirmed and were cached
	statUnconfirmed int64 // deltas whose confirmation did not check out
	statBytesSaved  int64 // message bytes not sent, summed over deltas
	statBytesDelta  int64 // message bytes actually sent in deltas
	statMessagesCut int64 // messages not sent, summed over deltas
	statMsgSent     int64 // messages actually sent in deltas
	// statUnscoped counts plans built with no account scope. Such a call can
	// never hit the cache; a nonzero value means a caller is not passing the
	// account and delta sending is silently disabled for it.
	statUnscoped int64
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
		c.deltas.mu.Lock()
		c.deltas.statConfirmed++
		c.deltas.mu.Unlock()
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
	c.deltas.mu.Lock()
	c.deltas.statUnconfirmed++
	c.deltas.mu.Unlock()
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
// key is the cache key: endpoint + "\n" + account + "\n" + runId.
//
// The published client keys by endpoint+runId because one process serves one
// account. This pool does not: the same conversation can fail over to a
// different account, and a snapshot confirmed by account A says nothing about
// what account B holds. Including the account keeps the two apart, so a
// failover sends the whole request instead of claiming a prefix B never saw.
//
// Empty runId never produces a delta.
func (c *Client) PlanDelta(endpoint, scope, runID string, env Envelope) *DeltaPlan {
	plan := &DeltaPlan{}
	run := strings.TrimSpace(runID)
	if run == "" {
		return plan
	}
	key := deltaKey(endpoint, scope, run)
	plan.key = key
	// A call with no account scope can never match a snapshot, so it always
	// sends the whole request. That is the safe default, but it is also
	// silent: a caller that forgot to pass the account would see delta
	// hits quietly drop to zero with nothing to point at. Counting it makes
	// the degradation visible in the delta report.
	if strings.TrimSpace(scope) == "" {
		c.deltas.mu.Lock()
		c.deltas.statUnscoped++
		c.deltas.mu.Unlock()
	}
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

// deltaKey names one account's run at one endpoint.
func deltaKey(endpoint, scope, runID string) string {
	return strings.TrimSpace(endpoint) + "\n" + strings.TrimSpace(scope) + "\n" + strings.TrimSpace(runID)
}

// find returns the newest confirmed snapshot whose digests are a prefix of
// digests, preferring the longest, and only within the trust window.
func (c *deltaCache) find(key string, digests []string, now time.Time) *deltaEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := c.byK[key]
	if len(entries) == 0 {
		return nil
	}
	ttl := c.limits.Resolved().TTL
	var best *deltaEntry
	for _, e := range entries {
		if now.Sub(e.confirmedAt) > ttl {
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
	maxKeys := c.limits.Resolved().MaxKeys
	for len(c.byK) > maxKeys {
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

// SetDeltaLimits applies the console's cache sizing.
//
// It takes effect on the next find/put: an entry already stored under a longer
// window is re-checked against the new one, so lowering the TTL expires it
// immediately instead of leaving it trusted until the old window ran out.
// Snapshots beyond a lowered key cap are *not* trimmed here; the next put
// evicts down to the cap, which keeps this call free of O(n) work on a
// settings save. A zero field keeps its default (see DeltaLimits.Resolved).
func (c *Client) SetDeltaLimits(lim DeltaLimits) {
	lim = lim.Resolved()
	c.deltas.mu.Lock()
	c.deltas.limits = lim
	c.deltas.mu.Unlock()
}

// DeltaLimits reports the sizing in force, for the diagnostics endpoint.
func (c *Client) DeltaLimits() DeltaLimits {
	c.deltas.mu.Lock()
	defer c.deltas.mu.Unlock()
	return c.deltas.limits.Resolved()
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

// DeltaReport is what the diagnostics endpoint shows about held-context deltas.
//
// The counts are process-wide and cumulative since start (or since the last
// ResetDeltaCounters): the question they answer is "is the pool actually
// sending only what is new", which a single sample cannot show.
type DeltaReport struct {
	// CachedKeys / CachedSnapshots are the live cache size.
	CachedKeys      int `json:"cachedKeys"`
	CachedSnapshots int `json:"cachedSnapshots"`
	// TrackedConversations is how many conversations hold a stable run id.
	TrackedConversations int `json:"trackedConversations"`

	// Delta / Whole split every model call the pool made.
	Delta int64 `json:"deltaRequests"`
	Whole int64 `json:"wholeRequests"`
	// Refused counts deltas the server rejected and that were resent whole.
	Refused int64 `json:"refusedDeltas"`
	// Unscoped counts calls planned with no account scope, which can never hit
	// the cache. It should be 0; a nonzero value means a caller is omitting the
	// account and that traffic is permanently sent whole.
	Unscoped int64 `json:"unscopedCalls"`
	// HitRate is Delta / (Delta + Whole), 0 when nothing has been sent.
	HitRate float64 `json:"hitRate"`

	// Confirmed / Unconfirmed count cache settlements that did and did not
	// verify against the server's x-perch-call-held-* headers.
	Confirmed   int64 `json:"confirmedPrefixes"`
	Unconfirmed int64 `json:"unconfirmedPrefixes"`

	// MessagesOmitted / MessagesSent measure the saving the delta bought.
	MessagesOmitted int64 `json:"messagesOmitted"`
	MessagesSent    int64 `json:"messagesSent"`
	// BytesOmitted / BytesSent are the same in JSON message bytes.
	BytesOmitted int64 `json:"bytesOmitted"`
	BytesSent    int64 `json:"bytesSent"`

	// Limits says what the cache is actually sized to, so an operator reading
	// the report can tell a miss from a misconfiguration.
	Limits DeltaLimitsView `json:"limits"`
}

// DeltaLimitsView is the report's rendering of the sizing in force.
type DeltaLimitsView struct {
	TTLMinutes int `json:"ttlMinutes"`
	MaxKeys    int `json:"maxKeys"`
}

// DeltaReport returns the counters. It never contacts the server.
func (c *Client) DeltaReport() DeltaReport {
	keys, snaps := c.DeltaStats()
	c.deltas.mu.Lock()
	defer c.deltas.mu.Unlock()
	rep := DeltaReport{
		CachedKeys:      keys,
		CachedSnapshots: snaps,
		Delta:           c.deltas.statDelta,
		Whole:           c.deltas.statWhole,
		Refused:         c.deltas.statRefused,
		Unscoped:        c.deltas.statUnscoped,
		Confirmed:       c.deltas.statConfirmed,
		Unconfirmed:     c.deltas.statUnconfirmed,
		MessagesOmitted: c.deltas.statMessagesCut,
		MessagesSent:    c.deltas.statMsgSent,
		BytesOmitted:    c.deltas.statBytesSaved,
		BytesSent:       c.deltas.statBytesDelta,
		Limits: DeltaLimitsView{
			TTLMinutes: int(c.deltas.limits.Resolved().TTL / time.Minute),
			MaxKeys:    c.deltas.limits.Resolved().MaxKeys,
		},
	}
	if total := rep.Delta + rep.Whole; total > 0 {
		rep.HitRate = float64(rep.Delta) / float64(total)
	}
	return rep
}

// RecordSending counts one request as delta or whole, with the byte/message
// saving it represents. Called by CallModel right before the request goes out.
func (c *Client) RecordSending(plan *DeltaPlan, env Envelope) {
	c.deltas.mu.Lock()
	defer c.deltas.mu.Unlock()
	if plan != nil && plan.IsDelta && plan.base != nil {
		held := len(plan.base.digests)
		total := len(env.Request.Messages)
		if held <= total {
			sent := total - held
			c.deltas.statDelta++
			c.deltas.statMessagesCut += int64(held)
			c.deltas.statMsgSent += int64(sent)
			c.deltas.statBytesSaved += messageBytes(env.Request.Messages[:held])
			c.deltas.statBytesDelta += messageBytes(env.Request.Messages[held:])
			return
		}
	}
	c.deltas.statWhole++
	if b := messageBytes(env.Request.Messages); b > 0 {
		c.deltas.statBytesDelta += b
	}
}

// RecordRefused counts a delta the server rejected.
func (c *Client) RecordRefused() {
	c.deltas.mu.Lock()
	c.deltas.statRefused++
	c.deltas.mu.Unlock()
}

// messageBytes is the JSON size of a message list, used only for the report.
func messageBytes(msgs []Message) int64 {
	if len(msgs) == 0 {
		return 0
	}
	b, err := json.Marshal(msgs)
	if err != nil {
		return 0
	}
	return int64(len(b))
}

// ResetDeltaCounters zeroes the cumulative counters, keeping the cache. An
// operator uses this after a change to see the new behaviour on its own.
func (c *Client) ResetDeltaCounters() {
	c.deltas.mu.Lock()
	c.deltas.statDelta = 0
	c.deltas.statWhole = 0
	c.deltas.statRefused = 0
	c.deltas.statConfirmed = 0
	c.deltas.statUnconfirmed = 0
	c.deltas.statBytesSaved = 0
	c.deltas.statBytesDelta = 0
	c.deltas.statMessagesCut = 0
	c.deltas.statMsgSent = 0
	c.deltas.statUnscoped = 0
	c.deltas.mu.Unlock()
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

// canonicalJSON renders one value the way the published client's
// JSON.stringify does.
//
// This cannot be left to encoding/json's defaults. Go's json.Marshal escapes
// <, > and & as \u003c, \u003e and \u0026 (an XSS guard for embedding JSON in
// HTML); JavaScript's JSON.stringify leaves them alone. The digests below are
// compared against digests the SERVER computes in JavaScript, so a message
// containing one of those three characters yields a local digest the server
// never agrees with.
//
// That is not hypothetical: the surface-gate preamble this pool injects as the
// system turn contains eleven '<', five '>' and seven '&'. Under the default
// escaping every request's prefix digest disagreed with the server's, so
// SettleDelta could never confirm a prefix, the delta cache stayed empty, and
// every model call went out whole — measured as confirmedPrefixes=0 while
// unconfirmedPrefixes rose one-for-one with wholeRequests.
//
// Marshal offers no way to disable the escaping, so the value goes through an
// Encoder with SetEscapeHTML(false) and its trailing newline trimmed. Key order
// is unchanged: it follows the struct tags, which match the order the published
// client writes (role, content, tool_calls, ...).
func canonicalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// digestMessages is the client's XQt + SHA-256 over each message's canonical
// JSON.
//
// The per-message JSON has to match JavaScript's JSON.stringify byte for byte;
// see canonicalJSON for why that is not the same thing as json.Marshal.
func digestMessages(msgs []Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		b, err := canonicalJSON(m)
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
	b, err := canonicalJSON(tools)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

