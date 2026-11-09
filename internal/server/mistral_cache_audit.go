package server

// Opt-in, bounded diagnostics, not a cache or routing policy. Only opaque HMACs
// and structural counts enter request logs; raw input is never retained here.
import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"account-pool-balancer/internal/config"
)

const (
	mistralAuditTTL         = 30 * time.Minute
	mistralAuditRequests    = 128
	mistralAuditSessions    = 16
	mistralAuditConnections = 256
	mistralAuditAttempts    = 8
	mistralAuditEvents      = 4
	mistralAuditMessages    = 2048
	mistralAuditTools       = 256
	mistralAuditFields      = 128
)

type cacheBodyFingerprint struct {
	Bytes            int    `json:"bytes"`
	Tag              string `json:"tag"`
	Structure        string `json:"structure"`
	RootOrderTag     string `json:"rootOrderTag,omitempty"`
	OtherFieldsTag   string `json:"otherFieldsTag,omitempty"`
	Sequence         string `json:"sequence"`
	SequenceTag      string `json:"sequenceTag,omitempty"`
	SequenceCount    int    `json:"sequenceCount"`
	SequenceComplete bool   `json:"sequenceComplete"`
	ToolsTag         string `json:"toolsTag,omitempty"`
	ToolsCount       int    `json:"toolsCount"`
	ToolsComplete    bool   `json:"toolsComplete"`
	CacheKeyTag      string `json:"cacheKeyTag,omitempty"`
}

type cacheFingerprintState struct {
	view            cacheBodyFingerprint
	messages, tools [][16]byte
}

type cacheFingerprintComparison struct {
	BodyEqual                bool  `json:"bodyEqual"`
	RootOrderEqual           *bool `json:"rootOrderEqual,omitempty"`
	OtherFieldsEqual         *bool `json:"otherFieldsEqual,omitempty"`
	ToolsEqual               *bool `json:"toolsEqual,omitempty"`
	SequenceEqual            *bool `json:"sequenceEqual,omitempty"`
	PreviousSequenceCount    int   `json:"previousSequenceCount"`
	CommonSequencePrefix     *int  `json:"commonSequencePrefix,omitempty"`
	PreviousSequenceRetained *bool `json:"previousSequenceRetained,omitempty"`
	SequenceReordered        *bool `json:"sequenceReordered,omitempty"`
	PreviousToolsCount       int   `json:"previousToolsCount"`
	CommonToolsPrefix        *int  `json:"commonToolsPrefix,omitempty"`
	ToolsReordered           *bool `json:"toolsReordered,omitempty"`
}

type cachePreviousComparison struct {
	RequestID    string                     `json:"requestId"`
	AttemptIndex int                        `json:"attemptIndex"`
	AccountID    string                     `json:"accountId,omitempty"`
	MayOverlap   bool                       `json:"mayOverlap"`
	Comparison   cacheFingerprintComparison `json:"comparison"`
}

type mistralCacheAuditLog struct {
	Version           int                        `json:"version"`
	RunID             string                     `json:"runId"`
	SampleLimit       int                        `json:"sampleLimit"`
	Sample            int                        `json:"sample"`
	ExpiresAt         time.Time                  `json:"expiresAt"`
	SessionTag        string                     `json:"sessionTag,omitempty"`
	Client            cacheBodyFingerprint       `json:"client"`
	Prepared          cacheBodyFingerprint       `json:"prepared"`
	Preparation       cacheFingerprintComparison `json:"preparation"`
	PreviousClient    *cachePreviousComparison   `json:"previousClient,omitempty"`
	PreviousPrepared  *cachePreviousComparison   `json:"previousPrepared,omitempty"`
	Attempts          []cacheAuditAttemptLog     `json:"attempts"`
	AttemptsTruncated bool                       `json:"attemptsTruncated,omitempty"`
}

type cacheAuditBaseline struct {
	requestID, accountID string
	attempt              int
	order                int
	done                 *atomic.Bool
	client, prepared     *cacheFingerprintState
}

type mistralCacheAudit struct {
	mu             sync.Mutex
	keyID          string
	key            [32]byte
	runID          string
	expires        time.Time
	samples        int
	order          int
	nextConnection uint64
	history, wire  map[string]cacheAuditBaseline
	connections    map[net.Conn]string
}

type mistralAuditRequest struct {
	audit                 *mistralCacheAudit
	log                   *mistralCacheAuditLog
	prepared              *cacheFingerprintState
	historyKey, requestID string
	done                  *atomic.Bool
}

func newMistralCacheAudit(cfg *config.Config, keyID string, now time.Time) (*mistralCacheAudit, error) {
	if keyID == "" {
		return nil, nil
	}
	valid := false
	for _, k := range cfg.ClientKeys {
		if k.ID == keyID && k.Enabled && keyPool(cfg, k) == config.ProviderMistral {
			valid = true
		}
	}
	if !valid {
		return nil, errors.New("Mistral cache audit requires an enabled Mistral client key ID")
	}
	a := &mistralCacheAudit{keyID: keyID, expires: now.Add(mistralAuditTTL), history: make(map[string]cacheAuditBaseline), wire: make(map[string]cacheAuditBaseline), connections: make(map[net.Conn]string)}
	if _, err := rand.Read(a.key[:]); err != nil {
		return nil, errors.New("Mistral cache audit random source unavailable")
	}
	a.runID = a.tag("run", nil)
	return a, nil
}

