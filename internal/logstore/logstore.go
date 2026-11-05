// Package logstore is the JSONL request and error log: segment files under
// DATA_DIR/logs (5 MiB each, 0600) plus an in-memory ring of the newest
// records for queries. Writes go through one async queue and fail open: a
// record that can't be written is dropped and counted, model traffic never
// waits for the disk.
package logstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	segmentBytes = 5 << 20
	maxRecord    = 64 << 10
	// detail records carry bodies, so they may be larger
	maxDetailRecord = 32 << 20
	// the details ring holds whole bodies in memory: bound it by size, not just count
	detailRingBytes = 256 << 20
	// queries read the in-memory ring, so the files may trail it this long
	flushEvery = time.Second
)

type Limits struct {
	RetentionDays int
	MaxRecords    map[string]int
	MaxTotalBytes int64
}

type record struct {
	seq  int64
	ts   time.Time
	data json.RawMessage
	keys map[string]string
	// seg is the segment the record was written to; 0 means it predates the
	// on-disk pass or was never persisted by this process.
	seg int64
}

type kind struct {
	name    string
	filters []string
	mu      sync.RWMutex
	ring    []record
	head    int // next write position once full
	seq     int64
	// current is the newest segment number, stamped on live records so a
	// rewrite can find them; the writer sets it as the segment really opens.
	current int64
	file    *os.File
	w       *bufio.Writer // over file; flushed every flushEvery
	size    int64
}

// push inserts r and stamps it with the current segment; caller holds k.mu.
func (k *kind) push(r record, max int) {
	k.seq++
	r.seq = k.seq
	r.seg = k.current
	if len(k.ring) > max {
		// the limit shrank: keep the newest max
		k.ring = k.ordered()[len(k.ring)-max:]
		k.head = 0
	}
	if len(k.ring) < max {
		k.ring = append(k.ring, r)
	} else {
		k.ring[k.head] = r
		k.head = (k.head + 1) % max
	}
	if k.name == "details" {
		k.trimBytes(detailRingBytes)
	}
}

// trimBytes drops the oldest records while the ring holds more than budget
// bytes, always keeping the newest (caller holds the lock).
func (k *kind) trimBytes(budget int) {
	total := 0
	for i := range k.ring {
		total += len(k.ring[i].data)
	}
	if total <= budget {
		return
	}
	all := k.ordered()
	drop := 0
	for total > budget && drop < len(all)-1 {
		total -= len(all[drop].data)
		drop++
	}
	k.ring, k.head = append([]record(nil), all[drop:]...), 0
}

// ordered is the ring oldest first (caller holds the lock).
func (k *kind) ordered() []record {
	if k.head == 0 {
		return append([]record(nil), k.ring...)
	}
	return append(append([]record(nil), k.ring[k.head:]...), k.ring[:k.head]...)
}

type job struct {
	k     *kind
	line  []byte
	clear bool
	// del runs on the writer goroutine so a rewrite cannot race an append.
	del  *deleteJob
	done chan struct{}
}

// deleteJob removes one record from the segment named by seg: lines stamped
// after seq were appended later, and a line holding want is the record itself.
type deleteJob struct {
	seg  int64
	seq  int64
	want string
	out  Removed
	err  error
}

type Store struct {
	dir     string
	limits  func() Limits
	kinds   map[string]*kind
	ch      chan job
	dropped atomic.Int64
	ready   atomic.Bool
	closed  chan struct{}
}

// Filters are the query parameters each kind accepts besides limit/cursor/since/until.
var Filters = map[string][]string{
	"requests": {"provider", "requestId", "accountId", "status", "result", "model", "errorCategory", "bindingResult", "clientKeyId", "stream", "protocol", "affinityKeyType", "affinityConfidence", "clientFamily", "cacheAffinityResult"},
	"errors":   {"provider", "requestId", "accountId", "status", "category", "ruleId", "model", "clientKeyId", "origin"},
	"details":  {"provider", "requestId", "accountId", "status", "model", "protocol", "clientKeyId"},
	// perch is the Perch pool's own event log: imports, renewals, session
	// expiry, status changes and usage reads. It carries no token: the
	// records are written by internal/server/perch_log.go and only name
	// accounts and outcomes.
	"perch": {"provider", "accountId", "event", "userId", "outcome", "kind"},
	// tokenharbor is the Token Harbor pool's registration log: one line per
	// stage per mailbox. Written by internal/server/tokenharbor_log.go; no
	// record carries a password, a key or a session cookie.
	"tokenharbor": {"provider", "email", "event", "kind", "outcome"},
}

