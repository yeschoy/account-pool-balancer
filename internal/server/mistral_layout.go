package server

// Server-only replay metadata. These records contain lengths and fixed schema
// enums, never prompt/answer/thought/tool text, credentials or raw identities.
import (
	"bytes"
	"container/list"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"account-pool-balancer/internal/config"
)

const (
	mistralLayoutEntries     = 8192
	mistralLayoutBytes       = 16 << 20
	mistralLayoutRecordBytes = 32 << 10
	mistralLayoutParts       = 512
	mistralLayoutTTL         = 24 * time.Hour
)

var errMistralLayout = errors.New("cannot replay saved Mistral history layout")

type mistralLayoutBlock struct {
	Kind      string `json:"kind"` // text | thinking
	Bytes     int    `json:"bytes,omitempty"`
	Parts     []int  `json:"parts,omitempty"`
	Closed    int    `json:"closed,omitempty"`    // 0 absent, 1 false, 2 true, 3 null
	Signature int    `json:"signature,omitempty"` // 0 absent, 1 null, 2 empty
}

type mistralLayoutRecord struct {
	Version int                  `json:"version"`
	Key     string               `json:"key"`             // domain-separated scoped HMAC, not routing HMAC
	State   string               `json:"state,omitempty"` // unknown | unavailable | ambiguous
	Blocks  []mistralLayoutBlock `json:"blocks,omitempty"`
	Expires time.Time            `json:"expiresAt"`
	MAC     string               `json:"mac,omitempty"`
}

type mistralLayoutLog struct {
	Restored           int  `json:"restored"`
	Unknown            int  `json:"legacyUnknown"`
	Native             int  `json:"native"`
	Captured           int  `json:"captured"`
	CaptureUnavailable int  `json:"captureUnavailable"`
	Unrecovered        int  `json:"unrecovered,omitempty"` // known missing descriptor, old compatibility used
	Rejected           bool `json:"rejected,omitempty"`    // historical logs only
	Fallback           int  `json:"fallback,omitempty"`    // prepared ordering used instead of exact replay; admin only
}

func (l *mistralLayoutLog) header() string {
	return fmt.Sprintf("restored=%d; legacy_unknown=%d; native=%d; unrecovered=%d", l.Restored, l.Unknown, l.Native, l.Unrecovered)
}

type mistralLayouts struct {
	mu       sync.Mutex
	key      []byte
	entries  map[string]*list.Element
	lru      list.List
	bytes    int
	changed  bool
	maxCount int
	maxBytes int
	now      func() time.Time
}

func newMistralLayouts(secret []byte) *mistralLayouts {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte("account-pool-balancer/mistral-layout/v1"))
	return &mistralLayouts{key: h.Sum(nil), entries: map[string]*list.Element{}, maxCount: mistralLayoutEntries, maxBytes: mistralLayoutBytes, now: time.Now}
}

