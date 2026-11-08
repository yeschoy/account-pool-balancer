// Package stats keeps 24h of per-minute request counters, overall and per
// account. It is written when a request ends and never derived from logs.
package stats

import (
	"sync"
	"time"
)

type Bucket struct {
	Minute        int64 `json:"m"`
	Requests      int64 `json:"r"`
	Success       int64 `json:"s"`
	Failed        int64 `json:"f"`
	Cancelled     int64 `json:"c"`
	Input         int64 `json:"i"`
	Output        int64 `json:"o"`
	Cached        int64 `json:"k"`
	CacheCreation int64 `json:"kw,omitempty"`
	Prompt        int64 `json:"p,omitempty"` // total prompt tokens (cache hits included)
	// CachePrompt and CacheCached cover only requests with both input and cache usage.
	CachePrompt             int64   `json:"cp,omitempty"`
	CacheCached             int64   `json:"ck,omitempty"`
	CacheKnownRequests      int64   `json:"ckr,omitempty"`
	CacheWrite              int64   `json:"cw,omitempty"`
	CacheWriteKnownRequests int64   `json:"cwkr,omitempty"`
	InputKnownRequests      int64   `json:"ikr,omitempty"`
	OutputKnownRequests     int64   `json:"okr,omitempty"`
	DurationCount           int64   `json:"dn,omitempty"`
	BindingHits             int64   `json:"bh,omitempty"`
	AffinityRequests        int64   `json:"ar,omitempty"`
	CacheAffinityHits       int64   `json:"cah,omitempty"`
	DurationMs              int64   `json:"dm,omitempty"`
	TTFTMs                  int64   `json:"tm,omitempty"`
	TTFTCount               int64   `json:"tn,omitempty"`
	CostCNY                 float64 `json:"cc,omitempty"`
	CostUSD                 float64 `json:"cu,omitempty"`
}

func (b *Bucket) add(o Bucket) {
	b.Requests += o.Requests
	b.Success += o.Success
	b.Failed += o.Failed
	b.Cancelled += o.Cancelled
	b.Input += o.Input
	b.Output += o.Output
	b.Cached += o.Cached
	b.CacheCreation += o.CacheCreation
	b.Prompt += o.Prompt
	b.CachePrompt += o.CachePrompt
	b.CacheCached += o.CacheCached
	b.CacheKnownRequests += o.CacheKnownRequests
	b.CacheWrite += o.CacheWrite
	b.CacheWriteKnownRequests += o.CacheWriteKnownRequests
	b.InputKnownRequests += o.InputKnownRequests
	b.OutputKnownRequests += o.OutputKnownRequests
	b.DurationCount += o.DurationCount
	b.BindingHits += o.BindingHits
	b.AffinityRequests += o.AffinityRequests
	b.CacheAffinityHits += o.CacheAffinityHits
	b.DurationMs += o.DurationMs
	b.TTFTMs += o.TTFTMs
	b.TTFTCount += o.TTFTCount
	b.CostCNY += o.CostCNY
	b.CostUSD += o.CostUSD
}

type ring [1440]Bucket

func (r *ring) slot(m int64) *Bucket {
	b := &r[m%1440]
	if b.Minute != m {
		*b = Bucket{Minute: m}
	}
	return b
}

// scope is one set of rings: everything, or one provider's share of it.
type scope struct {
	global  ring
	account map[string]*ring
	model   map[string]*ring
	proxy   map[string]*ring
}

func newScope() *scope {
	return &scope{account: map[string]*ring{}, model: map[string]*ring{}, proxy: map[string]*ring{}}
}

type Stats struct {
	mu   sync.Mutex
	all  *scope
	prov map[string]*scope // per provider (opendesign, river)
	now  func() time.Time
}

func New() *Stats {
	return &Stats{all: newScope(), prov: map[string]*scope{}, now: time.Now}
}

// maxModels bounds the per-model rings: model names come from clients.
const maxModels = 100

// maxProxies bounds the per-egress rings: one per distinct account proxy.
const maxProxies = 200

// Sample is one finished request.
type Sample struct {
	AccountID, Model    string
	Provider            string // opendesign | river; "" counts only in the overall view
	Proxy               string // egress label: proxy host:port, or "direct"
	Result              string // success | failed | client_cancelled
	Input, Output       *int64
	Cached              *int64
	CacheWrite          *int64
	CacheCreation       *int64 // legacy Token Harbor alias; CacheWrite takes precedence
	BindingResult       string
	CacheAffinityResult string
	DurationMs          int64
	TTFTMs              *int64
	Prompt              int64 // total prompt tokens, cache hits included
	Currency            string
	Cost                float64
}

