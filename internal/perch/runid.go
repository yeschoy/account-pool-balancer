package perch

// Run identity: giving the same conversation the same run id across requests.
//
// Perch's held-context delta is keyed by endpoint + runId, and a run id in the
// published client is one agent turn — several model calls that share a growing
// message list. A pool has the opposite shape: each downstream request is its
// own HTTP transaction, and the conversation arrives whole every time.
//
// If every request minted a fresh run id, nothing would ever match and a delta
// could never fire, so this file keeps the mapping from a conversation to a run
// id: one conversation keeps one run id for as long as it keeps asking, and the
// delta cache can then recognise the prefix the server already holds.
//
// The conversation key is the caller's, not ours. Downstream clients already
// tell us which conversation a request belongs to — session headers, a
// prompt_cache_key, a previous_response_id, or failing those a digest of the
// first messages — and internal/server/identity.go resolves it. Passing that
// resolved fingerprint here means an OpenAI chat request and a Responses request
// from the same agent land on the same run, which is what makes the cache hit.
//
// Two rules keep this honest:
//
//   - A run id is only reused while the conversation still looks like a
//     continuation. If the message list stops growing from the cached prefix,
//     the delta layer already sends the whole request, so reuse costs nothing.
//   - Reuse expires. A conversation that has been quiet for RunIdTTL gets a new
//     run id, which is what the published client does between turns; keeping one
//     forever would let two unrelated sessions inherit each other's prefix.

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RunIdTTL is how long a conversation keeps its run id while idle. It is
// deliberately longer than DeltaTTL: the delta cache has its own expiry, and a
// run id that outlived it would simply produce whole requests.
const RunIdTTL = 30 * time.Minute

// maxRunIds caps tracked conversations so a busy pool cannot grow without bound.
const maxRunIds = 4096

// runKey is one conversation's run id and when it was last used.
type runKey struct {
	id       string
	lastUsed time.Time
}

// runRegistry maps a conversation fingerprint to a run id.
type runRegistry struct {
	mu sync.Mutex
	// gen advances whenever a conversation is (re)assigned, so an expired or
	// forgotten conversation gets a genuinely different run id rather than the
	// same derived value again — a reused id would silently claim the prefix
	// the server still holds for the previous session.
	gen   uint64
	byKey map[string]*runKey
}

// RunFor returns the run id to use for one conversation on ONE ACCOUNT.
//
// scope identifies the account: a run id belongs to the account whose
// server-side context it refers to. The pool serves many accounts from one
// process, and a failover moves the same conversation to a different account —
// carrying the previous account's run id across would tell the new account the
// server already holds a prefix it has never seen. Scoping the key by account
// keeps each account's runs separate.
//
// fingerprint is the caller's stable conversation identity ("" when the request
// names no conversation at all). An empty fingerprint mints a fresh run id on
// every call: without a conversation there is nothing to continue, and reusing
// a run id across unrelated requests would claim a prefix that is not shared.
func (c *Client) RunFor(scope, fingerprint string) string {
	key := runScopeKey(scope, fingerprint)
	if key == "" {
		return NewRunID()
	}
	// An empty fingerprint is not a conversation: no reuse, no tracking.
	if strings.TrimSpace(fingerprint) == "" {
		return NewRunID()
	}
	now := c.now()
	c.runs.mu.Lock()
	defer c.runs.mu.Unlock()
	if c.runs.byKey == nil {
		c.runs.byKey = map[string]*runKey{}
	}
	if rk, ok := c.runs.byKey[key]; ok && now.Sub(rk.lastUsed) <= RunIdTTL {
		// Refresh recency so a long conversation is not evicted mid-use.
		rk.lastUsed = now
		return rk.id
	}
	c.runs.gen++
	id := ConversationRunID(key, c.runs.gen)
	c.runs.byKey[key] = &runKey{id: id, lastUsed: now}
	if len(c.runs.byKey) > maxRunIds {
		c.evictRunsLocked(now)
	}
	return id
}

// ForgetRun drops one conversation's run id on one account.
//
// Note what this is NOT for: a server that rejects a delta prefix (409) is
// saying the PREFIX does not match, not that the run is invalid, and the
// published client keeps the same run id there and simply resends the whole
// request. Use this when a run id itself must be abandoned — for instance when
// an operator wants the next request to look like a fresh turn.
func (c *Client) ForgetRun(scope, fingerprint string) {
	key := runScopeKey(scope, fingerprint)
	if key == "" {
		return
	}
	c.runs.mu.Lock()
	delete(c.runs.byKey, key)
	c.runs.mu.Unlock()
}

// runScopeKey joins an account scope and a conversation fingerprint. Either
// being empty yields "" so callers mint a fresh id instead of indexing a
// half-formed key.
func runScopeKey(scope, fingerprint string) string {
	acct := strings.TrimSpace(scope)
	fp := strings.TrimSpace(fingerprint)
	if acct == "" || fp == "" {
		return ""
	}
	return acct + "\n" + fp
}

// RunStats reports tracked conversations and whether any run id is live.
func (c *Client) RunStats() int {
	c.runs.mu.Lock()
	defer c.runs.mu.Unlock()
	return len(c.runs.byKey)
}

// evictRunsLocked drops expired entries first, then the oldest, so the registry
// stays bounded without discarding every conversation at once.
func (c *Client) evictRunsLocked(now time.Time) {
	for k, rk := range c.runs.byKey {
		if now.Sub(rk.lastUsed) > RunIdTTL {
			delete(c.runs.byKey, k)
		}
	}
	for len(c.runs.byKey) > maxRunIds {
		var oldestKey string
		var oldest time.Time
		for k, rk := range c.runs.byKey {
			if oldestKey == "" || rk.lastUsed.Before(oldest) {
				oldestKey, oldest = k, rk.lastUsed
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.runs.byKey, oldestKey)
	}
}

// ConversationRunID derives the stable run id of a conversation.
//
// The shape follows the published client's own run ids (cli-turn-<millis>-<rand>)
// so a pool call does not stand out by its id alone, but the value is derived
// from the conversation rather than minted at random: that is the whole point,
// because the same conversation must produce the same id across requests.
func ConversationRunID(fingerprint string, generation uint64) string {
	seed := "perch-run\x00" + fingerprint + "\x00" + strconv.FormatUint(generation, 10)
	sum := sha256.Sum256([]byte(seed))
	return "cli-turn-" + hex.EncodeToString(sum[:6]) + "-" + hex.EncodeToString(sum[6:9])
}