func (s *mistralLayouts) tag(parts ...string) string {
	h := hmac.New(sha256.New, s.key)
	for _, part := range parts {
		h.Write([]byte(strconv.Itoa(len(part))))
		h.Write([]byte{':'})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *mistralLayouts) scope(cfg *config.Config, ck config.ClientKey, ident Identity, model, path string) string {
	if s == nil || ident.Fingerprint == "" || path != "/v1/chat/completions" || !mistralReasoningModel(model) {
		return ""
	}
	return s.tag("scope", ck.Group, ck.ID, ck.KeyHash, ident.Fingerprint, model, cfg.Mistral.BaseURL)
}

func (s *mistralLayouts) textKey(scope, answer, thought string) string {
	a := hmac.New(sha256.New, s.key)
	r := hmac.New(sha256.New, s.key)
	a.Write([]byte(answer))
	r.Write([]byte(thought))
	return s.tag("reply", scope, hex.EncodeToString(a.Sum(nil)), hex.EncodeToString(r.Sum(nil)))
}

func cloneMistralLayout(r mistralLayoutRecord) mistralLayoutRecord {
	r.Blocks = append([]mistralLayoutBlock(nil), r.Blocks...)
	for i := range r.Blocks {
		r.Blocks[i].Parts = append([]int(nil), r.Blocks[i].Parts...)
	}
	return r
}

func mistralLayoutCost(r mistralLayoutRecord) int {
	b, _ := json.Marshal(r)
	return len(b) + 128 // conservatively budget fixed map/list bookkeeping too
}

func (s *mistralLayouts) put(r mistralLayoutRecord) {
	r.Version, r.MAC = 1, ""
	r.Expires = s.now().Add(mistralLayoutTTL)
	r = cloneMistralLayout(r)
	if mistralLayoutCost(r) > mistralLayoutRecordBytes {
		r.State, r.Blocks = "unavailable", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if el := s.entries[r.Key]; el != nil {
		old := el.Value.(mistralLayoutRecord)
		// Identical visible strings do not identify a layout when two native
		// layouts differ. Never select whichever response happened last.
		if old.State == "ambiguous" || old.State != "unavailable" && !sameMistralLayout(old, r) || old.State == "unavailable" && r.State != "" && r.State != "unavailable" {
			r.State, r.Blocks = "ambiguous", nil
		}
		s.bytes -= mistralLayoutCost(old)
		el.Value = r
		s.bytes += mistralLayoutCost(r)
		s.lru.MoveToFront(el)
	} else {
		s.entries[r.Key] = s.lru.PushFront(r)
		s.bytes += mistralLayoutCost(r)
	}
	s.changed = true
	for len(s.entries) > s.maxCount || s.bytes > s.maxBytes {
		el := s.lru.Back()
		if el == nil {
			break
		}
		old := el.Value.(mistralLayoutRecord)
		delete(s.entries, old.Key)
		s.bytes -= mistralLayoutCost(old)
		s.lru.Remove(el)
	}
}

func sameMistralLayout(a, b mistralLayoutRecord) bool {
	a.Key, a.Expires, a.MAC = "", time.Time{}, ""
	b.Key, b.Expires, b.MAC = "", time.Time{}, ""
	a.Blocks, b.Blocks = mistralReplayBlocks(a.Blocks), mistralReplayBlocks(b.Blocks)
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func (s *mistralLayouts) get(key string) (mistralLayoutRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el := s.entries[key]
	if el == nil {
		return mistralLayoutRecord{}, false, nil
	}
	r := el.Value.(mistralLayoutRecord)
	if r.State == "unknown" || r.State == "unavailable" {
		return r, false, nil // no descriptor: explicitly use the old compatibility path
	}
	if !s.now().Before(r.Expires) || r.State == "ambiguous" {
		return mistralLayoutRecord{}, true, errMistralLayout
	}
	return r, true, nil // blocks are immutable once put/imported
}

// Renew only after an exact render, never on fallback. Do not overwrite a
// concurrently recaptured (possibly now ambiguous) record.
func (s *mistralLayouts) refresh(used mistralLayoutRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el := s.entries[used.Key]
	if el == nil {
		return
	}
	r := el.Value.(mistralLayoutRecord)
	now := s.now()
	if r.State != "" || !r.Expires.Equal(used.Expires) || !now.Before(r.Expires) {
		return
	}
	r.Expires = now.Add(mistralLayoutTTL)
	el.Value = r
	s.lru.MoveToFront(el)
	s.changed = true
}

func (s *mistralLayouts) dirty() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

func (s *mistralLayouts) recordMAC(r mistralLayoutRecord) string {
	r.MAC = ""
	b, _ := json.Marshal(r)
	return s.tag("checkpoint", string(b))
}

func (s *mistralLayouts) export() []mistralLayoutRecord {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]mistralLayoutRecord, 0, len(s.entries))
	for el := s.lru.Back(); el != nil; el = el.Prev() {
		r := cloneMistralLayout(el.Value.(mistralLayoutRecord))
		r.MAC = s.recordMAC(r)
		out = append(out, r)
	}
	s.changed = false
	return out
}

func validMistralLayout(r mistralLayoutRecord) bool {
	key, err := hex.DecodeString(r.Key)
	if r.Version != 1 || err != nil || len(key) != 32 || r.Expires.IsZero() || mistralLayoutCost(r) > mistralLayoutRecordBytes {
		return false
	}
	if r.State != "" {
		return (r.State == "unknown" || r.State == "unavailable" || r.State == "ambiguous") && len(r.Blocks) == 0
	}
	nodes, total := 0, int64(0)
	for _, b := range r.Blocks {
		nodes++
		switch b.Kind {
		case "text":
			if b.Bytes < 0 || b.Bytes > 512<<20 || len(b.Parts) != 0 || b.Closed != 0 || b.Signature != 0 {
				return false
			}
			total += int64(b.Bytes)
		case "thinking":
			if b.Bytes != 0 || b.Closed < 0 || b.Closed > 3 || b.Signature < 0 || b.Signature > 2 {
				return false
			}
			for _, n := range b.Parts {
				if n < 0 || n > 512<<20 {
					return false
				}
				total += int64(n)
				nodes++
			}
		default:
			return false
		}
	}
	return nodes <= mistralLayoutParts && total <= 512<<20
}

func (s *mistralLayouts) importRecords(records []mistralLayoutRecord) error {
	if len(records) > s.maxCount {
		return errors.New("metadata.json: Mistral layout checkpoint exceeds capacity")
	}
	for _, r := range records {
		if !validMistralLayout(r) || !hmac.Equal([]byte(r.MAC), []byte(s.recordMAC(r))) || s.entries[r.Key] != nil {
			return errors.New("metadata.json: invalid Mistral layout checkpoint")
		}
		r = cloneMistralLayout(r)
		r.MAC = ""
		s.bytes += mistralLayoutCost(r)
		if s.bytes > s.maxBytes {
			return errors.New("metadata.json: Mistral layout checkpoint exceeds byte budget")
		}
		s.entries[r.Key] = s.lru.PushFront(r)
	}
	return nil
}

// Rebuild only trustworthy exact matches. Otherwise keep the already prepared
// assistant content (thinking first for flattened history), without recording
// it as original. Only the upstream response capture creates new layouts.
func (s *mistralLayouts) restore(original, prepared []byte, scope string, log *mistralLayoutLog) ([]byte, error) {
	if scope == "" {
		return prepared, nil
	}
	oldRoot, root := scanMistralObject(original), scanMistralObject(prepared)
	if oldRoot == nil || root == nil {
		return prepared, nil
	}
	field, ok := root.field("messages")
	if !ok || len(field.value) == 0 || field.value[0] != '[' {
		return prepared, nil
	}
	raw := field.value
	// Walk the original history by offset instead of decoding it into a
	// []json.RawMessage: the previous form copied the whole message array just
	// to pair item indexes. Preparation never adds, removes or reorders
	// messages, so positional pairing is unchanged.
	oldRaw := oldRoot.get("messages")
	if len(oldRaw) == 0 || oldRaw[0] != '[' {
		return prepared, nil
	}
	// A duplicate root messages key is ambiguous; leave such a request entirely
	// as preparation left it rather than picking one occurrence to rewrite.
	if countMistralField(root, "messages") != 1 {
		return prepared, nil
	}
	var out bytes.Buffer
	started, changed, mi := false, false, 0
	last := 0
	oi := skipSpace(oldRaw, 1)
	for i := skipSpace(raw, 1); raw[i] != ']'; mi++ {
		end := skipValue(raw, i)
		if end < 0 {
			return prepared, nil
		}
		var old *mistralWireObject
		if oi < len(oldRaw) && oldRaw[oi] != ']' {
			oend := skipValue(oldRaw, oi)
			if oend < 0 {
				return prepared, nil
			}
			old = scanMistralObject(oldRaw[oi:oend])
			oi = skipSpace(oldRaw, oend)
			if oi < len(oldRaw) && oldRaw[oi] == ',' {
				oi = skipSpace(oldRaw, oi+1)
			}
		}
		m := scanMistralObject(raw[i:end])
		if m != nil && old != nil && mistralString(m.get("role")) == "assistant" {
			if err := m.unique("content"); err != nil {
				return nil, err
			}
			contentField, _ := m.field("content")
			if mistralHasNativeThinking(old.get("content")) {
				log.Native++
			} else if answer, thought, ok := mistralLayoutPlain(m.get("content")); ok {
				r, found, err := s.get(s.textKey(scope, answer, thought))
				var content []byte
				if found && err == nil {
					content, err = r.render(answer, thought)
				}
				switch {
				case errors.Is(err, errMistralLayout):
					log.Fallback++ // ambiguous, expired or cannot be rendered exactly
				case err != nil:
					return nil, err
				case !found:
					log.Unknown++
					if r.State == "unavailable" {
						log.Unrecovered++
					}
					if thought != "" || r.State == "unavailable" {
						log.Fallback++
					}
				default:
					s.refresh(r)
					// Splice only the content value into the prepared message and
					// write the whole body once; rendering the message and then the
					// root object again would copy the history twice.
					if !started {
						out.Grow(len(prepared) + len(prepared)/8 + 1024)
						out.Write(prepared[:field.vs])
						out.Write(raw[:i])
						started = true
					} else {
						out.Write(raw[last:i])
					}
					out.Write(raw[i : i+contentField.vs])
					out.Write(content)
					out.Write(raw[i+contentField.end : end])
					last, changed = end, true
					log.Restored++
				}
			} else {
				log.Unknown++ // never discard real citations/images/unknown content
			}
		}
		i = skipSpace(raw, end)
		if raw[i] == ',' {
			i = skipSpace(raw, i+1)
		}
	}
	if !changed {
		return prepared, nil
	}
	out.Write(raw[last:])
	out.Write(prepared[field.end:])
	return out.Bytes(), nil
}

func mistralHasNativeThinking(raw []byte) bool {
	if len(raw) == 0 || raw[0] != '[' {
		return false
	}
	for i := skipSpace(raw, 1); i < len(raw) && raw[i] != ']'; {
		end := skipValue(raw, i)
		if end < 0 {
			return false
		}
		if o := scanMistralObject(raw[i:end]); o != nil && mistralString(o.get("type")) == "thinking" {
			return true
		}
		i = skipSpace(raw, end)
		if i < len(raw) && raw[i] == ',' {
			i = skipSpace(raw, i+1)
		}
	}
	return false
}

func mistralLayoutPlain(raw []byte) (string, string, bool) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", "", true
	}
	if raw[0] == '"' {
		var s string
		err := json.Unmarshal(raw, &s)
		return s, "", err == nil
	}
	if raw[0] != '[' {
		return "", "", false
	}
	var a, r bytes.Buffer
	// Walk the content array by offsets: decoding it into maps would allocate
	// a second copy of every chunk plus a map per chunk for the same answer.
	for i := skipSpace(raw, 1); i < len(raw) && raw[i] != ']'; {
		end := skipValue(raw, i)
		if end < 0 {
			return "", "", false
		}
		chunk := scanMistralObject(raw[i:end])
		if chunk == nil || chunk.unique("type", "text", "thinking", "closed", "signature") != nil {
			return "", "", false
		}
		switch mistralString(chunk.get("type")) {
		case "text":
			text, err := mistralTextChunkView(chunk)
			if err != nil {
				return "", "", false
			}
			a.WriteString(text)
		case "thinking":
			parts, err := mistralThinkingPartsView(chunk)
			if err != nil {
				return "", "", false
			}
			for _, text := range parts {
				r.WriteString(text)
			}
		default:
			return "", "", false
		}
		i = skipSpace(raw, end)
		if i < len(raw) && raw[i] == ',' {
			i = skipSpace(raw, i+1)
		}
	}
	return a.String(), r.String(), true
}

