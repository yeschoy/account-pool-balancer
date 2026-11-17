package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/telnyxreg"
	"account-pool-balancer/internal/telnyxsignupstore"
)

// The Telnyx registration machine (telnyx.com).
//
// Shape of a run, all steps verified against a live session on 2026-11-16:
//
//	validate-email → Turnstile (captcha.run) → POST /v2/unified_signup
//	→ wait for the "Your Single Use Telnyx Portal sign-in link" mail
//	→ POST /v2/ai_dev_event_sessions (redeems the one-time link)
//	→ POST /v2/api_keys → GET /v2/balance/promo-credits
//
// Three properties drive the whole design:
//
//  1. The Turnstile token is bound to the widget's data-action ("signup").
//     A token minted without it is always rejected, so the action is a setting
//     and the runner retries with a fresh token instead of failing the row.
//  2. The one-time portal link is single-use and expires within hours. A row
//     that misses its window cannot be resumed; it must sign up again.
//  3. The portal session is bound to the egress that redeemed the link, so one
//     account keeps one proxy for its whole run.
//
// Rows hold real credentials (session token, API key, mailbox refresh token).
// List responses go through telnyxRowView, which reveals the API key only on the
// explicit authenticated reveal action.

const (
	// telnyxMaxRows bounds the stored queue so the JSON store and the table stay
	// responsive; older finished rows must be cleared to import more.
	telnyxMaxRows = 3000
	// telnyxMaxBatch bounds one import request.
	telnyxMaxBatch = 500
	// telnyxLogLines bounds the in-memory log ring.
	telnyxLogLines = 400
)

// telnyxBulkInput is one "email----password----clientId----refreshToken" or
// "email----password----shareToken" line, already split.
type telnyxBulkInput struct {
	Email           string `json:"email"`
	MailboxPassword string `json:"mailboxPassword,omitempty"`
	MailMode        string `json:"mailMode"`
	ShareToken      string `json:"shareToken,omitempty"`
	ClientID        string `json:"clientId,omitempty"`
	RefreshToken    string `json:"refreshToken,omitempty"`
}

type telnyxRegisterRequest struct {
	Text string `json:"text"`
	// Proxies replaces the saved pool when non-empty. Order is significant:
	// assignment walks the list in order.
	Proxies []string `json:"proxies,omitempty"`
	// Concurrency 1–10; zero uses the saved default.
	Concurrency int `json:"concurrency,omitempty"`
	// Seeds restricts a start request to specific row IDs.
	Seeds []string `json:"seeds,omitempty"`
	// DryRun parses and reports without persisting or registering.
	DryRun bool `json:"dryRun,omitempty"`
}

