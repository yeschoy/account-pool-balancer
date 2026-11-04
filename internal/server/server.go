// Package server wires the pieces: the downstream /v1 proxy, the admin API
// and console, metadata persistence and the outbound transports.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/fsutil"
	"account-pool-balancer/internal/logstore"
	"account-pool-balancer/internal/pool"
	"account-pool-balancer/internal/quota"
	"account-pool-balancer/internal/stats"
)

type metadata struct {
	Version       int                        `json:"version"`
	RoutingSecret string                     `json:"routingSecret"`
	Pool          pool.Export                `json:"pool"`
	Quota         map[string]*quota.Snapshot `json:"quota"`
	Stats         stats.Export               `json:"stats"`
}

type Server struct {
	dataDir string
	cfg     *config.Store
	pool    *pool.Pool
	quota   *quota.Poller
	logs    *logstore.Store
	stats   *stats.Stats
	secret  []byte

	requestMu      sync.Mutex
	draining       bool
	requests       sync.WaitGroup
	requestContext context.Context
	cancelRequests context.CancelFunc

	tmu         sync.Mutex
	transports  map[string]*http.Transport
	proxyHealth struct {
		sync.Mutex
		states map[string]proxyHealthView
	}

	models struct {
		sync.Mutex
		byGroup  map[string]modelsEntry
		inflight map[string]*modelsCall
		gen      int
	}

	admin   *adminAuth
	devices *deviceFlows
	// openAuth only rate-limits failed import-key authentications.
	openAuth *adminAuth
	// staging holds open-endpoint imports that collide with an account.
	staging *staging
	// imports logs every import call: caller IP, time, account counts.
	imports *importLog
	// perch is the Perch pool's session keeper and scheduler (nil until
	// initPerch ran).
	perch              *perchRuntime
	perchRegistrations registrationManager
	perchAuto          *perchAutoManager
	// The surface-gate preamble is derived once and cached: it comes from a
	// Perch client installed on this machine, or from settings.
	perchPrefixMu     sync.Mutex
	perchPrefix       string
	perchPrefixSource string
	perchPrefixKnown  bool
	// metaDirty: quota, config or the routing secret changed (saved on the next
	// tick). statsDirty: only the rolling counters changed (saved every
	// sampleSaveEvery at most).
	metaMu      sync.Mutex
	metaDirty   atomic.Bool
	statsDirty  atomic.Bool
	metaSavedAt time.Time // protected by metaMu
	started     time.Time
}

const (
	countersSaveEvery = 5 * time.Second
	metaCheckEvery    = 15 * time.Second
	sampleSaveEvery   = time.Minute
)

// Options for New; zero values are fine.
type Options struct {
	// AdminPassword initializes the admin password when none is set yet.
	AdminPassword string
}