func recordLimit(kindName string) int {
	if kindName == "details" {
		return maxDetailRecord
	}
	return maxRecord
}

func Open(dir string, limits func() Limits) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, limits: limits, kinds: map[string]*kind{}, ch: make(chan job, 8192), closed: make(chan struct{})}
	for name, f := range Filters {
		s.kinds[name] = &kind{name: name, filters: f}
	}
	go s.writer()
	go s.recover()
	return s, nil
}

func (s *Store) Dropped() int64 { return s.dropped.Load() }
func (s *Store) Ready() bool    { return s.ready.Load() }

func keyString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func extractKeys(data []byte, filters []string) (map[string]string, time.Time, bool) {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return nil, time.Time{}, false
	}
	keys := map[string]string{}
	for _, f := range filters {
		if s, ok := keyString(m[f]); ok {
			keys[f] = s
		}
	}
	ts, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(m["ts"]))
	return keys, ts, true
}

// Keyed is a record that gives its filter keys itself, so Append doesn't
// decode what it just encoded. The keys must equal what extractKeys reads.
type Keyed interface {
	LogKeys() map[string]string
}

// Append queues rec (already a strict projection) for kind.
func (s *Store) Append(kindName string, ts time.Time, rec any) {
	k := s.kinds[kindName]
	if k == nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil || len(data) > recordLimit(kindName) {
		s.dropped.Add(1)
		return
	}
	var keys map[string]string
	if kr, ok := rec.(Keyed); ok {
		keys = kr.LogKeys()
	} else {
		keys, _, _ = extractKeys(data, k.filters)
	}
	lim := s.limits()
	// Open the job under the same lock as the ring insert: the writer may bump
	// k.current between these two steps otherwise, and the record would be
	// stamped with the previous segment.
	j := job{k: k, line: append(data, '\n')}
	k.mu.Lock()
	k.push(record{ts: ts, data: data, keys: keys}, max(lim.MaxRecords[kindName], 1))
	k.mu.Unlock()
	select {
	case s.ch <- j:
	default:
		s.dropped.Add(1)
	}
}

// segmentName names the next segment: name-stamp-<number>. The trailing number
// orders segments, encodes the append index (so a rewrite can tell how far each
// record got in its segment), and ties a live record to its file.
func (s *Store) segmentName(k *kind, num int64) string {
	return filepath.Join(s.dir, fmt.Sprintf("%s-%s-%d.jsonl", k.name, time.Now().UTC().Format("20060102-150405"), num))
}