func slotOf(m map[string]*ring, key string, limit int) *ring {
	r := m[key]
	if r == nil && len(m) < limit {
		r = &ring{}
		m[key] = r
	}
	return r
}

func (sc *scope) record(m int64, d Bucket, x Sample) {
	sc.global.slot(m).add(d)
	if x.AccountID != "" {
		slotOf(sc.account, x.AccountID, 1<<30).slot(m).add(d)
	}
	if x.Proxy != "" {
		if r := slotOf(sc.proxy, x.Proxy, maxProxies); r != nil {
			r.slot(m).add(d)
		}
	}
	if x.Model != "" {
		if r := slotOf(sc.model, x.Model, maxModels); r != nil {
			r.slot(m).add(d)
		}
	}
}

// Record adds one finished request.
func (s *Stats) Record(x Sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.now().Unix() / 60
	d := Bucket{Requests: 1, Prompt: x.Prompt, DurationMs: x.DurationMs, DurationCount: 1}
	if x.Input != nil {
		d.InputKnownRequests = 1
	}
	if x.Output != nil {
		d.OutputKnownRequests = 1
	}
	writes := x.CacheWrite
	if writes == nil {
		writes = x.CacheCreation
	}
	if writes != nil {
		// Keep both historical JSON projections without counting two writes.
		d.CacheWrite, d.CacheCreation = *writes, *writes
		d.CacheWriteKnownRequests = 1
	}
	if x.BindingResult != "" && x.BindingResult != "not-applicable" {
		d.AffinityRequests = 1
	}
	if x.BindingResult == "hit" {
		d.BindingHits = 1
	}
	if x.CacheAffinityResult == "hit" {
		d.CacheAffinityHits = 1
	}
	if x.TTFTMs != nil {
		d.TTFTMs, d.TTFTCount = *x.TTFTMs, 1
	}
	switch x.Result {
	case "success":
		d.Success = 1
	case "client_cancelled":
		d.Cancelled = 1
	default:
		d.Failed = 1
	}
	switch x.Currency {
	case "CNY":
		d.CostCNY = x.Cost
	case "USD":
		d.CostUSD = x.Cost
	}
	for _, y := range []struct {
		p   *int64
		dst *int64
	}{{x.Input, &d.Input}, {x.Output, &d.Output}, {x.Cached, &d.Cached}} {
		if y.p != nil {
			*y.dst = *y.p
		}
	}
	if x.Input != nil && x.Cached != nil {
		d.CachePrompt = x.Prompt
		d.CacheCached = *x.Cached
		d.CacheKnownRequests = 1
	}
	s.all.record(m, d, x)
	if x.Provider != "" {
		sc := s.prov[x.Provider]
		if sc == nil {
			sc = newScope()
			s.prov[x.Provider] = sc
		}
		sc.record(m, d, x)
	}
}

type Summary struct {
	Total     Bucket            `json:"total"`
	Hourly    []Bucket          `json:"hourly"` // 24 entries, oldest first; Minute is the hour's first minute
	Accounts  map[string]Bucket `json:"accounts"`
	Models    map[string]Bucket `json:"models"`
	Proxies   map[string]Bucket `json:"proxies"`
	LastHour  Bucket            `json:"lastHour"`
	Generated time.Time         `json:"generated"`
}

func sumRing(r *ring, cut int64) (t Bucket) {
	for _, b := range r {
		if b.Minute > cut {
			t.add(b)
		}
	}
	return t
}

func sumMap(src map[string]*ring, cut int64) map[string]Bucket {
	out := map[string]Bucket{}
	for k, r := range src {
		if t := sumRing(r, cut); t.Requests > 0 {
			out[k] = t
		}
	}
	return out
}

// Summary is the overall view.
func (s *Stats) Summary() Summary { return s.SummaryFor("") }

// SummaryFor is the view of one provider's requests ("" for all of them).
func (s *Stats) SummaryFor(provider string) Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.all
	if provider != "" {
		if sc = s.prov[provider]; sc == nil {
			sc = newScope()
		}
	}
	now := s.now()
	m := now.Unix() / 60
	cut := m - 1440
	hourStart := (m/60 - 23) * 60
	out := Summary{Hourly: make([]Bucket, 24), Generated: now.UTC()}
	for i := range out.Hourly {
		out.Hourly[i].Minute = hourStart + int64(i)*60
	}
	for _, b := range sc.global {
		if b.Minute <= cut || b.Requests == 0 {
			continue
		}
		out.Total.add(b)
		if b.Minute > m-60 {
			out.LastHour.add(b)
		}
		if h := (b.Minute - hourStart) / 60; h >= 0 && h < 24 {
			out.Hourly[h].add(b)
		}
	}
	out.Accounts = sumMap(sc.account, cut)
	out.Models = sumMap(sc.model, cut)
	out.Proxies = sumMap(sc.proxy, cut)
	return out
}

