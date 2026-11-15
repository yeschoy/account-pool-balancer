package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/x50reg"
	"account-pool-balancer/internal/x50signupstore"
)

// The 50x AI registration machine (ai.50x.energy).
//
// Unlike the OpenDesign/TokenHarbor flows, 50x needs no captcha and no consent
// switch: the upstream register call takes an email and a password, then the
// account is confirmed through a link mailed to the address. That makes the
// whole machine a single pass — register → wait for mail → verify → create key
// → read the welcome balance — which is why each row is one attempt rather than
// a multi-stage task.
//
// Rows hold real credentials (password, session token, API key). List responses
// go through x50RowView, which reveals the API key only on the explicit
// authenticated reveal action, exactly like TokenHarbor's saved-key reads.

const (
	// x50MaxRows bounds the stored queue so the JSON store and the table stay
	// responsive; older finished rows must be cleared to import more.
	x50MaxRows = 3000
	// x50MaxBatch bounds one import request.
	x50MaxBatch = 500
	// x50LogLines bounds the in-memory log ring.
	x50LogLines = 400
)

// x50BulkInput is one "email----mailbox" line, already split.
type x50BulkInput struct {
	Email      string `json:"email"`
	MailboxURL string `json:"mailboxUrl"`
}

type x50RegisterRequest struct {
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

// x50RowView is the console projection of a row. Credentials other than the
// masked key prefix never appear here.
type x50RowView struct {
	ID            string     `json:"id"`
	Email         string     `json:"email"`
	HasMailbox    bool       `json:"hasMailbox"`
	ProxyLabel    string     `json:"proxyLabel"`
	ProxyBound    bool       `json:"proxyBound"`
	Status        string     `json:"status"`
	StatusText    string     `json:"statusText"`
	Stage         string     `json:"stage,omitempty"`
	KeyPrefix     string     `json:"keyPrefix,omitempty"`
	HasKey        bool       `json:"hasKey"`
	HasSession    bool       `json:"hasSession"`
	Plan          string     `json:"plan,omitempty"`
	Tier          string     `json:"tier,omitempty"`
	BalanceUSD    string     `json:"balanceUsd,omitempty"`
	Error         string     `json:"error,omitempty"`
	Attempts      int        `json:"attempts,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	VerifiedAt    *time.Time `json:"verifiedAt,omitempty"`
	KeyedAt       *time.Time `json:"keyedAt,omitempty"`
	PoolAccountID string     `json:"poolAccountId,omitempty"`
	InPool        bool       `json:"inPool"`
}

// x50StatusText maps status to the console label.
var x50StatusText = map[x50signupstore.Status]string{
	x50signupstore.Queued:    "待注册",
	x50signupstore.Running:   "注册中",
	x50signupstore.Waiting:   "等待确认邮件",
	x50signupstore.Verified:  "已验证·待建 Key",
	x50signupstore.Keyed:     "已完成",
	x50signupstore.Failed:    "失败",
	x50signupstore.Cancelled: "已取消",
}

var x50StageText = map[x50reg.Stage]string{
	x50reg.StageChecking: "检查注册开关",
	x50reg.StageRegister: "提交注册",
	x50reg.StageMailWait: "等待确认邮件",
	x50reg.StageVerify:   "确认邮箱",
	x50reg.StageLogin:    "兜底登录",
	x50reg.StageKey:      "创建 API Key",
	x50reg.StageBilling:  "读取额度",
	x50reg.StageDone:     "完成",
}

func x50ViewRow(r x50signupstore.Row) x50RowView {
	v := x50RowView{
		ID: r.ID, Email: r.Email, HasMailbox: r.MailboxURL != "",
		ProxyLabel: x50ProxyLabel(r.ProxyURL), ProxyBound: r.ProxyURL != "",
		Status: string(r.Status), StatusText: x50StatusText[r.Status],
		KeyPrefix: r.KeyPrefix, HasKey: r.APIKey != "", HasSession: r.SessionToken != "",
		Plan: r.Plan, Tier: r.Tier, BalanceUSD: r.BalanceUSD,
		Error: r.Error, Attempts: r.Attempts,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.Stage != "" {
		if txt, ok := x50StageText[x50reg.Stage(r.Stage)]; ok {
			v.Stage = txt
		} else {
			v.Stage = r.Stage
		}
	}
	if !r.VerifiedAt.IsZero() {
		t := r.VerifiedAt
		v.VerifiedAt = &t
	}
	if !r.KeyedAt.IsZero() {
		t := r.KeyedAt
		v.KeyedAt = &t
	}
	return v
}

// x50ProxyLabel hides credentials but keeps the gateway identity visible.
func x50ProxyLabel(raw string) string {
	if raw == "" {
		return ""
	}
	scheme := ""
	if i := strings.Index(raw, "://"); i >= 0 {
		scheme = raw[:i+3]
		raw = raw[i+3:]
	}
	if i := strings.LastIndex(raw, "@"); i >= 0 {
		raw = raw[i+1:]
	}
	if j := strings.IndexAny(raw, "?#"); j >= 0 {
		raw = raw[:j]
	}
	if j := strings.Index(raw, "/"); j >= 0 {
		raw = raw[:j]
	}
	return scheme + raw
}

type x50LogLine struct {
	At      time.Time `json:"at"`
	Type    string    `json:"type"`
	Email   string    `json:"email,omitempty"`
	Message string    `json:"message,omitempty"`
}

// x50Manager owns the store, the worker budget and in-flight cancellation.
type x50Manager struct {
	srv   *Server
	store *x50signupstore.Store

	mu      sync.Mutex
	running map[string]context.CancelFunc
	logs    []x50LogLine
	logMu   sync.Mutex
	// pending counts rows queued by any start request so the pool size is
	// shared rather than per-request.
	pending int
	// base derives every job context. It comes from the server, never from an
	// HTTP request: a request context is cancelled the moment the handler
	// returns, which would kill the background registration it just queued.
	base context.Context
	wg   sync.WaitGroup
	stop chan struct{}
}

func (s *Server) x50Store() (*x50signupstore.Store, error) {
	s.x50Mu.Lock()
	defer s.x50Mu.Unlock()
	if s.x50StoreRef != nil {
		return s.x50StoreRef, nil
	}
	st, err := x50signupstore.Open(filepath.Join(s.dataDir, "x50-signup.json"))
	if err != nil {
		return nil, err
	}
	s.x50StoreRef = st
	return st, nil
}

func (s *Server) x50Mgr() (*x50Manager, error) {
	st, err := s.x50Store()
	if err != nil {
		return nil, err
	}
	s.x50Mu.Lock()
	defer s.x50Mu.Unlock()
	if s.x50MgrRef != nil {
		return s.x50MgrRef, nil
	}
	m := &x50Manager{
		srv:     s,
		store:   st,
		running: map[string]context.CancelFunc{},
		base:    s.x50BaseCtx(),
		stop:    make(chan struct{}),
	}
	s.x50MgrRef = m
	return m, nil
}

var x50LogSecret = regexp.MustCompile(`(?i)(?:authorization\s*[:=]\s*(?:Token|Bearer)\s+|\b(?:session_token|api_key|password|token)\s*["']?\s*[:=]\s*["']?)[^\s"'&,}]+`)

func (m *x50Manager) safeText(text string, extra ...string) string {
	if m.srv != nil {
		text = redactSecrets(m.srv.cfg.Get(), text)
	}
	if m.store != nil {
		for _, r := range m.store.Snapshot() {
			extra = append(extra, r.APIKey, r.SessionToken, r.Password, r.MailboxURL, r.MailboxToken, r.ProxyURL)
		}
	}
	for _, v := range extra {
		if v != "" {
			text = strings.ReplaceAll(text, v, "[REDACTED]")
		}
	}
	return x50LogSecret.ReplaceAllString(text, "[REDACTED]")
}
func (m *x50Manager) addLog(typ, email, msg string) {
	msg = m.safeText(msg)
	m.logMu.Lock()
	defer m.logMu.Unlock()
	m.logs = append(m.logs, x50LogLine{At: time.Now(), Type: typ, Email: email, Message: msg})
	if len(m.logs) > x50LogLines {
		m.logs = m.logs[len(m.logs)-x50LogLines:]
	}
}

// Logs returns the ring buffer newest-first.
func (m *x50Manager) Logs() []x50LogLine {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	out := make([]x50LogLine, len(m.logs))
	copy(out, m.logs)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// ---- proxy assignment -------------------------------------------------------

// assignProxies binds the next proxy, in order, to every row that has none.
//
// The rule the console promises: one proxy per account, proxies reusable, and
// assignment walks the pool in order. Concretely the row gets the proxy with
// the fewest rows already bound, breaking ties by pool position — which
// degenerates to strict round-robin for a fresh import and stays balanced when
// rows are added later.
func assignProxies(existing []x50signupstore.Row, targets []x50signupstore.Row, pool []string) map[string]string {
	out := map[string]string{}
	if len(pool) == 0 {
		return out
	}
	index := make(map[string]int, len(pool))
	for i, p := range pool {
		index[p] = i
	}
	// Cursor = one past the last proxy actually handed out, so a later import
	// continues the rotation instead of restarting at index 0.
	cursor := -1
	for _, r := range existing {
		if i, ok := index[r.ProxyURL]; ok && i > cursor {
			cursor = i
		}
	}
	for _, r := range targets {
		if r.ProxyURL != "" {
			continue
		}
		cursor = (cursor + 1) % len(pool)
		out[r.ID] = pool[cursor]
	}
	return out
}

// x50ProxyCursor returns the index of the last bound proxy, i.e. one before the
// next assignment. Returns -1 for a fresh pool.
func x50ProxyCursor(existing []x50signupstore.Row, pool []string) int {
	index := make(map[string]int, len(pool))
	for i, p := range pool {
		index[p] = i
	}
	cursor := -1
	for _, r := range existing {
		if i, ok := index[r.ProxyURL]; ok && i > cursor {
			cursor = i
		}
	}
	return cursor
}

// ---- runner -----------------------------------------------------------------

// x50RunJob is one queued registration.
type x50RunJob struct {
	RowID    string
	Email    string
	Mailbox  x50reg.Mailbox
	Password string
	Proxy    string
}

// startRows queues the given rows and runs them with a shared concurrency cap.
//
// Running rows are never duplicated: a row already registered is skipped rather
// than replayed, because a replay would create a second API key on the same
// account.
func (m *x50Manager) startRows(ids []string, concurrency int, settings x50signupstore.Settings) (int, error) {
	keyName := settings.KeyName
	mailWait := time.Duration(settings.MailTimeoutSec) * time.Second
	mailPoll := time.Duration(settings.MailPollSec) * time.Second
	reqTimeout := time.Duration(settings.RequestTimeoutSec) * time.Second
	apiBase := settings.APIBase
	mailBase := settings.MailBase
	if concurrency <= 0 {
		concurrency = x50signupstore.DefaultSettings().Concurrency
	}
	concurrency = max(x50signupstore.MinConcurrency, min(concurrency, x50signupstore.MaxConcurrency))

	var jobs []x50RunJob
	seenID := map[string]bool{}
	for _, id := range ids {
		if seenID[id] {
			continue
		}
		seenID[id] = true
		row, err := m.store.Get(id)
		if err != nil {
			continue
		}
		switch row.Status {
		case x50signupstore.Keyed:
			continue // already complete: never replay and mint a second key
		}
		m.mu.Lock()
		_, busy := m.running[id]
		if busy {
			m.mu.Unlock()
			continue
		}
		runCtx, cancel := context.WithCancel(m.base)
		m.running[id] = cancel
		m.pending++
		m.mu.Unlock()

		mbTok := row.MailboxToken
		if mbTok == "" {
			mbTok = x50TokenFromURL(row.MailboxURL)
		}
		job := x50RunJob{
			RowID: id, Email: row.Email,
			Mailbox:  x50reg.Mailbox{Email: row.Email, Token: mbTok},
			Password: row.Password, Proxy: row.ProxyURL,
		}
		// Mark running before the goroutine starts so a concurrent start
		// request observes the row as busy.
		if _, uerr := m.store.Update(id, func(r *x50signupstore.Row) error {
			r.Status = x50signupstore.Running
			r.Stage = string(x50reg.StageChecking)
			r.Error = ""
			return nil
		}); uerr != nil {
			m.mu.Lock()
			delete(m.running, id)
			m.pending--
			m.mu.Unlock()
			cancel()
			continue
		}
		jobs = append(jobs, job)
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runOne(runCtx, job, keyName, mailWait, mailPoll, reqTimeout, apiBase, mailBase)
		}()
	}
	return len(jobs), nil
}

func (m *x50Manager) runOne(ctx context.Context, job x50RunJob, keyName string, mailWait, mailPoll, reqTimeout time.Duration, apiBase, mailBase string) {
	defer func() {
		m.mu.Lock()
		if cancel, ok := m.running[job.RowID]; ok {
			cancel()
		}
		delete(m.running, job.RowID)
		m.pending--
		m.mu.Unlock()
	}()

	password := job.Password
	if password == "" {
		password = x50reg.RandomPassword()
	}

	reg, err := x50reg.New(x50reg.Config{
		APIBase:      apiBase,
		MailBase:     mailBase,
		ProxyURL:     job.Proxy,
		Email:        job.Email,
		Password:     password,
		Mailbox:      job.Mailbox,
		KeyName:      keyName,
		MailWait:     mailWait,
		MailInterval: mailPoll,
		Timeout:      reqTimeout,
	})
	if err != nil {
		m.finish(job.RowID, x50signupstore.Failed, "", err)
		return
	}
	reg.SetProgress(func(stage x50reg.Stage, detail string) {
		_, _ = m.store.Update(job.RowID, func(r *x50signupstore.Row) error {
			r.Stage = string(stage)
			r.Password = password
			if stage == x50reg.StageMailWait {
				r.MailboxToken = job.Mailbox.Token
			}
			switch stage {
			case x50reg.StageMailWait:
				r.Status = x50signupstore.Waiting
			case x50reg.StageVerify:
				r.Status = x50signupstore.Waiting
			}
			return nil
		})
	})
	reg.SetTracer(func(f string, a ...any) {
		m.addLog("info", job.Email, m.safeText(fmt.Sprintf(f, a...), password, job.Proxy, job.Mailbox.Token))
	})

	acc, err := reg.Register(ctx)
	if err != nil {
		err = errors.New(m.safeText(err.Error(), password, job.Proxy, job.Mailbox.Token))
		m.addLog("error", job.Email, err.Error())
		m.finish(job.RowID, x50signupstore.Failed, "", err)
		return
	}
	m.addLog("ok", job.Email, "注册完成，余额 "+acc.BalanceUSD+" USD")
	m.finish(job.RowID, x50signupstore.Keyed, password, nil, func(r *x50signupstore.Row) {
		r.SessionToken = acc.SessionToken
		r.APIKey = acc.APIKey
		r.KeyID = acc.KeyID
		r.KeyPrefix = acc.KeyPrefix
		r.Plan = acc.Plan
		r.Tier = acc.Tier
		r.BalanceUSD = acc.BalanceUSD
		r.VerifiedAt = time.Now().UTC()
		r.KeyedAt = time.Now().UTC()
		r.Stage = string(x50reg.StageDone)
	})
}

// finish records the terminal state of one attempt. Optional mutators run
// before the status/error fields are written.
func (m *x50Manager) finish(id string, status x50signupstore.Status, password string, cause error, mutators ...func(*x50signupstore.Row)) {
	message := m.safeText(x50ErrorText(cause), password)
	_, _ = m.store.Update(id, func(r *x50signupstore.Row) error {
		for _, fn := range mutators {
			fn(r)
		}
		if password != "" {
			r.Password = password
		}
		r.Status = status
		r.Attempts++
		if status == x50signupstore.Failed {
			r.Error = message
			r.Stage = ""
		} else {
			r.Error = ""
		}
		return nil
	})
}

// x50ErrorText keeps the reason readable and bounded; callers never pass raw
// credentials into it.
func x50ErrorText(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	// Trim the request-path noise that the Go HTTP client adds.
	msg = strings.ReplaceAll(msg, "\n", " ")
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	return msg
}

// stopX50 cancels in-flight registrations and waits for them, so nothing touches
// the store after the data directory is torn down.
func (s *Server) stopX50() {
	s.x50Mu.Lock()
	m := s.x50MgrRef
	s.x50Mu.Unlock()
	if m == nil {
		return
	}
	// Cancel the root first so a job that has not reached runOne yet also stops,
	// then the per-row cancels for everything already in flight.
	s.x50RootMu.Lock()
	if s.x50Cancel != nil {
		s.x50Cancel()
	}
	s.x50RootMu.Unlock()
	m.mu.Lock()
	for _, cancel := range m.running {
		cancel()
	}
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// x50TokenFromURL extracts the mailbox share token.
func x50TokenFromURL(raw string) string {
	mb, err := x50reg.ParseMailboxLine("x@x----" + raw)
	if err == nil {
		return mb.Token
	}
	if mb, err := x50reg.ParseMailboxLine(raw); err == nil {
		return mb.Token
	}
	return ""
}

// ---- handlers ---------------------------------------------------------------

func (s *Server) registerX50(_ *http.ServeMux, a func(string, http.HandlerFunc)) {
	a("GET /api/x50/candidates", s.apiX50Candidates)
	a("POST /api/x50/pool/import", s.apiX50PoolImport)
	a("POST /api/x50/pool/proxy", s.apiX50PoolProxy)
	a("POST /api/x50/candidates/bulk", s.apiX50BulkImport)
	a("POST /api/x50/candidates/import-completed", s.apiX50ImportCompleted)
	a("DELETE /api/x50/candidates/{id}", s.apiX50DeleteRow)
	a("POST /api/x50/candidates/{id}/register", s.apiX50RegisterOne)
	a("POST /api/x50/candidates/{id}/retry", s.apiX50RetryOne)
	a("POST /api/x50/candidates/bulk-register", s.apiX50RegisterPending)
	a("POST /api/x50/candidates/clear-finished", s.apiX50ClearFinished)
	a("GET /api/x50/candidates/{id}/key", s.apiX50RevealKey)
	a("GET /api/x50/status", s.apiX50Status)
	a("GET /api/x50/logs", s.apiX50Logs)
	a("GET /api/x50/settings", s.apiX50Settings)
	a("PUT /api/x50/settings", s.apiX50SaveSettings)
	a("GET /api/x50/export", s.apiX50Export)
}

func (s *Server) x50StatusPayload(m *x50Manager, st *x50signupstore.Store) map[string]any {
	rows := st.List()
	counts := map[string]int{}
	withKey, withProxy := 0, 0
	for _, r := range rows {
		counts[string(r.Status)]++
		if r.APIKey != "" {
			withKey++
		}
		if r.ProxyURL != "" {
			withProxy++
		}
	}
	m.mu.Lock()
	busy := len(m.running)
	m.mu.Unlock()
	return map[string]any{
		"total": len(rows), "counts": counts,
		"queued":   counts[string(x50signupstore.Queued)],
		"running":  counts[string(x50signupstore.Running)],
		"waiting":  counts[string(x50signupstore.Waiting)],
		"verified": counts[string(x50signupstore.Verified)],
		"keyed":    counts[string(x50signupstore.Keyed)],
		"failed":   counts[string(x50signupstore.Failed)],
		"withKey":  withKey, "withProxy": withProxy, "busy": busy,
	}
}

func (s *Server) apiX50Candidates(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	st := m.store
	rows := st.List()
	views := make([]x50RowView, 0, len(rows))
	cfg := s.cfg.Get()
	for _, row := range rows {
		v := x50ViewRow(row)
		v.PoolAccountID = x50AccountFor(cfg, row.APIKey)
		v.InPool = v.PoolAccountID != ""
		views = append(views, v)
	}
	settings := st.Settings()
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":     views,
		"settings": x50SettingsView(settings),
		"status":   s.x50StatusPayload(m, st),
		"limits": map[string]any{
			"maxRows": x50MaxRows, "maxBatch": x50MaxBatch,
			"minConcurrency": x50signupstore.MinConcurrency,
			"maxConcurrency": x50signupstore.MaxConcurrency,
		},
	})
}

// x50SettingsView never returns the proxy credentials.
func x50SettingsView(in x50signupstore.Settings) map[string]any {
	labels := make([]string, 0, len(in.ProxyPool))
	for _, p := range in.ProxyPool {
		labels = append(labels, x50ProxyLabel(p))
	}
	return map[string]any{
		"concurrency":       in.Concurrency,
		"mailTimeoutSec":    in.MailTimeoutSec,
		"mailPollSec":       in.MailPollSec,
		"requestTimeoutSec": in.RequestTimeoutSec,
		"keyName":           in.KeyName,
		"autoBindProxy":     in.AutoBindProxy,
		"passwordMode":      in.PasswordMode,
		"apiBase":           in.APIBase,
		"mailBase":          in.MailBase,
		"proxyCount":        len(in.ProxyPool),
		"proxyLabels":       labels,
	}
}

func (s *Server) apiX50BulkImport(w http.ResponseWriter, r *http.Request) {
	var in x50RegisterRequest
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	st := m.store

	lines := parseX50Lines(in.Text)
	if len(lines) == 0 {
		apiError(w, http.StatusBadRequest, errors.New("没有可导入的账号：每行格式为 email----接码地址"))
		return
	}
	if len(lines) > x50MaxBatch {
		apiError(w, http.StatusBadRequest, fmt.Errorf("单次最多导入 %d 行", x50MaxBatch))
		return
	}

	settings := st.Settings()
	if in.Proxies != nil {
		settings.ProxyPool = normalizeProxies(in.Proxies)
	}
	if len(st.Snapshot())+len(lines) > x50MaxRows {
		apiError(w, http.StatusBadRequest, fmt.Errorf("队列上限 %d，请先清理已完成账号", x50MaxRows))
		return
	}

	if in.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{
			"dryRun": true, "parsed": len(lines),
			"proxies": len(normalizeProxies(in.Proxies)),
			"sample":  firstN(lines, 5),
		})
		return
	}

	rows := make([]x50signupstore.Row, 0, len(lines))
	for _, l := range lines {
		rows = append(rows, x50signupstore.Row{
			Email: l.Email, MailboxURL: l.MailboxURL,
			MailboxToken: x50TokenFromURL(l.MailboxURL),
			Status:       x50signupstore.Queued,
		})
	}
	// Bind proxies before persisting: a row must never exist without its egress.
	// Assignment walks the pool in order, continuing where the stored rows left
	// off, so a second import does not restart at the first proxy.
	if settings.AutoBindProxy && len(settings.ProxyPool) > 0 {
		cursor := x50ProxyCursor(st.Snapshot(), settings.ProxyPool)
		for i := range rows {
			cursor = (cursor + 1) % len(settings.ProxyPool)
			rows[i].ProxyURL = settings.ProxyPool[cursor]
		}
	}

	added, errs, err := st.AddMany(rows)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if in.Proxies != nil {
		if _, err := st.SaveSettings(settings); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	m.addLog("info", "", fmt.Sprintf("导入 %d 个账号，跳过 %d 个", len(added), len(errs)))
	writeJSON(w, http.StatusOK, map[string]any{
		"added": len(added), "skipped": len(errs), "errors": errs,
		"rows": viewsOf(added),
	})
}

func viewsOf(rows []x50signupstore.Row) []x50RowView {
	out := make([]x50RowView, 0, len(rows))
	for _, r := range rows {
		out = append(out, x50ViewRow(r))
	}
	return out
}

// parseX50Lines accepts `email----mailbox`, tab-separated, or two whitespace
// fields, skipping blanks and comments.
func parseX50Lines(text string) []x50BulkInput {
	var out []x50BulkInput
	seen := map[string]bool{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var email, mailbox string
		switch {
		case strings.Contains(line, "----"):
			parts := strings.SplitN(line, "----", 2)
			email, mailbox = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		case strings.Contains(line, "\t"):
			parts := strings.SplitN(line, "\t", 2)
			email, mailbox = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		default:
			continue
		}
		if email == "" || mailbox == "" || !strings.Contains(email, "@") {
			continue
		}
		if x50TokenFromURL(mailbox) == "" {
			continue // a share link without a token cannot be read
		}
		key := strings.ToLower(email)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, x50BulkInput{Email: email, MailboxURL: mailbox})
	}
	return out
}

func normalizeProxies(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "#") || seen[p] {
			continue
		}
		if !strings.Contains(p, "://") {
			p = "socks5://" + p
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func firstN(in []x50BulkInput, n int) []x50BulkInput {
	if len(in) <= n {
		return in
	}
	return in[:n]
}

func (s *Server) apiX50DeleteRow(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	m.mu.Lock()
	cancel, busy := m.running[id]
	m.mu.Unlock()
	if busy {
		cancel()
	}
	if err := m.store.Delete(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiX50RegisterOne queues exactly one row.
func (s *Server) apiX50RegisterOne(w http.ResponseWriter, r *http.Request) {
	s.x50Start(w, r, []string{r.PathValue("id")})
}

// apiX50RetryOne re-queues a row in a retryable state.
func (s *Server) apiX50RetryOne(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	row, gerr := m.store.Get(id)
	if gerr != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": gerr.Error()})
		return
	}
	if !row.Status.Retryable() {
		apiError(w, http.StatusBadRequest, fmt.Errorf("当前状态「%s」不可重试；已完成账号不会重复注册以免生成第二个 Key", x50StatusText[row.Status]))
		return
	}
	s.x50Start(w, r, []string{id})
}

// apiX50RegisterPending starts every retryable row.
func (s *Server) apiX50RegisterPending(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var ids []string
	for _, row := range m.store.List() {
		if row.Status.Retryable() {
			ids = append(ids, row.ID)
		}
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"queued": 0, "message": "没有待注册或可重试的账号"})
		return
	}
	s.x50Start(w, r, ids)
}

func (s *Server) x50Start(w http.ResponseWriter, r *http.Request, ids []string) {
	var in x50RegisterRequest
	if r.Body != nil {
		if err := readBody(r, &in); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
	}
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	settings := m.store.Settings()
	if in.Proxies != nil {
		settings.ProxyPool = normalizeProxies(in.Proxies)
		if _, err := m.store.SaveSettings(settings); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
	}
	conc := settings.Concurrency
	if in.Concurrency != 0 {
		if in.Concurrency < 0 || in.Concurrency > x50signupstore.MaxConcurrency {
			apiError(w, http.StatusBadRequest, fmt.Errorf("并发必须为 %d-%d；0 表示使用默认设置", x50signupstore.MinConcurrency, x50signupstore.MaxConcurrency))
			return
		}
		conc = in.Concurrency
	}
	if len(in.Seeds) > 0 {
		ids = in.Seeds
	}
	settings.Concurrency = conc
	queued, err := m.startRows(ids, conc, settings)
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued, "concurrency": conc})
}

func (s *Server) apiX50ClearFinished(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	n, err := m.store.ClearFinished()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": n})
}

// apiX50RevealKey is the only read that returns a plaintext API key.
func (s *Server) apiX50RevealKey(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	row, err := m.store.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if row.APIKey == "" {
		apiError(w, http.StatusBadRequest, errors.New("该账号还没有 API Key"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": row.ID, "email": row.Email, "apiKey": row.APIKey})
}

func (s *Server) apiX50Status(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, s.x50StatusPayload(m, m.store))
}

func (s *Server) apiX50Logs(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": m.Logs()})
}

func (s *Server) apiX50Settings(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": x50SettingsView(m.store.Settings())})
}

func (s *Server) apiX50SaveSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Concurrency       int      `json:"concurrency"`
		MailTimeoutSec    int      `json:"mailTimeoutSec"`
		MailPollSec       int      `json:"mailPollSec"`
		RequestTimeoutSec int      `json:"requestTimeoutSec"`
		KeyName           string   `json:"keyName"`
		APIBase           string   `json:"apiBase"`
		MailBase          string   `json:"mailBase"`
		AutoBindProxy     *bool    `json:"autoBindProxy"`
		PasswordMode      string   `json:"passwordMode"`
		Proxies           []string `json:"proxies"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if in.Concurrency < 0 || in.Concurrency > x50signupstore.MaxConcurrency {
		apiError(w, http.StatusBadRequest, fmt.Errorf("并发必须为 %d-%d；0 表示使用默认值", x50signupstore.MinConcurrency, x50signupstore.MaxConcurrency))
		return
	}
	cur := m.store.Settings()
	next := x50signupstore.Settings{
		Concurrency:       in.Concurrency,
		MailTimeoutSec:    in.MailTimeoutSec,
		MailPollSec:       in.MailPollSec,
		RequestTimeoutSec: in.RequestTimeoutSec,
		KeyName:           strings.TrimSpace(in.KeyName),
		APIBase:           strings.TrimSpace(in.APIBase),
		MailBase:          strings.TrimSpace(in.MailBase),
		PasswordMode:      in.PasswordMode,
		ProxyPool:         cur.ProxyPool,
		AutoBindProxy:     cur.AutoBindProxy,
	}
	if in.Proxies != nil {
		next.ProxyPool = normalizeProxies(in.Proxies)
	}
	if in.AutoBindProxy != nil {
		next.AutoBindProxy = *in.AutoBindProxy
	}
	saved, err := m.store.SaveSettings(next)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": x50SettingsView(saved)})
}

// apiX50Export renders `email \t password \t apiKey` one account per line so the
// result can be pasted straight into a downstream import box.
func (s *Server) apiX50Export(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="x50-accounts.txt"`)
	var b strings.Builder
	for _, row := range m.store.List() {
		if row.APIKey == "" {
			continue
		}
		b.WriteString(row.Email)
		b.WriteByte('\t')
		b.WriteString(row.Password)
		b.WriteByte('\t')
		b.WriteString(row.APIKey)
		b.WriteByte('\n')
	}
	_, _ = w.Write([]byte(b.String()))
}

// ---- completed-account import ----------------------------------------------

// x50CompletedInput is one account that was registered elsewhere and only needs
// to be recorded, not re-registered.
//
// This exists because a registration pass runs on a machine that may not be the
// console host: keys minted there are real, and recreating them here would mint
// a second key for the same account. The import records the observed result
// verbatim and never contacts upstream.
type x50CompletedInput struct {
	Email        string `json:"email"`
	MailboxURL   string `json:"mailboxUrl,omitempty"`
	Password     string `json:"password"`
	ProxyURL     string `json:"proxyUrl,omitempty"`
	SessionToken string `json:"sessionToken,omitempty"`
	APIKey       string `json:"apiKey"`
	KeyID        int64  `json:"keyId,omitempty"`
	KeyPrefix    string `json:"keyPrefix,omitempty"`
	Plan         string `json:"plan,omitempty"`
	Tier         string `json:"tier,omitempty"`
	BalanceUSD   string `json:"balanceUsd,omitempty"`
	Note         string `json:"note,omitempty"`
}

type x50ImportCompletedRequest struct {
	Accounts []x50CompletedInput `json:"accounts"`
	DryRun   bool                `json:"dryRun,omitempty"`
}

// x50CompletedImportResult reports what the import did per account.
type x50CompletedImportResult struct {
	Added   int          `json:"added"`
	Skipped int          `json:"skipped"`
	Errors  []string     `json:"errors"`
	Rows    []x50RowView `json:"rows"`
}

// apiX50ImportCompleted records already-registered accounts with their keys.
//
// Validation rejects an entry before anything is persisted so a malformed batch
// cannot leave half a queue behind. An existing row is never overwritten: the
// console already owns that row's state and key.
func (s *Server) apiX50ImportCompleted(w http.ResponseWriter, r *http.Request) {
	m, err := s.x50Mgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var in x50ImportCompletedRequest
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if len(in.Accounts) == 0 {
		apiError(w, http.StatusBadRequest, errors.New("没有要导入的已完成账号"))
		return
	}
	if len(in.Accounts) > x50MaxBatch {
		apiError(w, http.StatusBadRequest, fmt.Errorf("单次最多导入 %d 个账号", x50MaxBatch))
		return
	}

	existing := m.store.Snapshot()
	seen := map[string]bool{}
	for _, row := range existing {
		seen[strings.ToLower(row.Email)] = true
	}

	now := time.Now().UTC()
	rows := make([]x50signupstore.Row, 0, len(in.Accounts))
	var errs []string
	for _, a := range in.Accounts {
		email := strings.TrimSpace(a.Email)
		key := strings.TrimSpace(a.APIKey)
		switch {
		case email == "" || !strings.Contains(email, "@"):
			errs = append(errs, "缺少有效邮箱")
			continue
		case key == "":
			errs = append(errs, email+": 缺少 api_key")
			continue
		case !strings.HasPrefix(key, "sk-50x-"):
			errs = append(errs, email+": api_key 不是 sk-50x- 前缀，拒绝写入")
			continue
		}
		if seen[strings.ToLower(email)] {
			errs = append(errs, email+": 该邮箱已存在，未覆盖")
			continue
		}
		seen[strings.ToLower(email)] = true
		row := x50signupstore.Row{
			Email: email, MailboxURL: strings.TrimSpace(a.MailboxURL),
			MailboxToken: x50TokenFromURL(strings.TrimSpace(a.MailboxURL)),
			Password:     a.Password, ProxyURL: strings.TrimSpace(a.ProxyURL),
			Status:       x50signupstore.Keyed,
			SessionToken: a.SessionToken, APIKey: key, KeyID: a.KeyID,
			KeyPrefix: a.KeyPrefix, Plan: a.Plan, Tier: a.Tier, BalanceUSD: a.BalanceUSD,
			Stage:      string(x50reg.StageDone),
			VerifiedAt: now, KeyedAt: now,
			Attempts: 1,
		}
		if row.KeyPrefix == "" && len(key) > 12 {
			row.KeyPrefix = key[:12]
		}
		rows = append(rows, row)
	}

	if in.DryRun {
		writeJSON(w, http.StatusOK, x50CompletedImportResult{
			Added: len(rows), Skipped: len(errs), Errors: errs, Rows: viewsOf(rows),
		})
		return
	}
	added, addErrs, err := m.store.AddMany(rows)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	errs = append(errs, addErrs...)
	m.addLog("info", "", fmt.Sprintf("导入 %d 个已完成账号，跳过 %d 个", len(added), len(errs)))
	writeJSON(w, http.StatusOK, x50CompletedImportResult{
		Added: len(added), Skipped: len(errs), Errors: errs, Rows: viewsOf(added),
	})
}