// telnyxRowView is the console projection of a row. Credentials other than the
// masked key prefix never appear here.
type telnyxRowView struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	MailMode     string     `json:"mailMode"`
	HasShare     bool       `json:"hasShare"`
	HasMsa       bool       `json:"hasMsa"`
	ShareToken   string     `json:"shareTokenMasked,omitempty"`
	ProxyLabel   string     `json:"proxyLabel"`
	ProxyBound   bool       `json:"proxyBound"`
	Status       string     `json:"status"`
	StatusText   string     `json:"statusText"`
	Stage        string     `json:"stage,omitempty"`
	KeyPrefix    string     `json:"keyPrefix,omitempty"`
	HasKey       bool       `json:"hasKey"`
	HasSession   bool       `json:"hasSession"`
	AccountLevel string     `json:"accountLevel,omitempty"`
	CreditsUSD   string     `json:"creditsUsd,omitempty"`
	EgressIP     string     `json:"egressIp,omitempty"`
	Error        string     `json:"error,omitempty"`
	Attempts     int        `json:"attempts,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	KeyedAt      *time.Time `json:"keyedAt,omitempty"`
}

func telnyxViewRow(r telnyxsignupstore.Row) telnyxRowView {
	v := telnyxRowView{
		ID:           r.ID,
		Email:        r.Email,
		MailMode:     r.MailMode,
		HasShare:     r.ShareToken != "",
		HasMsa:       r.ClientID != "" && r.RefreshToken != "",
		ProxyLabel:   telnyxProxyLabel(r.ProxyURL),
		ProxyBound:   r.ProxyURL != "",
		Status:       string(r.Status),
		StatusText:   telnyxStatusText(r.Status),
		Stage:        r.Stage,
		KeyPrefix:    telnyxKeyPrefix(r.APIKey),
		HasKey:       r.APIKey != "",
		HasSession:   r.SessionToken != "",
		AccountLevel: r.AccountLevel,
		CreditsUSD:   r.CreditsUSD,
		EgressIP:     r.EgressIP,
		Error:        r.Error,
		Attempts:     r.Attempts,
		CreatedAt:    r.CreatedAt,
		UpdatedAt:    r.UpdatedAt,
	}
	if r.ShareToken != "" {
		v.ShareToken = telnyxMaskShare(r.ShareToken)
	}
	if !r.KeyedAt.IsZero() {
		t := r.KeyedAt
		v.KeyedAt = &t
	}
	return v
}

func telnyxStatusText(s telnyxsignupstore.Status) string {
	switch s {
	case telnyxsignupstore.Queued:
		return "待注册"
	case telnyxsignupstore.Running:
		return "注册中"
	case telnyxsignupstore.Waiting:
		return "等待验证邮件"
	case telnyxsignupstore.Linked:
		return "已换会话"
	case telnyxsignupstore.Keyed:
		return "已完成"
	case telnyxsignupstore.Failed:
		return "失败"
	case telnyxsignupstore.Cancelled:
		return "已取消"
	}
	return string(s)
}

func telnyxKeyPrefix(k string) string {
	if k == "" {
		return ""
	}
	if i := strings.IndexByte(k, '_'); i > 0 {
		return k[:i]
	}
	if len(k) > 12 {
		return k[:12]
	}
	return k
}

// telnyxMaskShare keeps the first and last few characters so an operator can
// tell two inboxes apart without the token being copied off a screenshot.
func telnyxMaskShare(t string) string {
	if len(t) <= 10 {
		return "***"
	}
	return t[:6] + "…" + t[len(t)-4:]
}

// telnyxProxyLabel strips credentials for display.
func telnyxProxyLabel(raw string) string {
	if raw == "" {
		return "直连"
	}
	if i := strings.Index(raw, "@"); i >= 0 {
		if j := strings.Index(raw, "://"); j >= 0 {
			return raw[:j+3] + "***@" + raw[i+1:]
		}
		return "***@" + raw[i+1:]
	}
	return raw
}

// telnyxManager owns the run queue.
type telnyxManager struct {
	srv   *Server
	store *telnyxsignupstore.Store
	base  context.Context

	mu      sync.Mutex
	running map[string]context.CancelFunc
	pending int
	cancel  context.CancelFunc
	// gate bounds concurrent runs to the configured budget.
	gate chan struct{}

	wg   sync.WaitGroup
	logs []telnyxLogLine
}

type telnyxRunJob struct {
	RowID string
	Email string
	Mail  telnyxsignupstore.Row
	Proxy string
}

func (s *Server) telnyxStore() (*telnyxsignupstore.Store, error) {
	s.telnyxOnce.Do(func() {
		path := filepath.Join(s.dataDir, "telnyx-signup.json")
		st, err := telnyxsignupstore.Open(path)
		if err != nil {
			s.telnyxInitErr = err
			return
		}
		s.telnyxStoreRef = st
	})
	return s.telnyxStoreRef, s.telnyxInitErr
}

func (s *Server) telnyxMgr() (*telnyxManager, error) {
	st, err := s.telnyxStore()
	if err != nil {
		return nil, err
	}
	// telnyxBaseCtx takes telnyxMu itself, so resolve it before locking here:
	// calling it under the same lock self-deadlocks on the first request.
	root := s.telnyxBaseCtx()

	s.telnyxMu.Lock()
	defer s.telnyxMu.Unlock()
	if s.telnyxMgrRef != nil {
		return s.telnyxMgrRef, nil
	}
	ctx, cancel := context.WithCancel(root)
	s.telnyxMgrRef = &telnyxManager{
		srv:     s,
		store:   st,
		base:    ctx,
		running: map[string]context.CancelFunc{},
		cancel:  cancel,
	}
	return s.telnyxMgrRef, nil
}

// telnyxBaseCtx is the service-level context every background run hangs off.
//
// It must not be the request context: a handler returning would cancel the
// registration goroutines mid-flight.
func (s *Server) telnyxBaseCtx() context.Context {
	s.telnyxMu.Lock()
	defer s.telnyxMu.Unlock()
	if s.telnyxCtx != nil {
		return s.telnyxCtx
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.telnyxCtx = ctx
	s.telnyxCtxCancel = cancel
	return ctx
}

type telnyxLogLine struct {
	At    time.Time `json:"at"`
	Type  string    `json:"type"`
	Email string    `json:"email,omitempty"`
	Msg   string    `json:"msg"`
}

func (m *telnyxManager) addLog(typ, email, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs = append(m.logs, telnyxLogLine{At: time.Now().UTC(), Type: typ, Email: email, Msg: msg})
	if len(m.logs) > telnyxLogLines {
		m.logs = m.logs[len(m.logs)-telnyxLogLines:]
	}
}

// Logs returns the ring, newest last.
func (m *telnyxManager) Logs() []telnyxLogLine {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]telnyxLogLine, len(m.logs))
	copy(out, m.logs)
	return out
}

// assignTelnyxProxies binds the least-loaded proxy to each target row.
//
// Order is preserved and the binding is persisted so a retry keeps the same
// egress: the portal token is bound to the address that submitted the signup,
// and switching mid-run turns a fresh link into a 401.
func assignTelnyxProxies(existing []telnyxsignupstore.Row, targets []telnyxsignupstore.Row, pool []string) map[string]string {
	if len(pool) == 0 {
		return nil
	}
	load := map[string]int{}
	for _, r := range existing {
		if r.ProxyURL != "" {
			load[r.ProxyURL]++
		}
	}
	out := map[string]string{}
	for _, r := range targets {
		if r.ProxyURL != "" {
			continue // keep the pinned binding
		}
		best, bestN := pool[0], -1
		for _, p := range pool {
			n := load[p]
			if bestN < 0 || n < bestN {
				best, bestN = p, n
			}
		}
		load[best]++
		out[r.ID] = best
	}
	return out
}

func (m *telnyxManager) startRows(ids []string, concurrency int, settings telnyxsignupstore.Settings) (int, error) {
	if m.srv != nil && !m.srv.cfg.Get().PoolEnabled("telnyx") {
		return 0, errors.New("Telnyx号池已关闭")
	}
	if concurrency <= 0 {
		concurrency = settings.Concurrency
	}
	concurrency = max(telnyxsignupstore.MinConcurrency, min(concurrency, telnyxsignupstore.MaxConcurrency))

	m.mu.Lock()
	m.gate = make(chan struct{}, concurrency)
	m.mu.Unlock()

	bound := assignTelnyxProxies(m.store.List(), nil, settings.ProxyPool)

	var jobs []telnyxRunJob
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		row, err := m.store.Get(id)
		if err != nil {
			continue
		}
		if row.Status == telnyxsignupstore.Keyed {
			continue // already complete: never replay and mint a second key
		}
		m.mu.Lock()
		if _, busy := m.running[id]; busy {
			m.mu.Unlock()
			continue
		}
		runCtx, cancel := context.WithCancel(m.base)
		if m.srv != nil {
			cancel()
			runCtx, cancel = m.srv.poolJobContext(m.base, "telnyx")
		}
		m.running[id] = cancel
		m.pending++
		m.mu.Unlock()

		proxy := row.ProxyURL
		if proxy == "" {
			proxy = bound[id]
		}
		if _, uerr := m.store.Update(id, func(r *telnyxsignupstore.Row) error {
			r.Status = telnyxsignupstore.Running
			r.Stage = string(telnyxreg.StageValidate)
			r.Error = ""
			if proxy != "" {
				r.ProxyURL = proxy
			}
			return nil
		}); uerr != nil {
			m.mu.Lock()
			delete(m.running, id)
			m.pending--
			m.mu.Unlock()
			cancel()
			continue
		}
		jobs = append(jobs, telnyxRunJob{RowID: id, Email: row.Email, Mail: row, Proxy: proxy})
		m.wg.Add(1)
		go func(j telnyxRunJob) {
			defer m.wg.Done()
			m.runOne(runCtx, j, settings)
		}(jobs[len(jobs)-1])
	}
	// Respect the concurrency budget: startRows queues everything, the gate
	// below throttles actual upstream traffic.
	if concurrency < len(jobs) {
		m.addLog("info", "", fmt.Sprintf("已排队 %d 个，并发上限 %d", len(jobs), concurrency))
	}
	return len(jobs), nil
}

func (m *telnyxManager) runOne(ctx context.Context, job telnyxRunJob, settings telnyxsignupstore.Settings) {
	if m.srv != nil {
		var cancel context.CancelFunc
		ctx, cancel = m.srv.poolJobContext(ctx, "telnyx")
		defer cancel()
	}
	// Throttle upstream traffic to the configured budget. Rows are queued up
	// front so the console shows the whole batch immediately.
	if m.gate != nil {
		select {
		case m.gate <- struct{}{}:
			defer func() { <-m.gate }()
		case <-ctx.Done():
			m.finish(job.RowID, telnyxsignupstore.Cancelled, ctx.Err())
			m.mu.Lock()
			delete(m.running, job.RowID)
			m.pending--
			m.mu.Unlock()
			return
		}
	}
	defer func() {
		m.mu.Lock()
		if cancel, ok := m.running[job.RowID]; ok {
			cancel()
		}
		delete(m.running, job.RowID)
		m.pending--
		m.mu.Unlock()
	}()

	// Resolve the mailbox: an MsaArtifacts pair has to become a share token
	// before the inbox is readable, and the refresh token rotates on the way.
	shareToken, err := m.resolveMailbox(ctx, &job, settings)
	if err != nil {
		m.addLog("error", job.Email, "邮箱解析失败: "+err.Error())
		m.finish(job.RowID, telnyxsignupstore.Failed, err)
		return
	}

	solver := telnyxreg.NewCaptchaRun(settings.CaptchaToken)
	box := telnyxreg.NewYaoYiMailbox(shareToken)
	if settings.MailBase != "" {
		box.BaseURL = settings.MailBase
	}

	var tags []string
	for _, t := range strings.Split(settings.KeyTags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}

	reg, err := telnyxreg.New(telnyxreg.Config{
		SiteOrigin:         settings.SiteOrigin,
		APIBase:            settings.APIBase,
		PortalBase:         settings.PortalBase,
		Email:              job.Email,
		ProxyURL:           job.Proxy,
		Captcha:            solver,
		Mailbox:            box,
		SiteKey:            settings.SiteKey,
		TurnstileAction:    settings.TurnstileAction,
		KeyTags:            tags,
		OptInSubscriptions: settings.OptInSubscriptions,
		MailWait:           time.Duration(settings.MailTimeoutSec) * time.Second,
		MailInterval:       time.Duration(settings.MailPollSec) * time.Second,
		Timeout:            time.Duration(settings.RequestTimeoutSec) * time.Second,
	})
	if err != nil {
		m.finish(job.RowID, telnyxsignupstore.Failed, err)
		return
	}
	reg.SetProgress(func(stage telnyxreg.Stage, detail string) {
		_, _ = m.store.Update(job.RowID, func(r *telnyxsignupstore.Row) error {
			r.Stage = string(stage)
			switch stage {
			case telnyxreg.StageMailWait:
				r.Status = telnyxsignupstore.Waiting
			case telnyxreg.StageSession:
				r.Status = telnyxsignupstore.Linked
			}
			return nil
		})
	})
	reg.SetTracer(func(f string, a ...any) {
		m.addLog("info", job.Email, fmt.Sprintf(f, a...))
	})

	acct, err := reg.Run(ctx)
	if err != nil {
		m.addLog("error", job.Email, err.Error())
		m.finish(job.RowID, telnyxsignupstore.Failed, err)
		return
	}
	m.addLog("ok", job.Email, fmt.Sprintf("注册完成，%s USD，Key %s",
		acct.CreditsUSD, telnyxKeyPrefix(acct.APIKey)))
	now := time.Now().UTC()
	m.finish(job.RowID, telnyxsignupstore.Keyed, nil, func(r *telnyxsignupstore.Row) {
		r.SessionToken = acct.SessionToken
		r.APIKey = acct.APIKey
		r.KeyID = acct.KeyID
		r.UserID = acct.UserID
		r.AccountLevel = acct.AccountLevel
		r.CreditsUSD = acct.CreditsUSD
		r.EgressIP = acct.EgressIP
		r.LinkedAt = now
		r.KeyedAt = now
		r.Stage = string(telnyxreg.StageDone)
	})
}

// resolveMailbox turns an MsaArtifacts pair into a share token and writes the
// rotated refresh token back, so the mailbox survives the next attempt.
func (m *telnyxManager) resolveMailbox(ctx context.Context, job *telnyxRunJob, settings telnyxsignupstore.Settings) (string, error) {
	if job.Mail.ShareToken != "" {
		return job.Mail.ShareToken, nil
	}
	if job.Mail.ClientID == "" || job.Mail.RefreshToken == "" {
		return "", errors.New("缺少取件凭据（shareToken 或 clientId+refreshToken）")
	}
	res := telnyxreg.NewResolver()
	if settings.MailBase != "" {
		res.BaseURL = settings.MailBase
	}
	got, err := res.Resolve(ctx, telnyxreg.MailCredential{
		Email:        job.Email,
		ClientID:     job.Mail.ClientID,
		RefreshToken: job.Mail.RefreshToken,
	})
	if err != nil {
		return "", err
	}
	if _, uerr := m.store.Update(job.RowID, func(r *telnyxsignupstore.Row) error {
		r.ShareToken = got.ShareToken
		r.RefreshToken = got.RefreshToken // rotated: the old one is now dead
		r.MailMode = telnyxsignupstore.MailModeShareToken
		return nil
	}); uerr != nil {
		m.addLog("error", job.Email, "回写邮箱凭据失败: "+uerr.Error())
	}
	m.addLog("info", job.Email, "取件地址已解析 ("+got.State+")")
	return got.ShareToken, nil
}

// finish records the terminal state of one attempt.
func (m *telnyxManager) finish(id string, status telnyxsignupstore.Status, cause error, mutators ...func(*telnyxsignupstore.Row)) {
	_, _ = m.store.Update(id, func(r *telnyxsignupstore.Row) error {
		for _, fn := range mutators {
			fn(r)
		}
		r.Status = status
		r.Attempts++
		if status == telnyxsignupstore.Failed {
			r.Error = telnyxErrorText(cause)
			r.Stage = ""
		} else {
			r.Error = ""
		}
		return nil
	})
	_ = m.store.Flush()
}

func telnyxErrorText(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}

// stopTelnyx cancels every in-flight registration and waits for them.
func (s *Server) stopTelnyx() {
	s.telnyxMu.Lock()
	mgr := s.telnyxMgrRef
	cancel := s.telnyxCtxCancel
	s.telnyxMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if mgr != nil {
		mgr.wg.Wait()
	}
	if st, err := s.telnyxStore(); err == nil {
		_ = st.Flush()
	}
}

// registerTelnyx wires the console's endpoints.
func (s *Server) registerTelnyx(_ *http.ServeMux, a func(string, http.HandlerFunc)) {
	a("GET /api/telnyx/candidates", s.apiTelnyxCandidates)
	a("POST /api/telnyx/candidates/bulk", s.apiTelnyxBulkImport)
	a("POST /api/telnyx/candidates/import-completed", s.apiTelnyxImportCompleted)
	a("DELETE /api/telnyx/candidates/{id}", s.apiTelnyxDeleteRow)
	a("POST /api/telnyx/candidates/{id}/register", s.apiTelnyxRegisterOne)
	a("POST /api/telnyx/candidates/{id}/retry", s.apiTelnyxRetryOne)
	a("POST /api/telnyx/candidates/bulk-register", s.apiTelnyxRegisterPending)
	a("POST /api/telnyx/candidates/clear-finished", s.apiTelnyxClearFinished)
	a("GET /api/telnyx/candidates/{id}/key", s.apiTelnyxRevealKey)
	a("GET /api/telnyx/status", s.apiTelnyxStatus)
	a("GET /api/telnyx/logs", s.apiTelnyxLogs)
	a("GET /api/telnyx/settings", s.apiTelnyxSettings)
	a("PUT /api/telnyx/settings", s.apiTelnyxSaveSettings)
	a("GET /api/telnyx/export", s.apiTelnyxExport)
}

func (s *Server) telnyxRowViews() ([]telnyxRowView, error) {
	st, err := s.telnyxStore()
	if err != nil {
		return nil, err
	}
	rows := st.List()
	out := make([]telnyxRowView, 0, len(rows))
	for _, r := range rows {
		out = append(out, telnyxViewRow(r))
	}
	return out, nil
}

func (s *Server) apiTelnyxCandidates(w http.ResponseWriter, r *http.Request) {
	views, err := s.telnyxRowViews()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": views, "settings": s.telnyxSettingsView()})
}

// telnyxSettingsView projects settings for the console. The captcha token is
// never echoed: only whether one is stored.
func (s *Server) telnyxSettingsView() map[string]any {
	st, err := s.telnyxStore()
	if err != nil {
		return map[string]any{}
	}
	set := st.Settings()
	mask := func(v string) string {
		if v == "" {
			return ""
		}
		if len(v) <= 8 {
			return "***"
		}
		return v[:4] + "…" + v[len(v)-4:]
	}
	proxies := make([]string, 0, len(set.ProxyPool))
	for _, p := range set.ProxyPool {
		proxies = append(proxies, telnyxProxyLabel(p))
	}
	return map[string]any{
		"concurrency":        set.Concurrency,
		"mailTimeoutSec":     set.MailTimeoutSec,
		"mailPollSec":        set.MailPollSec,
		"requestTimeoutSec":  set.RequestTimeoutSec,
		"captchaTokenSet":    set.CaptchaToken != "",
		"captchaTokenMasked": mask(set.CaptchaToken),
		"siteKey":            set.SiteKey,
		"turnstileAction":    set.TurnstileAction,
		"captchaAttempts":    set.CaptchaAttempts,
		"keyTags":            set.KeyTags,
		"optInSubscriptions": set.OptInSubscriptions,
		"proxyPoolCount":     len(set.ProxyPool),
		"proxies":            proxies,
		"autoBindProxy":      set.AutoBindProxy,
		"siteOrigin":         set.SiteOrigin,
		"apiBase":            set.APIBase,
		"portalBase":         set.PortalBase,
		"mailBase":           set.MailBase,
	}
}

func (s *Server) apiTelnyxBulkImport(w http.ResponseWriter, r *http.Request) {
	var req telnyxRegisterRequest
	if err := telnyxDecodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	inputs := parseTelnyxLines(req.Text)
	if len(inputs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有可导入的行"})
		return
	}
	if len(inputs) > telnyxMaxBatch {
		inputs = inputs[:telnyxMaxBatch]
	}
	if req.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{"parsed": inputs, "dryRun": true})
		return
	}
	st, err := s.telnyxStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if len(st.List()) >= telnyxMaxRows {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("队列已满（%d），请先清理已完成的行", telnyxMaxRows)})
		return
	}

	rows := make([]telnyxsignupstore.Row, 0, len(inputs))
	for _, in := range inputs {
		row := telnyxsignupstore.NewRow(in.Email)
		row.MailboxPassword = in.MailboxPassword
		row.MailMode = in.MailMode
		row.ShareToken = in.ShareToken
		row.ClientID = in.ClientID
		row.RefreshToken = in.RefreshToken
		rows = append(rows, row)
	}

	set := st.Settings()
	if len(req.Proxies) > 0 {
		set.ProxyPool = normalizeProxies(req.Proxies)
	}
	if set.AutoBindProxy && len(set.ProxyPool) > 0 {
		bind := assignTelnyxProxies(st.List(), rows, set.ProxyPool)
		for i := range rows {
			if p, ok := bind[rows[i].ID]; ok {
				rows[i].ProxyURL = p
			}
		}
	}
	added, errs, err := st.AddMany(rows)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"added": len(added), "skipped": len(errs), "errors": errs,
	})
}

// parseTelnyxLines accepts both shipped credential shapes:
//
//	email----password----<shareToken>
//	email----password----<clientId uuid>----<refreshToken>
//
// A UUID in the third field is always the Microsoft OAuth client id shared by
// every exported line, never a share token, so it is routed to MailModeMsa.
func parseTelnyxLines(text string) []telnyxBulkInput {
	var out []telnyxBulkInput
	seen := map[string]bool{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "----")
		email := strings.TrimSpace(parts[0])
		if email == "" || seen[strings.ToLower(email)] {
			continue
		}
		in := telnyxBulkInput{Email: email}
		if len(parts) > 1 {
			in.MailboxPassword = strings.TrimSpace(parts[1])
		}
		if len(parts) < 3 {
			continue
		}
		third := strings.TrimSpace(parts[2])
		if telnyxUUIDLike.MatchString(third) {
			if len(parts) < 4 || strings.TrimSpace(parts[3]) == "" {
				continue // client id without a refresh token is unusable
			}
			in.MailMode = telnyxsignupstore.MailModeMsa
			in.ClientID = third
			in.RefreshToken = strings.TrimSpace(parts[3])
		} else {
			in.MailMode = telnyxsignupstore.MailModeShareToken
			in.ShareToken = third
		}
		seen[strings.ToLower(email)] = true
		out = append(out, in)
	}
	return out
}

var telnyxUUIDLike = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (s *Server) apiTelnyxDeleteRow(w http.ResponseWriter, r *http.Request) {
	st, err := s.telnyxStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	if err := st.Delete(r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	_ = st.Flush()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiTelnyxRegisterOne(w http.ResponseWriter, r *http.Request) {
	s.telnyxStart(w, r, []string{r.PathValue("id")})
}

func (s *Server) apiTelnyxRetryOne(w http.ResponseWriter, r *http.Request) {
	s.telnyxStart(w, r, []string{r.PathValue("id")})
}

func (s *Server) apiTelnyxRegisterPending(w http.ResponseWriter, r *http.Request) {
	st, err := s.telnyxStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	var ids []string
	for _, row := range st.List() {
		if row.Status.Retryable() {
			ids = append(ids, row.ID)
		}
	}
	s.telnyxStart(w, r, ids)
}

func (s *Server) telnyxStart(w http.ResponseWriter, r *http.Request, ids []string) {
	m, err := s.telnyxMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	st, _ := s.telnyxStore()
	set := st.Settings()
	// Also read the request body for a concurrency override.
	var req telnyxRegisterRequest
	_ = telnyxDecodeJSON(r, &req)
	if req.Concurrency > 0 {
		set.Concurrency = req.Concurrency
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "没有待注册的行"})
		return
	}
	n, err := m.startRows(ids, set.Concurrency, set)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": n})
}

func (s *Server) apiTelnyxClearFinished(w http.ResponseWriter, r *http.Request) {
	st, err := s.telnyxStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	n, err := st.ClearFinished()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	_ = st.Flush()
	writeJSON(w, http.StatusOK, map[string]any{"removed": n})
}

// apiTelnyxRevealKey returns the full API key for one row. Like TokenHarbor's
// saved-key reads this is a deliberate action, never part of a list response.
func (s *Server) apiTelnyxRevealKey(w http.ResponseWriter, r *http.Request) {
	st, err := s.telnyxStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	row, err := st.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": err.Error()})
		return
	}
	if row.APIKey == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "该行还没有 API Key"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"email": row.Email, "apiKey": row.APIKey, "keyId": row.KeyID,
	})
}

func (s *Server) apiTelnyxStatus(w http.ResponseWriter, r *http.Request) {
	m, err := s.telnyxMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	views, _ := s.telnyxRowViews()
	m.mu.Lock()
	pending := m.pending
	running := len(m.running)
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"pending": pending, "running": running,
		"candidates": views, "settings": s.telnyxSettingsView(),
	})
}

func (s *Server) apiTelnyxLogs(w http.ResponseWriter, r *http.Request) {
	m, err := s.telnyxMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": m.Logs()})
}

func (s *Server) apiTelnyxSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.telnyxSettingsView())
}

func (s *Server) apiTelnyxSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Concurrency        *int     `json:"concurrency"`
		MailTimeoutSec     *int     `json:"mailTimeoutSec"`
		MailPollSec        *int     `json:"mailPollSec"`
		RequestTimeoutSec  *int     `json:"requestTimeoutSec"`
		CaptchaToken       *string  `json:"captchaToken"`
		SiteKey            *string  `json:"siteKey"`
		TurnstileAction    *string  `json:"turnstileAction"`
		CaptchaAttempts    *int     `json:"captchaAttempts"`
		KeyTags            *string  `json:"keyTags"`
		OptInSubscriptions *bool    `json:"optInSubscriptions"`
		Proxies            []string `json:"proxies"`
		AutoBindProxy      *bool    `json:"autoBindProxy"`
		SiteOrigin         *string  `json:"siteOrigin"`
		APIBase            *string  `json:"apiBase"`
		PortalBase         *string  `json:"portalBase"`
		MailBase           *string  `json:"mailBase"`
	}
	if err := telnyxDecodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	st, err := s.telnyxStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	set := st.Settings()
	if in.Concurrency != nil {
		set.Concurrency = *in.Concurrency
	}
	if in.MailTimeoutSec != nil {
		set.MailTimeoutSec = *in.MailTimeoutSec
	}
	if in.MailPollSec != nil {
		set.MailPollSec = *in.MailPollSec
	}
	if in.RequestTimeoutSec != nil {
		set.RequestTimeoutSec = *in.RequestTimeoutSec
	}
	// An empty captchaToken means "keep the stored one": the console never
	// receives the real value, so it cannot echo it back.
	if in.CaptchaToken != nil && *in.CaptchaToken != "" {
		set.CaptchaToken = *in.CaptchaToken
	}
	if in.SiteKey != nil && *in.SiteKey != "" {
		set.SiteKey = *in.SiteKey
	}
	if in.TurnstileAction != nil && *in.TurnstileAction != "" {
		set.TurnstileAction = *in.TurnstileAction
	}
	if in.CaptchaAttempts != nil {
		set.CaptchaAttempts = *in.CaptchaAttempts
	}
	if in.KeyTags != nil {
		set.KeyTags = *in.KeyTags
	}
	if in.OptInSubscriptions != nil {
		set.OptInSubscriptions = *in.OptInSubscriptions
	}
	if in.Proxies != nil {
		set.ProxyPool = normalizeProxies(in.Proxies)
	}
	if in.AutoBindProxy != nil {
		set.AutoBindProxy = *in.AutoBindProxy
	}
	if in.SiteOrigin != nil {
		set.SiteOrigin = strings.TrimRight(*in.SiteOrigin, "/")
	}
	if in.APIBase != nil {
		set.APIBase = strings.TrimRight(*in.APIBase, "/")
	}
	if in.PortalBase != nil {
		set.PortalBase = strings.TrimRight(*in.PortalBase, "/")
	}
	if in.MailBase != nil {
		set.MailBase = strings.TrimRight(*in.MailBase, "/")
	}
	if _, err := st.SaveSettings(set); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if err := st.Flush(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.telnyxSettingsView())
}

// apiTelnyxExport writes the finished accounts as "email<TAB>apiKey<TAB>credits".
func (s *Server) apiTelnyxExport(w http.ResponseWriter, r *http.Request) {
	st, err := s.telnyxStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	var b strings.Builder
	for _, row := range st.List() {
		if row.APIKey == "" {
			continue
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\n", row.Email, row.APIKey, row.CreditsUSD)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="telnyx_accounts.txt"`)
	_, _ = w.Write([]byte(b.String()))
}

func (s *Server) apiTelnyxImportCompleted(w http.ResponseWriter, r *http.Request) {
	var req telnyxRegisterRequest
	if err := telnyxDecodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	st, err := s.telnyxStore()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	added := 0
	for _, raw := range strings.Split(req.Text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(fields[0], "@") {
			continue
		}
		row := telnyxsignupstore.NewRow(fields[0])
		row.APIKey = fields[1]
		row.Status = telnyxsignupstore.Keyed
		now := time.Now().UTC()
		row.KeyedAt = now
		row.Stage = string(telnyxreg.StageDone)
		if len(fields) > 2 {
			row.CreditsUSD = strings.TrimPrefix(fields[2], "$")
		}
		if _, aerr := st.Add(row); aerr == nil {
			added++
		}
	}
	_ = st.Flush()
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

// telnyxDecodeJSON reads a bounded JSON body.
func telnyxDecodeJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return errors.New("empty body")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<20))
	if err := dec.Decode(out); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // empty body is fine for start/retry requests
		}
		return err
	}
	return nil
}