// segmentIndex reads the trailing number of a segment file, -1 if unreadable.
func segmentIndex(name string) int64 {
	base := strings.TrimSuffix(name, ".jsonl")
	_, num, ok := strings.Cut(base, "-")
	for ok {
		if _, rest, more := strings.Cut(num, "-"); more {
			num, ok = rest, true
			continue
		}
		break
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

func (s *Store) writer() {
	enforce := time.NewTicker(10 * time.Minute)
	defer enforce.Stop()
	flush := time.NewTicker(flushEvery)
	defer flush.Stop()
	for {
		select {
		case j, ok := <-s.ch:
			if !ok {
				for _, k := range s.kinds {
					s.closeFile(k)
				}
				close(s.closed)
				return
			}
			if j.clear {
				s.clearFiles(j.k)
				close(j.done)
				continue
			}
			if j.del != nil {
				// This goroutine is the only appender, so nothing writes to the
				// segment while it is being rewritten.
				j.del.out.Disk, j.del.err = s.dropSegLine(j.k, j.del.seg, j.del.seq, j.del.want)
				close(j.done)
				continue
			}
			if err := s.write(j.k, j.line); err != nil {
				s.dropped.Add(1)
			}
		case <-enforce.C:
			s.enforce()
		case <-flush.C:
			for _, k := range s.kinds {
				if k.w != nil && k.w.Buffered() > 0 && k.w.Flush() != nil {
					s.dropped.Add(1)
				}
			}
		}
	}
}

// closeFile flushes and closes k's current segment (writer goroutine).
func (s *Store) closeFile(k *kind) {
	if k.file == nil {
		return
	}
	if k.w.Flush() != nil {
		s.dropped.Add(1)
	}
	k.file.Close()
	k.file, k.w = nil, nil
}

func (s *Store) write(k *kind, line []byte) error {
	if k.file != nil && k.size+int64(len(line)) > segmentBytes {
		s.closeFile(k)
		s.enforce()
	}
	if k.file == nil {
		// current is read by Append under k.mu; bump it there too.
		k.mu.Lock()
		k.current++
		num := k.current
		k.mu.Unlock()
		f, err := os.OpenFile(s.segmentName(k, num), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		k.file, k.w, k.size = f, bufio.NewWriterSize(f, 64<<10), 0
	}
	n, err := k.w.Write(line)
	k.size += int64(n)
	return err
}

type segFile struct {
	path string
	kind string
	mod  time.Time
	size int64
}

func (s *Store) segments() []segFile {
	entries, _ := os.ReadDir(s.dir)
	var out []segFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		kindName, _, ok := strings.Cut(name, "-")
		if _, known := s.kinds[kindName]; !ok || !known {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, segFile{path: filepath.Join(s.dir, name), kind: kindName, mod: info.ModTime(), size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

// segIndex caches segmentIndex per file name for one pass (writer goroutine).
type segIndex map[string]int64

func (x segIndex) of(path string) int64 {
	name := filepath.Base(path)
	if n, ok := x[name]; ok {
		return n
	}
	n := segmentIndex(name)
	x[name] = n
	return n
}

func (s *Store) isCurrent(path string) bool {
	for _, k := range s.kinds {
		if k.file != nil && k.file.Name() == path {
			return true
		}
	}
	return false
}

// enforce deletes segments past retention, then the oldest while the total
// is over the cap (runs on the writer goroutine).
func (s *Store) enforce() {
	lim := s.limits()
	segs := s.segments()
	cut := time.Now().Add(-time.Duration(lim.RetentionDays) * 24 * time.Hour)
	var total int64
	var keep []segFile
	for _, f := range segs {
		if f.mod.Before(cut) && !s.isCurrent(f.path) {
			os.Remove(f.path)
			continue
		}
		total += f.size
		keep = append(keep, f)
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].mod.Before(keep[j].mod) })
	// detail segments are large: keep them to half the cap so they can't push the request log out
	var detailTotal int64
	for _, f := range keep {
		if f.kind == "details" {
			detailTotal += f.size
		}
	}
	for i, f := range keep {
		if detailTotal <= lim.MaxTotalBytes/2 {
			break
		}
		if f.kind == "details" && !s.isCurrent(f.path) {
			os.Remove(f.path)
			detailTotal -= f.size
			total -= f.size
			keep[i].path = ""
		}
	}
	for _, f := range keep {
		if total <= lim.MaxTotalBytes {
			break
		}
		if f.path == "" || s.isCurrent(f.path) {
			continue
		}
		os.Remove(f.path)
		total -= f.size
	}
}

// deal streams the lines of one segment that pass keep as JSONL and reports how
// many it wrote. A record that cannot be read back is not one this store can
// serve, so dropping unreadable lines is intentional.
func deal(path string, limit int, keep func(line []byte, idx int64) bool) ([]byte, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var out bytes.Buffer
	n := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), limit+1024)
	for idx := int64(1); sc.Scan(); idx++ {
		line := sc.Bytes()
		if len(line) == 0 || !keep(line, idx) {
			continue
		}
		out.Write(line)
		out.WriteByte('\n')
		n++
	}
	return out.Bytes(), n, sc.Err()
}

func (s *Store) clearFiles(k *kind) {
	s.closeFile(k)
	for _, f := range s.segments() {
		if f.kind == k.name {
			os.Remove(f.path)
		}
	}
}

// recover loads the newest records of each kind from disk in the
// background; queries answer 503 until it is done.
func (s *Store) recover() {
	defer s.ready.Store(true)
	lim := s.limits()
	segs := s.segments()
	for name, k := range s.kinds {
		max := max(lim.MaxRecords[name], 1)
		var loaded []record
		// newest segments first until enough
		for i := len(segs) - 1; i >= 0 && len(loaded) < max; i-- {
			if segs[i].kind != name {
				continue
			}
			var recs []record
			seg := segmentIndex(filepath.Base(segs[i].path)) // stamps the lines for later deletes
			f, err := os.Open(segs[i].path)
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64<<10), recordLimit(name)+1024)
			for sc.Scan() {
				line := append([]byte(nil), sc.Bytes()...)
				keys, ts, ok := extractKeys(line, k.filters)
				if ok {
					recs = append(recs, record{ts: ts, data: line, keys: keys, seg: seg})
				}
			}
			f.Close()
			// The writer numbers new segments above everything on disk, and the
			// rewritten tail describes where this process will next append.
			k.mu.Lock()
			if seg > k.current {
				k.current = seg
			}
			k.mu.Unlock()
			loaded = append(recs, loaded...)
		}
		if len(loaded) > max {
			loaded = loaded[len(loaded)-max:]
		}
		k.mu.Lock()
		// records appended while recovering are newer: keep them after
		live := k.ordered()
		all := append(loaded, live...)
		if len(all) > max {
			all = all[len(all)-max:]
		}
		k.ring, k.head = nil, 0
		for i := range all {
			all[i].seq = int64(i + 1)
		}
		k.ring = all
		k.seq = int64(len(all))
		if len(k.ring) == max {
			k.head = 0
		}
		k.mu.Unlock()
	}
}