// mistralTextChunkView mirrors mistralTextChunk (the response adapter's parser)
// on a wire view: only type/text, no null text, and the last duplicate wins as
// a map decode would.
func mistralTextChunkView(o *mistralWireObject) (string, error) {
	for _, f := range o.fields {
		if f.key != "type" && f.key != "text" {
			return "", errMistralContent
		}
	}
	return mistralTextFieldsView(o)
}

func mistralTextFieldsView(o *mistralWireObject) (string, error) {
	var typ, text string
	rawText := o.get("text")
	if json.Unmarshal(o.get("type"), &typ) != nil || typ != "text" || json.Unmarshal(rawText, &text) != nil || bytes.Equal(bytes.TrimSpace(rawText), []byte("null")) {
		return "", errMistralContent
	}
	return text, nil
}

// mistralThinkingPartsView mirrors mistralThinkingTextParts on a wire view.
func mistralThinkingPartsView(o *mistralWireObject) ([]string, error) {
	for _, f := range o.fields {
		switch f.key {
		case "type", "thinking", "closed":
		case "signature":
			if !bytes.Equal(bytes.TrimSpace(f.value), []byte("null")) && !bytes.Equal(bytes.TrimSpace(f.value), []byte(`""`)) {
				return nil, errMistralContent
			}
		default:
			return nil, errMistralContent
		}
	}
	arr := o.get("thinking")
	if len(arr) == 0 || arr[0] != '[' {
		return nil, errMistralContent
	}
	parts := make([]string, 0, 4)
	for i := skipSpace(arr, 1); i < len(arr) && arr[i] != ']'; {
		end := skipValue(arr, i)
		if end < 0 {
			return nil, errMistralContent
		}
		part := scanMistralObject(arr[i:end])
		if part == nil || part.unique("type", "text") != nil {
			return nil, errMistralContent
		}
		text, err := mistralTextChunkView(part)
		if err != nil {
			return nil, err
		}
		parts = append(parts, text)
		i = skipSpace(arr, end)
		if i < len(arr) && arr[i] == ',' {
			i = skipSpace(arr, i+1)
		}
	}
	return parts, nil
}