type Export struct {
	Global   []Bucket            `json:"global"`
	Accounts map[string][]Bucket `json:"accounts"`
	Models   map[string][]Bucket `json:"models,omitempty"`
	Proxies  map[string][]Bucket `json:"proxies,omitempty"`
	// Providers is each provider's share. Files written before it existed
	// have none: all of that traffic was OpenDesign.
	Providers map[string]Export `json:"providers,omitempty"`
}

func (sc *scope) export(cut int64) Export {
	pick := func(r *ring) []Bucket {
		var out []Bucket
		for _, b := range r {
			if b.Minute > cut && b.Requests > 0 {
				out = append(out, b)
			}
		}
		return out
	}
	e := Export{Global: pick(&sc.global), Accounts: map[string][]Bucket{}, Models: map[string][]Bucket{}, Proxies: map[string][]Bucket{}}
	for id, r := range sc.account {
		if l := pick(r); len(l) > 0 {
			e.Accounts[id] = l
		}
	}
	for name, r := range sc.model {
		if l := pick(r); len(l) > 0 {
			e.Models[name] = l
		}
	}
	for name, r := range sc.proxy {
		if l := pick(r); len(l) > 0 {
			e.Proxies[name] = l
		}
	}
	return e
}

func (s *Stats) Export() Export {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := s.now().Unix()/60 - 1440
	e := s.all.export(cut)
	if len(s.prov) > 0 {
		e.Providers = map[string]Export{}
		for p, sc := range s.prov {
			e.Providers[p] = sc.export(cut)
		}
	}
	return e
}

// withPrompt fills Prompt for buckets recorded before it existed: those
// requests reported input including the cache hits, so the prompt is the input.
func withPrompt(b Bucket) Bucket {
	if b.Prompt == 0 {
		b.Prompt = b.Input
	}
	return b
}

func loadRing(l []Bucket) *ring {
	r := &ring{}
	for _, b := range l {
		r[b.Minute%1440] = withPrompt(b)
	}
	return r
}

func (sc *scope) load(e Export) {
	for _, b := range e.Global {
		sc.global[b.Minute%1440] = withPrompt(b)
	}
	for id, l := range e.Accounts {
		sc.account[id] = loadRing(l)
	}
	for name, l := range e.Models {
		if len(sc.model) >= maxModels {
			break
		}
		sc.model[name] = loadRing(l)
	}
	for name, l := range e.Proxies {
		if len(sc.proxy) >= maxProxies {
			break
		}
		sc.proxy[name] = loadRing(l)
	}
}

func (s *Stats) Import(e Export) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all.load(e)
	if len(e.Providers) == 0 {
		// written before providers existed: everything was OpenDesign
		sc := newScope()
		sc.load(e)
		s.prov["opendesign"] = sc
		return
	}
	for p, pe := range e.Providers {
		sc := newScope()
		sc.load(pe)
		s.prov[p] = sc
	}
}

// ResetCacheRates clears only measured cache-rate counters for one provider.
// Raw usage, costs, request counts and other providers remain untouched. The
// existing export persists the reset, including new traffic in the same minute.
func (s *Stats) ResetCacheRates(provider string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.prov[provider]
	if provider == "" || sc == nil {
		return
	}
	reset := func(r, all *ring) {
		for i := range r {
			b := &r[i]
			if all != nil && all[i].Minute == b.Minute {
				a := &all[i]
				a.CachePrompt = max(0, a.CachePrompt-b.CachePrompt)
				a.CacheCached = max(0, a.CacheCached-b.CacheCached)
				a.CacheKnownRequests = max(0, a.CacheKnownRequests-b.CacheKnownRequests)
			}
			b.CachePrompt, b.CacheCached, b.CacheKnownRequests = 0, 0, 0
		}
	}
	reset(&sc.global, &s.all.global)
	for _, pair := range []struct{ scoped, all map[string]*ring }{
		{sc.account, s.all.account}, {sc.model, s.all.model}, {sc.proxy, s.all.proxy},
	} {
		for key, r := range pair.scoped {
			reset(r, pair.all[key])
		}
	}
}

func (s *Stats) Forget(id string) {
	s.mu.Lock()
	delete(s.all.account, id)
	for _, sc := range s.prov {
		delete(sc.account, id)
	}
	s.mu.Unlock()
}