var (
	ErrNotReady  = errors.New("logs are still being recovered")
	ErrNotFound  = errors.New("log record not found")
	ErrInvalidID = errors.New("log record id is required")
)

type Page struct {
	Items      []json.RawMessage `json:"items"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// Query returns kind's records newest first. filters were validated by the caller.
func (s *Store) Query(kindName string, filters map[string]string, since, until time.Time, limit int, cursor int64) (Page, error) {
	if !s.ready.Load() {
		return Page{}, ErrNotReady
	}
	k := s.kinds[kindName]
	if k == nil {
		return Page{}, fmt.Errorf("unknown log kind")
	}
	page := Page{Items: []json.RawMessage{}}
	var lastSeq int64
	// walk the ring newest first under the read lock: records are immutable,
	// so nothing is copied (the ring can hold 50k)
	k.mu.RLock()
	defer k.mu.RUnlock()
	n := len(k.ring)
	for i := n - 1; i >= 0; i-- {
		r := &k.ring[(k.head+i)%n]
		if cursor > 0 && r.seq >= cursor {
			continue
		}
		if !since.IsZero() && r.ts.Before(since) {
			continue
		}
		if !until.IsZero() && r.ts.After(until) {
			continue
		}
		ok := true
		for f, v := range filters {
			if r.keys[f] != v {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if len(page.Items) == limit {
			page.NextCursor = strconv.FormatInt(lastSeq, 10)
			break
		}
		page.Items = append(page.Items, r.data)
		lastSeq = r.seq
	}
	return page, nil
}

// Removed reports the outcome of one Remove: the records it dropped from
// memory and the lines it rewrote on disk.
type Removed struct {
	Memory int `json:"memory"`
	Disk   int `json:"disk"`
}

// Remove deletes the record of kind that Query returns for rid, from the live
// ring and from the segment file holding it. It returns ErrNotFound when no
// record matches. A record whose segment was already rotated or trimmed away
// is removed from memory only, and Disk stays 0.
//
// rid is compared by value: the store keeps only filter keys, so it cannot
// prove which JSON member of a record it matched. The API layer checks the
// returned record before exposing the match.
func (s *Store) Remove(kindName, rid string) (Removed, error) {
	if !s.ready.Load() {
		return Removed{}, ErrNotReady
	}
	if rid == "" {
		return Removed{}, ErrInvalidID
	}
	k := s.kinds[kindName]
	if k == nil {
		return Removed{}, fmt.Errorf("unknown log kind")
	}
	want, _ := json.Marshal(rid)
	want = want[1 : len(want)-1] // the escaped value without its quotes
	var kept []record
	var target *record
	k.mu.Lock()
	all := k.ordered()
	for i := range all {
		if target == nil && bytes.Contains(all[i].data, want) {
			target = &all[i]
			continue
		}
		kept = append(kept, all[i])
	}
	if target == nil {
		k.mu.Unlock()
		return Removed{}, ErrNotFound
	}
	// A rewrite keeps the file's line order; records stamped after the target
	// (and the target's own line) are the only ones past it.
	seg, seq := target.seg, target.seq
	out := Removed{Memory: 1}
	k.ring, k.head = nil, 0
	k.ring = kept
	k.mu.Unlock()
	if seg < 0 {
		return out, nil // unreachable: recovery keeps unreadable names at -1
	}
	j := &deleteJob{seg: seg, seq: seq, want: string(want)}
	done := make(chan struct{})
	s.ch <- job{k: k, del: j, done: done}
	<-done
	out.Disk = j.out.Disk
	return out, j.err
}

// dropSegLine removes the record from its segment (writer goroutine).
func (s *Store) dropSegLine(k *kind, seg, seq int64, want string) (int, error) {
	// A record queued before the writer opened its file is stamped 0; it is in
	// the live segment like anything else stamped at or after the previous one.
	k.mu.RLock()
	current, open := k.current, k.file
	k.mu.RUnlock()
	floor := seg
	if floor < current && seg == 0 {
		floor = current
	}
	// The writer opened its latest segment after this record was stamped, so it
	// holds the record at line seq. Older segments are found by their number.
	live := open != nil && current >= floor
	path := ""
	if live {
		path = open.Name()
	}
	if !live {
		path = ""
		for _, f := range s.segments() {
			if f.kind == k.name && segmentIndex(filepath.Base(f.path)) == seg {
				path = f.path
				break
			}
		}
	}
	if path == "" {
		return 0, nil // rotated or trimmed away since the record was written
	}
	if live && k.w.Buffered() > 0 {
		// Lines still in the writer's buffer are not on disk yet: flush them or
		// the scan would miss them and this rewrite would drop them.
		if err := k.w.Flush(); err != nil {
			return 0, err
		}
	}
	// Only lines stamped after seq can be the record or follow it, so skip the
	// rest without scanning their (possibly large) bodies.
	data, n, err := deal(path, recordLimit(k.name), func(line []byte, idx int64) bool {
		return idx < seq || !bytes.Contains(line, []byte(want))
	})
	if err != nil {
		return 0, err
	}
	if live {
		// The append handle cannot be written at an offset, so the rewritten
		// bytes go in through a plain handle that keeps the same inode.
		if err := rewriteInPlace(path, data); err != nil {
			return 0, err
		}
		k.size = int64(len(data))
		k.w.Reset(k.file)
		return 1, nil
	}
	if n == 0 {
		return 1, os.Remove(path)
	}
	return 1, writeFileAtomic(path, data)
}

// rewriteInPlace replaces path's contents with data, keeping the inode so an
// open append handle (the live segment) stays valid.
func rewriteInPlace(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// writeFileAtomic replaces path with data through a synced temporary file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".logseg-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0o600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Clear empties one kind, memory and files.
func (s *Store) Clear(kindName string) error {
	k := s.kinds[kindName]
	if k == nil {
		return fmt.Errorf("unknown log kind")
	}
	k.mu.Lock()
	k.ring, k.head = nil, 0
	k.mu.Unlock()
	done := make(chan struct{})
	s.ch <- job{k: k, clear: true, done: done}
	<-done
	return nil
}

// Close flushes the queue and closes files.
func (s *Store) Close() {
	close(s.ch)
	<-s.closed
}
