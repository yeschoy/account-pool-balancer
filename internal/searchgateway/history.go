package searchgateway

import (
	"bytes"
	"container/list"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
	"time"
)

type historyEntry struct {
	id, scope            string
	public, call, result json.RawMessage
	expires              time.Time
	size                 int
}
type historyStore struct {
	mu                          sync.Mutex
	items                       map[string]*list.Element
	lru                         *list.List
	bytes, maxBytes, maxEntries int
	ttl                         time.Duration
	secret                      [32]byte
	now                         func() time.Time
}

func newHistory(c Config) *historyStore {
	h := &historyStore{items: map[string]*list.Element{}, lru: list.New(), maxBytes: c.HistoryMaxBytes, maxEntries: c.HistoryMaxEntries, ttl: time.Duration(c.HistoryTTLSeconds) * time.Second, now: time.Now}
	if _, e := rand.Read(h.secret[:]); e != nil {
		panic(e)
	}
	return h
}
func id(prefix string) string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return prefix + hex.EncodeToString(b[:])
}
func (h *historyStore) scope(route Route, r *http.Request, o object) string {
	m := hmac.New(sha256.New, h.secret[:])
	m.Write([]byte(route.ID + "\x00" + route.token + "\x00" + str(o["model"]) + "\x00"))
	for _, k := range []string{"X-Session-Id", "Session-Id", "X-Codex-Session-Id", "Thread-Id", "X-Pi-Session-Id"} {
		if v := r.Header.Get(k); v != "" && len(v) <= 512 {
			m.Write([]byte(k + "\x00" + v))
			return hex.EncodeToString(m.Sum(nil))
		}
	}
	// A caller without explicit identity gets a conservative first-input scope.
	// Changed first-input formatting intentionally fails closed, never cross-replays.
	raw := o["input"]
	if a, e := array(raw); e == nil && len(a) > 0 {
		raw = a[0]
	}
	var compact json.RawMessage
	_ = json.Unmarshal(raw, &compact)
	m.Write(compact)
	return hex.EncodeToString(m.Sum(nil))
}
func (h *historyStore) remove(e *list.Element) {
	v := e.Value.(historyEntry)
	delete(h.items, v.id)
	h.bytes -= v.size
	h.lru.Remove(e)
}
func (h *historyStore) put(v historyEntry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	v.size = len(v.public) + len(v.call) + len(v.result) + len(v.scope) + len(v.id)
	if v.size > h.maxBytes {
		return &gatewayError{503, "search_history_limit", "Search history capacity exceeded"}
	}
	v.expires = h.now().Add(h.ttl)
	if old := h.items[v.id]; old != nil {
		h.remove(old)
	}
	for h.lru.Len() > 0 && (h.bytes+v.size > h.maxBytes || h.lru.Len() >= h.maxEntries) {
		h.remove(h.lru.Back())
	}
	h.items[v.id] = h.lru.PushFront(v)
	h.bytes += v.size
	return nil
}
func (h *historyStore) get(key, scope string, public json.RawMessage) (historyEntry, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	miss := &gatewayError{409, "search_history_unavailable", "Search history is missing, expired, changed or belongs to another scope; start a new conversation"}
	e := h.items[key]
	if e == nil {
		return historyEntry{}, miss
	}
	v := e.Value.(historyEntry)
	if !h.now().Before(v.expires) {
		h.remove(e)
		return historyEntry{}, miss
	}
	// Codex legitimately drops action.sources and serializes optional metadata as
	// null. Compare authority-bearing fields, never require unsupported carriers.
	if !matchingSearchHistory(public, v.public) || !hmac.Equal([]byte(v.scope), []byte(scope)) {
		return historyEntry{}, miss
	}
	h.lru.MoveToFront(e)
	return v, nil
}

func matchingSearchHistory(got, want json.RawMessage) bool {
	a, err := decodeObject(got)
	if err != nil {
		return false
	}
	b, err := decodeObject(want)
	if err != nil {
		return false
	}
	for k, v := range a {
		switch k {
		case "id", "type", "status", "action":
		case "encrypted_content", "internal_chat_message_metadata_passthrough":
			if !bytes.Equal(v, []byte("null")) {
				return false
			}
			delete(a, k)
		default:
			return false
		}
	}
	for _, k := range []string{"id", "type", "status"} {
		if str(a[k]) != str(b[k]) || a[k] == nil {
			return false
		}
	}
	x, err := decodeObject(a["action"])
	if err != nil {
		return false
	}
	y, err := decodeObject(b["action"])
	if err != nil {
		return false
	}
	for k, v := range x {
		switch k {
		case "type", "query", "queries":
		case "sources":
			if !jsonEqual(v, y[k]) {
				return false
			}
			delete(x, k)
		default:
			return false
		}
	}
	delete(y, "sources")
	// Both single-query and queries-array spellings must agree if supplied.
	q := str(y["query"])
	if str(x["type"]) != "search" {
		return false
	}
	if raw := x["queries"]; raw != nil && !bytes.Equal(raw, []byte("null")) {
		var queries []string
		if json.Unmarshal(raw, &queries) != nil || len(queries) != 1 || queries[0] != q {
			return false
		}
	} else if str(x["query"]) != q {
		return false
	}
	if raw := x["query"]; raw != nil && !bytes.Equal(raw, []byte("null")) && str(raw) != q {
		return false
	}
	return true
}
func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	da := json.NewDecoder(bytes.NewReader(a))
	da.UseNumber()
	db := json.NewDecoder(bytes.NewReader(b))
	db.UseNumber()
	return da.Decode(&x) == nil && db.Decode(&y) == nil && reflect.DeepEqual(x, y)
}