// Response transport fragments (even closed=true) are not distinct request
// ThinkChunks. Collapse adjacent thought runs without crossing real text or
// changing any thought bytes; also repairs checkpoints from the first release.
func mistralReplayBlocks(blocks []mistralLayoutBlock) []mistralLayoutBlock {
	out := make([]mistralLayoutBlock, 0, len(blocks))
	for _, b := range blocks {
		b.Parts = append([]int(nil), b.Parts...)
		if b.Kind == "thinking" && len(out) > 0 && out[len(out)-1].Kind == "thinking" {
			last := &out[len(out)-1]
			for i, n := range b.Parts {
				if i == 0 && len(last.Parts) > 0 {
					last.Parts[len(last.Parts)-1] += n
				} else {
					last.Parts = append(last.Parts, n)
				}
			}
			if b.Closed != 0 {
				last.Closed = b.Closed
			}
			if b.Signature != 0 {
				last.Signature = b.Signature
			}
			continue
		}
		out = append(out, b)
	}
	return out
}

func (r mistralLayoutRecord) render(answer, thought string) ([]byte, error) {
	take := func(text *string, n int) (string, error) {
		if n < 0 || n > len(*text) || !utf8.ValidString((*text)[:n]) {
			return "", errMistralLayout
		}
		part := (*text)[:n]
		*text = (*text)[n:]
		return part, nil
	}
	blocks := mistralReplayBlocks(r.Blocks)
	// Assemble the content array directly. Every chunk below is already compact
	// JSON built here, so encoding through []json.RawMessage would only
	// re-validate and re-compact the text a second time.
	var out bytes.Buffer
	out.Grow(totalMistralLayoutBytes(blocks) + 2)
	out.WriteByte('[')
	for bi, b := range blocks {
		if bi > 0 {
			out.WriteByte(',')
		}
		if b.Kind == "text" {
			text, err := take(&answer, b.Bytes)
			if err != nil {
				return nil, err
			}
			s, _ := json.Marshal(text)
			out.WriteString(`{"type":"text","text":`)
			out.Write(s)
			out.WriteByte('}')
			continue
		}
		out.WriteString(`{"type":"thinking","thinking":[`)
		for i, n := range b.Parts {
			if i > 0 {
				out.WriteByte(',')
			}
			text, err := take(&thought, n)
			if err != nil {
				return nil, err
			}
			s, _ := json.Marshal(text)
			out.WriteString(`{"type":"text","text":`)
			out.Write(s)
			out.WriteByte('}')
		}
		out.WriteByte(']')
		if b.Closed != 0 {
			out.WriteString(`,"closed":`)
			out.WriteString([]string{"", "false", "true", "null"}[b.Closed])
		}
		if b.Signature != 0 {
			out.WriteString(`,"signature":`)
			out.WriteString([]string{"", "null", `""`}[b.Signature])
		}
		out.WriteByte('}')
	}
	out.WriteByte(']')
	if answer != "" || thought != "" {
		return nil, errMistralLayout
	}
	return out.Bytes(), nil
}

// totalMistralLayoutBytes estimates the rendered content size so the buffer is
// allocated once; JSON escaping can grow text, so the estimate is generous.
func totalMistralLayoutBytes(blocks []mistralLayoutBlock) int {
	n := 0
	for _, b := range blocks {
		if b.Kind == "text" {
			n += b.Bytes + 32
			continue
		}
		n += 48
		for _, part := range b.Parts {
			n += part + 32
		}
	}
	return n
}