func (a *mistralCacheAudit) bodyHash(size int) hash.Hash {
	h := hmac.New(sha256.New, a.key[:])
	h.Write([]byte("mistral-cache-audit/v1\x00body\x00"))
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(size))
	h.Write(n[:])
	return h
}
func (a *mistralCacheAudit) digest(domain string, values ...[]byte) [16]byte {
	h := hmac.New(sha256.New, a.key[:])
	h.Write([]byte("mistral-cache-audit/v1\x00" + domain + "\x00"))
	for _, v := range values {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(v)))
		h.Write(n[:])
		h.Write(v)
	}
	var out [16]byte
	copy(out[:], h.Sum(nil))
	return out
}
func (a *mistralCacheAudit) tag(domain string, values ...[]byte) string {
	sum := a.digest(domain, values...)
	return hex.EncodeToString(sum[:])
}
func (a *mistralCacheAudit) bodyTag(body []byte) string {
	h := a.bodyHash(len(body))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func (a *mistralCacheAudit) arrayTags(raw []byte, domain string, limit int) (int, [][16]byte, bool) {
	if len(raw) == 0 || raw[0] != '[' {
		return 0, nil, false
	}
	var tags [][16]byte
	count := 0
	for i := skipSpace(raw, 1); raw[i] != ']'; {
		end := skipValue(raw, i)
		if count < limit {
			tags = append(tags, a.digest(domain, raw[i:end]))
		}
		count++
		i = skipSpace(raw, end)
		if raw[i] == ',' {
			i = skipSpace(raw, i+1)
		}
	}
	return count, tags, count <= limit
}

func (a *mistralCacheAudit) fingerprint(body []byte, sequence string, reuse *cacheFingerprintState) *cacheFingerprintState {
	tag := a.bodyTag(body)
	if reuse != nil && reuse.view.Tag == tag && reuse.view.Sequence == sequence {
		return reuse
	}
	s := &cacheFingerprintState{view: cacheBodyFingerprint{Bytes: len(body), Tag: tag, Sequence: sequence, Structure: "invalid-json"}}
	if !json.Valid(body) {
		return s
	}
	i := skipSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return s
	}
	// Reuse the validated JSON wire scanners without allocating an unbounded
	// root-field slice or decoding/reordering any user-defined schema objects.
	fields := make(map[string][]byte)
	var order, other [][]byte
	for i = skipSpace(body, i+1); body[i] != '}'; {
		if len(order) == mistralAuditFields {
			s.view.Structure = "field-limit"
			return s
		}
		endKey := skipString(body, i)
		var key string
		_ = json.Unmarshal(body[i:endKey], &key)
		vs := skipSpace(body, skipSpace(body, endKey)+1)
		end := skipValue(body, vs)
		order = append(order, body[i:vs])
		if key == sequence || key == "tools" || key == "prompt_cache_key" {
			if _, exists := fields[key]; exists {
				s.view.Structure = "ambiguous-fields"
				return s
			}
			fields[key] = body[vs:end]
		}
		if key != sequence && key != "tools" {
			other = append(other, body[i:end])
		}
		i = skipSpace(body, end)
		if body[i] == ',' {
			i = skipSpace(body, i+1)
		}
	}
	s.view.Structure = "complete"
	s.view.RootOrderTag = a.tag("root-order", order...)
	s.view.OtherFieldsTag = a.tag("other-fields", other...)
	if raw, ok := fields[sequence]; ok {
		s.view.SequenceTag = a.tag("sequence", raw)
		s.view.SequenceCount, s.messages, s.view.SequenceComplete = a.arrayTags(raw, "message", mistralAuditMessages)
	}
	s.view.ToolsComplete = true
	if raw, ok := fields["tools"]; ok {
		s.view.ToolsTag = a.tag("tools", raw)
		s.view.ToolsCount, s.tools, s.view.ToolsComplete = a.arrayTags(raw, "tool", mistralAuditTools)
	}
	if raw, ok := fields["prompt_cache_key"]; ok {
		s.view.CacheKeyTag = a.tag("cache-field", raw)
	}
	return s
}