func New(dataDir string, opt Options) (*Server, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	store, err := config.Load(filepath.Join(dataDir, "config.json"))
	if err != nil {
		return nil, err
	}
	s := &Server{dataDir: dataDir, cfg: store, stats: stats.New(), transports: map[string]*http.Transport{}, started: time.Now()}
	s.requestContext, s.cancelRequests = context.WithCancel(context.Background())
	s.quota = quota.NewPoller(store.Get, s.httpClient)
	s.quota.OnUpdate = func(id string) {
		s.metaDirty.Store(true)
		if a, ok := s.cfg.Get().Account(id); ok && config.StrictResponses(a.Provider) {
			s.resetModels()
		}
	}
	s.pool = pool.New(s.quota.View)

	valid := map[string]bool{}
	for _, a := range store.Get().Accounts {
		valid[a.ID] = true
	}
	var md metadata
	switch err := fsutil.ReadJSON(filepath.Join(dataDir, "metadata.json"), &md); {
	case errors.Is(err, fsutil.ErrNotExist):
		md = metadata{Version: 1}
	case err != nil:
		return nil, err
	}
	if md.RoutingSecret == "" {
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		md.RoutingSecret = hex.EncodeToString(b)
		s.metaDirty.Store(true)
	}
	if s.secret, err = hex.DecodeString(md.RoutingSecret); err != nil || len(s.secret) < 16 {
		return nil, errors.New("metadata.json: invalid routingSecret")
	}
	s.pool.ImportState(md.Pool, valid)
	quotas := map[string]*quota.Snapshot{}
	for id, q := range md.Quota {
		if valid[id] {
			if a, ok := store.Get().Account(id); ok && a.Provider == config.ProviderTokenHarbor && (q == nil || q.CatalogSource != quota.ResponseBindingSource(store.Get(), a)) {
				continue
			}
			quotas[id] = q
		}
	}
	s.quota.Load(quotas)
	s.stats.Import(md.Stats)

	var cnt pool.Counters
	switch err := fsutil.ReadJSON(filepath.Join(dataDir, "selection-counters.json"), &cnt); {
	case errors.Is(err, fsutil.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := s.pool.ImportCounters(cnt, valid); err != nil {
			return nil, err
		}
	}

	s.logs, err = logstore.Open(filepath.Join(dataDir, "logs"), func() logstore.Limits {
		l := s.cfg.Get().Logs
		return logstore.Limits{RetentionDays: l.RetentionDays, MaxTotalBytes: int64(l.MaxTotalMB) << 20,
			MaxRecords: map[string]int{"requests": l.MaxRequests, "errors": l.MaxErrors, "details": l.MaxDetails, "perch": 20000}}
	})
	if err != nil {
		return nil, err
	}
	s.admin = newAdminAuth(filepath.Join(dataDir, "sessions.json"))
	s.openAuth = newAdminAuth("")
	s.devices = newDeviceFlows()
	if s.staging, err = loadStaging(stagingPath(dataDir)); err != nil {
		return nil, err
	}
	if s.imports, err = loadImportLog(importLogPath(dataDir)); err != nil {
		return nil, err
	}
	if opt.AdminPassword != "" && store.Get().Admin.PasswordHash == "" {
		hash, err := hashPassword(opt.AdminPassword)
		if err != nil {
			return nil, err
		}
		if _, err := store.Update(-1, func(c *config.Config) error { c.Admin.PasswordHash = hash; return nil }); err != nil {
			return nil, err
		}
	}
	store.OnChange = s.onConfigChange
	if err := s.initPerch(); err != nil {
		return nil, fmt.Errorf("perch: %w", err)
	}
	if err := s.initPerchAuto(); err != nil {
		return nil, fmt.Errorf("perch autosignup: %w", err)
	}
	return s, s.saveMetadata(true)
}

// onConfigChange drops what depends on a changed or removed account.
func (s *Server) onConfigChange(old, next *config.Config) {
	s.rebindPerch(next)
	if !reflect.DeepEqual(old.OpenCode, next.OpenCode) || old.TokenHarbor.BaseURL != next.TokenHarbor.BaseURL || !reflect.DeepEqual(old.Accounts, next.Accounts) {
		s.resetModels()
	}
	if old.Routing.DirectMaxConns != next.Routing.DirectMaxConns || old.Routing.ProxyMaxConns != next.Routing.ProxyMaxConns {
		s.tmu.Lock()
		for _, t := range s.transports {
			t.CloseIdleConnections()
		}
		s.transports = map[string]*http.Transport{}
		s.tmu.Unlock()
	}
	for _, a := range old.Accounts {
		n, ok := next.Account(a.ID)
		if a.Provider == config.ProviderTokenHarbor && ok && quota.ResponseBindingSource(old, a) != quota.ResponseBindingSource(next, n) {
			s.quota.Forget(a.ID)
			s.pool.Invalidate(a.ID, false)
		}
		capabilitiesChanged := old.OpenCode.CapabilitiesURL != next.OpenCode.CapabilitiesURL || old.OpenCode.ZenDocsURL != next.OpenCode.ZenDocsURL || old.OpenCode.GoDocsURL != next.OpenCode.GoDocsURL || !reflect.DeepEqual(old.OpenCode.ProtocolBases(a.Provider), next.OpenCode.ProtocolBases(n.Provider))
		if config.IsOpenCode(a.Provider) && ok && (a.RuntimeKey != n.RuntimeKey || a.ProxyURL != n.ProxyURL || a.Provider != n.Provider || old.RuntimeBase(a) != next.RuntimeBase(n) || capabilitiesChanged) {
			s.quota.Forget(a.ID)
			s.pool.Invalidate(a.ID, false)
		}
		if config.IsOpenCode(a.Provider) && ok && !reflect.DeepEqual(old.OpenCode, next.OpenCode) {
			s.quota.RefreshSession(a.ID)
		}
		switch {
		case !ok:
			s.pool.Invalidate(a.ID, true)
			s.quota.Forget(a.ID)
			s.stats.Forget(a.ID)
		case n.RuntimeKey != a.RuntimeKey || n.ProxyURL != a.ProxyURL || n.Group != a.Group:
			s.pool.Invalidate(a.ID, false)
			if n.ControlKey != a.ControlKey || n.ProxyURL != a.ProxyURL {
				s.quota.Forget(a.ID)
			}
		case n.ControlKey != a.ControlKey:
			s.quota.Forget(a.ID)
		case !n.Enabled && a.Enabled:
			s.pool.Invalidate(a.ID, false)
		}
	}
	for _, a := range next.Accounts {
		if config.IsOpenCode(a.Provider) && next.OpenCode.Enabled {
			if o, ok := old.Account(a.ID); !ok || o.RuntimeKey != a.RuntimeKey || o.ProxyURL != a.ProxyURL || old.RuntimeBase(o) != next.RuntimeBase(a) {
				s.quota.RefreshSession(a.ID)
			}
		}
		if _, ok := old.Account(a.ID); !ok && a.ControlKey != "" {
			s.quota.Refresh(a.ID)
		} else if o, _ := old.Account(a.ID); o.ControlKey != a.ControlKey && a.ControlKey != "" {
			s.quota.Refresh(a.ID)
		}
	}
	s.metaDirty.Store(true)
}

// saveMetadata writes metadata.json when it changed. What steers routing
// (cooldowns, quarantines, quota, config) is written right away; the rolling
// counters every request touches (stats, health samples) wait for
// sampleSaveEvery, so steady traffic doesn't rewrite the file on every tick.
func (s *Server) saveMetadata(force bool) error {
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	states, samples := s.pool.Dirty()
	samples = samples || s.statsDirty.Load()
	if !force && !states && !s.metaDirty.Load() && !(samples && time.Since(s.metaSavedAt) >= sampleSaveEvery) {
		return nil
	}
	s.metaDirty.Store(false)
	s.statsDirty.Store(false)
	exp, _ := s.pool.ExportState()
	md := metadata{Version: 1, RoutingSecret: hex.EncodeToString(s.secret), Pool: exp, Quota: s.quota.All(), Stats: s.stats.Export()}
	if err := fsutil.WriteJSONCompactAtomic(filepath.Join(s.dataDir, "metadata.json"), md); err != nil {
		s.metaDirty.Store(true)
		return err
	}
	s.metaSavedAt = time.Now()
	return nil
}

func (s *Server) saveCounters(force bool) error {
	c, changed := s.pool.ExportCounters(force)
	if !changed {
		return nil
	}
	return fsutil.WriteJSONCompactAtomic(filepath.Join(s.dataDir, "selection-counters.json"), c)
}

// Run starts the background loops until ctx ends, then flushes state.
func (s *Server) Run(ctx context.Context) {
	quotaDone := make(chan struct{})
	go func() { defer close(quotaDone); s.quota.Run(ctx) }()
	s.RunPerch(ctx)
	autoDone := make(chan struct{})
	go func() { defer close(autoDone); s.runPerchAuto(ctx) }()
	counters := time.NewTicker(countersSaveEvery)
	meta := time.NewTicker(metaCheckEvery)
	defer counters.Stop()
	defer meta.Stop()
	for {
		select {
		case <-ctx.Done():
			s.drainRequests() // cancel inference, release leases, then persist and close logs
			<-quotaDone
			<-autoDone
			if err := s.saveCounters(true); err != nil {
				log.Printf("save counters: %v", err)
			}
			if err := s.saveMetadata(true); err != nil {
				log.Printf("save metadata: %v", err)
			}
			s.stopPerch()
			s.logs.Close()
			return
		case <-counters.C:
			if err := s.saveCounters(false); err != nil {
				log.Printf("save counters: %v", err)
			}
		case <-meta.C:
			if err := s.saveMetadata(false); err != nil {
				log.Printf("save metadata: %v", err)
			}
		}
	}
}

// Reload rereads config.json (SIGHUP); in-flight requests keep their snapshot.
func (s *Server) Reload() error { return s.cfg.Reload() }

func (s *Server) transport(proxyURL string) *http.Transport {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if t := s.transports[proxyURL]; t != nil {
		return t
	}
	cfg := s.cfg.Get().Routing
	t := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   cfg.DirectMaxConns,
		MaxConnsPerHost:       cfg.DirectMaxConns,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		DisableCompression:    true,
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			// never fall back to a direct connection
			t.Proxy = func(*http.Request) (*url.URL, error) { return nil, errors.New("invalid proxy URL") }
		} else {
			t.Proxy = http.ProxyURL(u)
		}
		t.MaxIdleConnsPerHost, t.MaxConnsPerHost = cfg.ProxyMaxConns, cfg.ProxyMaxConns
	}
	s.transports[proxyURL] = t
	return t
}

func (s *Server) httpClient(proxyURL string) *http.Client {
	return &http.Client{Transport: s.transport(proxyURL)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/", s.handleV1)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("POST /open/v1/accounts/import", s.handleOpenImport)
	// Perch's open import takes the account EXPORT (a Supabase session)
	// rather than an API key, so it has its own endpoint.
	mux.HandleFunc("POST /open/v1/perch/import", s.apiPerchOpenImport)
	s.registerAdmin(mux)
	return s.trackRequests(mux)
}

func (s *Server) trackRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requestMu.Lock()
		if s.draining {
			s.requestMu.Unlock()
			writeLocalError(w, "openai", http.StatusServiceUnavailable, "api_error", "server_draining", "server is shutting down", 1)
			return
		}
		s.requests.Add(1)
		s.requestMu.Unlock()
		defer s.requests.Done()
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(s.requestContext, cancel)
		defer stop()
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) drainRequests() {
	// The admission lock makes Add/Wait safe: no new handler can register once
	// draining becomes visible. Callers already running retain their snapshot.
	s.requestMu.Lock()
	s.draining = true
	s.requestMu.Unlock()
	s.cancelRequests()
	s.requests.Wait()
}
