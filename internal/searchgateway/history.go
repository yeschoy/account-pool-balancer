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

type historyEntry struct {id,scope string; public,call,result json.RawMessage;expires time.Time;size int}
type historyStore struct {mu sync.Mutex;items map[string]*list.Element;lru *list.List;bytes,maxBytes,maxEntries int;ttl time.Duration;secret [32]byte; now func()time.Time}
func newHistory(c Config)*historyStore {h:=&historyStore{items:map[string]*list.Element{},lru:list.New(),maxBytes:c.HistoryMaxBytes,maxEntries:c.HistoryMaxEntries,ttl:time.Duration(c.HistoryTTLSeconds)*time.Second,now:time.Now};if _,e:=rand.Read(h.secret[:]);e!=nil{panic(e)};return h}
func id(prefix string)string {var b [16]byte;if _,e:=rand.Read(b[:]);e!=nil{panic(e)};return prefix+hex.EncodeToString(b[:])}
func(h *historyStore)scope(route Route,r *http.Request,o object)string {
 m:=hmac.New(sha256.New,h.secret[:]);m.Write([]byte(route.ID+"\x00"+route.token+"\x00"))
 for _,k:=range []string{"X-Session-Id","Session-Id","X-Codex-Session-Id","Thread-Id","X-Pi-Session-Id"} {if v:=r.Header.Get(k);v!="" && len(v)<=512 {m.Write([]byte(k+"\x00"+v));return hex.EncodeToString(m.Sum(nil))}}
 // A caller without explicit identity gets a conservative first-input scope.
 // Changed first-input formatting intentionally fails closed, never cross-replays.
 raw:=o["input"];if a,e:=array(raw);e==nil && len(a)>0 {raw=a[0]}
 var compact json.RawMessage;_ = json.Unmarshal(raw,&compact);m.Write(compact)
 return hex.EncodeToString(m.Sum(nil))
}
func(h *historyStore)remove(e *list.Element){v:=e.Value.(historyEntry);delete(h.items,v.id);h.bytes-=v.size;h.lru.Remove(e)}
func(h *historyStore)put(v historyEntry)error {
 h.mu.Lock();defer h.mu.Unlock();v.size=len(v.public)+len(v.call)+len(v.result)+len(v.scope)+len(v.id)
 if v.size>h.maxBytes {return &gatewayError{503,"search_history_limit","Search history capacity exceeded"}}
 v.expires=h.now().Add(h.ttl)
 if old:=h.items[v.id];old!=nil{h.remove(old)}
 for h.lru.Len()>0 && (h.bytes+v.size>h.maxBytes || h.lru.Len()>=h.maxEntries) {h.remove(h.lru.Back())}
 h.items[v.id]=h.lru.PushFront(v);h.bytes+=v.size;return nil
}
func(h *historyStore)get(key,scope string,public json.RawMessage)(historyEntry,error){
 h.mu.Lock();defer h.mu.Unlock()
 miss:=&gatewayError{409,"search_history_unavailable","Search history is missing, expired, changed or belongs to another scope; start a new conversation"}
 e:=h.items[key];if e==nil{return historyEntry{},miss};v:=e.Value.(historyEntry)
 if !h.now().Before(v.expires){h.remove(e);return historyEntry{},miss}
 // Structural equality tolerates normal JSON property ordering, not edits.
 var a,b any;da:=json.NewDecoder(bytes.NewReader(public));da.UseNumber();db:=json.NewDecoder(bytes.NewReader(v.public));db.UseNumber()
 if da.Decode(&a)!=nil || db.Decode(&b)!=nil || !reflect.DeepEqual(a,b) || !hmac.Equal([]byte(v.scope),[]byte(scope)){return historyEntry{},miss}
 h.lru.MoveToFront(e);return v,nil
}