func fingerprintPrefix(a, b [][16]byte) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}
func fingerprintPermutation(a, b [][16]byte) bool {
	if len(a) != len(b) || fingerprintPrefix(a, b) == len(a) {
		return false
	}
	counts := map[[16]byte]int{}
	for _, t := range a {
		counts[t]++
	}
	for _, t := range b {
		counts[t]--
		if counts[t] < 0 {
			return false
		}
	}
	return true
}
func compareCacheFingerprints(previous, current *cacheFingerprintState) cacheFingerprintComparison {
	p, c := previous.view, current.view
	out := cacheFingerprintComparison{BodyEqual: p.Tag == c.Tag, PreviousSequenceCount: p.SequenceCount, PreviousToolsCount: p.ToolsCount}
	if p.Structure != "complete" || c.Structure != "complete" {
		return out
	}
	root, other, tools, seq := p.RootOrderTag == c.RootOrderTag, p.OtherFieldsTag == c.OtherFieldsTag, p.ToolsTag == c.ToolsTag, p.SequenceTag == c.SequenceTag
	out.RootOrderEqual, out.OtherFieldsEqual, out.ToolsEqual, out.SequenceEqual = &root, &other, &tools, &seq
	if p.SequenceComplete && c.SequenceComplete && p.Sequence == c.Sequence {
		n := fingerprintPrefix(previous.messages, current.messages)
		kept := n == len(previous.messages)
		reordered := fingerprintPermutation(previous.messages, current.messages)
		out.CommonSequencePrefix, out.PreviousSequenceRetained, out.SequenceReordered = &n, &kept, &reordered
	}
	if p.ToolsComplete && c.ToolsComplete {
		n := fingerprintPrefix(previous.tools, current.tools)
		reordered := fingerprintPermutation(previous.tools, current.tools)
		out.CommonToolsPrefix, out.ToolsReordered = &n, &reordered
	}
	return out
}
func previousCacheComparison(b cacheAuditBaseline, current *cacheFingerprintState, prepared bool) *cachePreviousComparison {
	old := b.client
	if prepared {
		old = b.prepared
	}
	if old == nil {
		return nil
	}
	return &cachePreviousComparison{RequestID: b.requestID, AccountID: b.accountID, AttemptIndex: b.attempt, MayOverlap: !b.done.Load(), Comparison: compareCacheFingerprints(old, current)}
}
func (a *mistralCacheAudit) remember(m map[string]cacheAuditBaseline, key string, b cacheAuditBaseline) {
	if key == "" {
		return
	}
	a.order++
	b.order = a.order
	if _, exists := m[key]; !exists && len(m) >= mistralAuditSessions {
		oldest := ""
		order := a.order
		for k, v := range m {
			if v.order < order {
				oldest, order = k, v.order
			}
		}
		delete(m, oldest)
	}
	m[key] = b
}

// The existing maintenance tick releases closed connection references even if
// the opted-in key sends no further requests after the sampling deadline.
func (a *mistralCacheAudit) expire(now time.Time) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !now.Before(a.expires) {
		clear(a.history)
		clear(a.wire)
		clear(a.connections)
	}
}

func (a *mistralCacheAudit) begin(rec *requestLog, ident Identity, client, prepared []byte, now time.Time) *mistralAuditRequest {
	if a == nil || rec.Provider != config.ProviderMistral || rec.ClientKeyID != a.keyID || rec.Path != "/v1/chat/completions" && rec.Path != "/v1/responses" {
		return nil
	}
	a.mu.Lock()
	if !now.Before(a.expires) {
		clear(a.history)
		clear(a.wire)
		clear(a.connections)
		a.mu.Unlock()
		return nil
	}
	if a.samples >= mistralAuditRequests {
		a.mu.Unlock()
		return nil
	}
	a.samples++
	sample := a.samples
	a.mu.Unlock()
	sequence := "messages"
	if rec.Path == "/v1/responses" {
		sequence = "input"
	}
	input := a.fingerprint(client, sequence, nil)
	output := a.fingerprint(prepared, sequence, input)
	r := &mistralAuditRequest{audit: a, prepared: output, requestID: rec.RequestID, done: new(atomic.Bool)}
	r.log = &mistralCacheAuditLog{Version: 1, RunID: a.runID, Sample: sample, SampleLimit: mistralAuditRequests, ExpiresAt: a.expires, Client: input.view, Prepared: output.view, Preparation: compareCacheFingerprints(input, output), Attempts: []cacheAuditAttemptLog{}}
	if ident.Fingerprint != "" {
		r.log.SessionTag = a.tag("identity", []byte(rec.ClientKeyID), []byte(ident.Fingerprint))
		r.historyKey = a.tag("history", []byte(r.log.SessionTag), []byte(rec.Model), []byte(rec.Path))
		a.mu.Lock()
		b := a.history[r.historyKey]
		r.log.PreviousClient = previousCacheComparison(b, input, false)
		r.log.PreviousPrepared = previousCacheComparison(b, output, true)
		a.remember(a.history, r.historyKey, cacheAuditBaseline{requestID: rec.RequestID, done: r.done, client: input, prepared: output})
		a.mu.Unlock()
	}
	rec.CacheAudit = r.log
	return r
}

func (a *mistralCacheAudit) connectionID(conn net.Conn) string {
	if conn == nil || !reflect.TypeOf(conn).Comparable() {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if id := a.connections[conn]; id != "" {
		return id
	}
	if len(a.connections) >= mistralAuditConnections {
		return ""
	}
	var n [8]byte
	a.nextConnection++
	binary.BigEndian.PutUint64(n[:], a.nextConnection)
	id := a.tag("connection", n[:])
	a.connections[conn] = id
	return id
}
