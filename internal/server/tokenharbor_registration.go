package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/thcaptcha"
	"account-pool-balancer/internal/thmailbox"
	"account-pool-balancer/internal/thregister"
	"account-pool-balancer/internal/thsignupstore"
)

// The Token Harbor registration machine.
//
// One "attempt" is one submission of the form — a batch of mailboxes that
// share an egress plan. Rows move queued → running → registered → imported,
// and any step can end in failed with a reason. Registration and import stay
// separate for the same reason they do in the Perch pool: a batch can be
// registered ahead of time and imported later, deliberately.
//
// Credentials never cross this file's HTTP boundary: rows carry a password
// and a key, the API returns thsignupstore.Summary (masked key, credential-free
// proxy label) and the pool account gets the key through config, as any pool's
// account would.

// thRegistrationJobs bounds how many attempts are kept in the list.
const thRegistrationJobs = 50

// thRegisterInput is one pasted line: 邮箱----密码----接码页
type thRegisterInput struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	MailboxURL string `json:"mailboxUrl,omitempty"`
	// ProxyURL overrides the egress for this one mailbox.
	ProxyURL string `json:"proxyUrl,omitempty"`
}

type thRegisterRequest struct {
	Accounts   []thRegisterInput `json:"accounts"`
	Proxies    []string          `json:"proxies,omitempty"`
	Group      string            `json:"group,omitempty"`
	AutoImport bool              `json:"autoImport"`
	// SolveCaptcha buys a Turnstile token when the site asks for one. Off by
	// default: the caller pays for each attempt it enables.
	SolveCaptcha bool `json:"solveCaptcha"`
	// Concurrency overrides this task's simultaneous registrations (0 = the
	// setting). It never exceeds thMaxConcurrency, and the whole server still
	// shares one budget.
	Concurrency int `json:"concurrency,omitempty"`
}

// thMaxConcurrency bounds per-task parallelism: every slot can spend money at
// the captcha service and one mailbox, so the ceiling is a cost guard.
const thMaxConcurrency = 10

// thRegistrationView is the console's attempt row: counts plus summaries.
type thRegistrationView struct {
	ID        string                  `json:"id"`
	CreatedAt time.Time               `json:"createdAt"`
	Group     string                  `json:"group"`
	Running   bool                    `json:"running"`
	Counts    map[string]int          `json:"counts"`
	Rows      []thsignupstore.Summary `json:"rows"`
}

// thRegistrar runs rows in the background. It owns the concurrency budget so
// two attempts cannot each open the full set of registrations.
type thRegistrar struct {
	srv *Server
	mu  sync.Mutex
	sem chan struct{}
}

func (s *Server) thRegistrar() *thRegistrar {
	s.thRegMu.Lock()
	defer s.thRegMu.Unlock()
	if s.thReg == nil {
		s.thReg = &thRegistrar{srv: s}
	}
	return s.thReg
}

// budget returns the concurrency limiter, rebuilding it when settings change.
// A zero or negative setting means one at a time.
func (r *thRegistrar) budget(n int) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n <= 0 {
		n = 1
	}
	if r.sem == nil || cap(r.sem) != n {
		r.sem = make(chan struct{}, n)
	}
	return r.sem
}

// ---- API ---------------------------------------------------------------------

func (s *Server) registerTokenHarbor(mux *http.ServeMux, a func(string, http.HandlerFunc)) {
	a("POST /api/tokenharbor/registrations", s.apiTHRegister)
	a("GET /api/tokenharbor/registrations", s.apiTHRegistrations)
	a("GET /api/tokenharbor/registrations/{id}", s.apiTHRegistration)
	a("PUT /api/tokenharbor/registrations/{id}/proxy", s.apiTHRowProxy)
	a("POST /api/tokenharbor/registrations/{id}/import", s.apiTHImport)
	a("POST /api/tokenharbor/registrations/{id}/retry", s.apiTHRetry)
	a("POST /api/tokenharbor/registrations/{id}/reverify", s.apiTHReverify)
	a("GET /api/tokenharbor/log", s.apiTHLog)
	a("DELETE /api/tokenharbor/registrations/{id}", s.apiTHDeleteRow)
	a("GET /api/tokenharbor/settings", s.apiTHSettings)
	a("PUT /api/tokenharbor/settings", s.apiTHSaveSettings)
}

// apiTHRegister queues one batch. Validation happens before anything is
// persisted, and before any money is spent on a captcha.
func (s *Server) apiTHRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in thRegisterRequest
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	if len(in.Accounts) == 0 || len(in.Accounts) > 100 {
		apiError(w, http.StatusBadRequest, errors.New("请提交 1-100 个邮箱"))
		return
	}
	proxies := make([]string, 0, len(in.Proxies))
	for _, p := range in.Proxies {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if err := thsignupstore.ValidateProxy(p); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
		proxies = append(proxies, p)
	}
	rows := make([]thsignupstore.Row, 0, len(in.Accounts))
	seen := map[string]bool{}
	for _, acct := range in.Accounts {
		email, mailbox := thmailbox.ParseLine(strings.TrimSpace(acct.Email))
		if acct.MailboxURL != "" {
			mailbox = strings.TrimSpace(acct.MailboxURL)
		}
		email = strings.ToLower(email)
		if !strings.Contains(email, "@") || strings.ContainsAny(email, " \t") {
			apiError(w, http.StatusBadRequest, fmt.Errorf("邮箱格式无效: %q", email))
			return
		}
		if seen[email] {
			apiError(w, http.StatusBadRequest, fmt.Errorf("邮箱重复: %s", email))
			return
		}
		seen[email] = true
		if strings.TrimSpace(acct.Password) == "" && strings.TrimSpace(acct.ProxyURL) == "" && mailbox == "" {
			// thRegisterInput may also carry one raw "邮箱----密码----接码页" line.
			parts := strings.Split(strings.TrimSpace(acct.Email), "----")
			if len(parts) >= 2 {
				email, mailbox = thmailbox.ParseLine(strings.TrimSpace(acct.Email))
				_ = parts
			}
		}
		if err := thmailbox.Validate(mailbox); err != nil {
			apiError(w, http.StatusBadRequest, fmt.Errorf("%s: %v", email, err))
			return
		}
		if p := strings.TrimSpace(acct.ProxyURL); p != "" {
			if err := thsignupstore.ValidateProxy(p); err != nil {
				apiError(w, http.StatusBadRequest, err)
				return
			}
		}
		rows = append(rows, thsignupstore.Row{
			Email:      email,
			Password:   strings.TrimSpace(acct.Password),
			MailboxURL: mailbox,
			ProxyURL:   strings.TrimSpace(acct.ProxyURL),
		})
	}
	// Round-robin the proxy list so one IP never carries a whole batch.
	cfg := s.cfg.Get().TokenHarbor
	for i := range rows {
		if rows[i].ProxyURL != "" {
			continue
		}
		if n := len(proxies); n > 0 {
			rows[i].ProxyURL = proxies[i%n]
		} else {
			rows[i].ProxyURL = cfg.ProxyURL
		}
	}
	group := strings.TrimSpace(in.Group)
	if group == "" {
		group = cfg.Group
	}
	if group == "" {
		group = config.TokenHarborGroup
	}
	for i := range rows {
		rows[i].Group = group
	}
	attempt := config.NewID("that_")
	summaries, err := store.Add(attempt, rows)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	conc := in.Concurrency
	if conc > 0 {
		if conc > thMaxConcurrency {
			conc = thMaxConcurrency
		}
	} else {
		conc = cfg.RegisterConcurrency
	}
	s.thLive("", "任务创建", fmt.Sprintf("%d 个邮箱，并发 %d，代理 %d 个", len(rows), conc, len(proxies)), "", "info")
	s.thRegisterAttempt(attempt, group, in.AutoImport, in.SolveCaptcha, conc, summaries)
	writeJSON(w, http.StatusAccepted, map[string]any{"id": attempt, "rows": summaries})
}

// thRegisterAttempt starts the background work for the queued rows.
func (s *Server) thRegisterAttempt(attempt, group string, autoImport, solve bool, conc int, rows []thsignupstore.Summary) {
	reg := s.thRegistrar()
	if conc <= 0 {
		conc = s.cfg.Get().TokenHarbor.RegisterConcurrency
	}
	for _, row := range rows {
		if row.Status != thsignupstore.Queued || row.Attempt != attempt {
			continue
		}
		id := row.ID
		// Every row gets its own goroutine; the per-attempt budget below is
		// what actually limits simultaneity, so a queued row waits instead of
		// being dropped (and its place in the line is kept).
		s.thTasks.Add(1)
		go func() {
			defer s.thTasks.Done()
			_ = reg.run(id, group, autoImport, solve, conc)
		}()
	}
}

// run registers one row and, when asked, imports it into the pool.
func (r *thRegistrar) run(id, group string, autoImport, solve bool, conc int) error {
	s := r.srv
	store, err := s.thStore()
	if err != nil {
		return err
	}
	cfg := s.cfg.Get().TokenHarbor
	if conc <= 0 {
		conc = cfg.RegisterConcurrency
	}
	sem := r.budget(conc)

	select {
	case <-s.thStopping:
		return errors.New("服务正在停止")
	default:
	}
	row, err := store.Claim(id)
	if err != nil {
		return err
	}
	fail := func(stage string, err error) error {
		detail := stage + "失败：" + thRedact(err.Error())
		_, _ = store.Update(id, func(rw *thsignupstore.Row) error {
			rw.Status = thsignupstore.Failed
			rw.Detail = detail
			return nil
		})
		s.logTHDiagnostic(row.Email, stage, row.ProxyURL, err)
		s.thLive(row.Email, stage, err.Error(), row.ProxyURL, "error")
		return errors.New(detail)
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-s.thStopping:
		return errors.New("服务正在停止")
	case <-time.After(10 * time.Minute):
		return fail("排队", errors.New("等待并发名额超时"))
	}

	proxy := row.ProxyURL
	if proxy == "" {
		proxy = cfg.ProxyURL
	}
	flowCfg := thregister.Config{
		BaseURL:        thBaseURL(cfg.BaseURL),
		ProxyURL:       proxy,
		Timezone:       "UTC",
		Label:          thLabel(cfg),
		MailTimeout:    time.Duration(cfg.MailTimeoutSec) * time.Second,
		RequestTimeout: 40 * time.Second,
		Log: func(stage, detail string) {
			s.thLive(row.Email, stage, detail, proxy, "info")
		},
	}
	if row.MailboxURL != "" {
		flowCfg.Mailbox = thmailbox.New(row.MailboxURL, s.thHTTPClient(proxy))
	}
	if solve {
		flowCfg.Solver = thcaptcha.New(thcaptcha.Config{
			BaseURL:    cfg.CaptchaBase,
			APIKey:     cfg.CaptchaKey,
			HTTPClient: s.thHTTPClient(proxy),
			PollEvery:  time.Duration(cfg.SolverPollMs) * time.Millisecond,
			Timeout:    time.Duration(cfg.SolveTimeoutSec) * time.Second,
		})
	}
	flow, err := thregister.New(flowCfg)
	if err != nil {
		return fail("准备", err)
	}
	defer flow.Close()

	s.thLive(row.Email, "开始注册", "", proxy, "info")
	// 12m of work + the mail budget, which the flow spends inside this window.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute+time.Duration(cfg.MailTimeoutSec)*time.Second)
	defer cancel()
	res, err := flow.Run(ctx, thregister.Credentials{
		Email: row.Email, Password: row.Password, MailboxURL: row.MailboxURL, ProxyURL: proxy,
	})
	if err != nil {
		stage := "注册"
		switch {
		case errors.Is(err, thregister.ErrCaptchaRequired):
			stage = "验证码"
		case errors.Is(err, thregister.ErrKeyRejected):
			stage = "账号判定"
		case errors.Is(err, thregister.ErrSessionRejected):
			stage = "注册被拒"
		}
		return fail(stage, err)
	}
	cookies := make([]thsignupstore.Cookie, 0, len(res.Cookies))
	for _, c := range res.Cookies {
		cookies = append(cookies, thsignupstore.Cookie{Name: c.Name, Value: c.Value})
	}
	status := thsignupstore.Registered
	if res.MailTimedOut {
		status = thsignupstore.Unverified
	}
	_, err = store.Update(id, func(rw *thsignupstore.Row) error {
		rw.Status = status
		rw.APIKey = res.APIKey
		rw.Cookies = cookies
		rw.Verified = res.VerifiedEmail
		rw.FreeModels = res.FreeModelsEnabled
		rw.Detail = "已注册"
		if res.MailTimedOut {
			rw.Detail = "已注册，待邮箱验证：验证信超过等待时间才到，可点「补验证」"
		} else if !res.VerifiedEmail {
			rw.Detail += "（未完成邮箱验证，API 已可用）"
		}
		if !res.FreeModelsEnabled && res.FreeModelsError != "" {
			rw.Detail += "；免费模型未开启：" + thRedact(res.FreeModelsError)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.logTHDiagnostic(row.Email, "register", proxy, nil)
	level := "success"
	if status == thsignupstore.Unverified {
		level = "warn"
	}
	s.thLive(row.Email, "注册完成", thsignupstore.MaskKey(res.APIKey), proxy, level)
	if autoImport {
		if err := s.thImport(id, group); err != nil {
			_, _ = store.Update(id, func(rw *thsignupstore.Row) error {
				rw.Detail = "已注册，入池失败：" + thRedact(err.Error())
				return nil
			})
		}
	}
	return nil
}

// thImport adds a registered row's key to the pool as one account.
func (s *Server) thImport(id, group string) error {
	store, err := s.thStore()
	if err != nil {
		return err
	}
	row, err := store.Get(id)
	if err != nil {
		return err
	}
	switch row.Status {
	case thsignupstore.Imported:
	case thsignupstore.Registered:
	case thsignupstore.Unverified:
		return errors.New("该账号还没完成邮箱验证，现在入池调对话会被拒绝（email_verification_required）；请先「补验证」")
	default:
		return errors.New("只有已注册的账号可以入池")
	}
	if strings.TrimSpace(row.APIKey) == "" {
		return errors.New("该账号没有 API Key")
	}
	if group == "" {
		group = row.Group
	}
	if group == "" {
		group = s.cfg.Get().TokenHarbor.Group
	}
	if group == "" {
		group = config.TokenHarborGroup
	}
	accountID := row.Account
	_, err = s.cfg.Update(-1, func(c *config.Config) error {
		// A re-import (or a row whose account was deleted) must not duplicate:
		// the key is the identity, exactly as for any other key pool.
		for i := range c.Accounts {
			if c.Accounts[i].Provider == config.ProviderTokenHarbor && c.Accounts[i].RuntimeKey == row.APIKey {
				accountID = c.Accounts[i].ID
				return nil
			}
		}
		a := config.Account{
			ID:         config.NewID("acc_"),
			Provider:   config.ProviderTokenHarbor,
			Name:       row.Email,
			Email:      row.Email,
			Group:      group,
			Enabled:    true,
			RuntimeKey: row.APIKey,
			ProxyURL:   row.ProxyURL,
			CreatedAt:  time.Now(),
		}
		c.Accounts = append(c.Accounts, a)
		accountID = a.ID
		return nil
	})
	if err != nil {
		return err
	}
	_, err = store.Update(id, func(rw *thsignupstore.Row) error {
		rw.Status = thsignupstore.Imported
		rw.Account = accountID
		rw.Detail = "已入池"
		return nil
	})
	return err
}

// ---- API handlers --------------------------------------------------------------

func (s *Server) apiTHRegistrations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	grouped := store.Summaries()
	groups := make([]string, 0, len(grouped))
	for k := range grouped {
		groups = append(groups, k)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(groups)))
	out := make([]thRegistrationView, 0, len(groups))
	for _, k := range groups {
		rows := grouped[k]
		view := thRegistrationView{ID: k, Rows: rows, Counts: map[string]int{}}
		for _, row := range rows {
			view.Counts[string(row.Status)]++
			if row.CreatedAt.After(view.CreatedAt) {
				view.CreatedAt = row.CreatedAt
			}
			if row.Status == thsignupstore.Running || row.Status == thsignupstore.Queued {
				view.Running = true
			}
		}
		out = append(out, view)
		if len(out) >= thRegistrationJobs {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

func (s *Server) apiTHRegistration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	id := r.PathValue("id")
	var rows []thsignupstore.Summary
	view := thRegistrationView{ID: id, Counts: map[string]int{}}
	for _, row := range store.List() {
		if row.Attempt != id {
			continue
		}
		rows = append(rows, row)
		view.Counts[string(row.Status)]++
		if row.Status == thsignupstore.Running || row.Status == thsignupstore.Queued {
			view.Running = true
		}
	}
	if rows == nil {
		apiError(w, http.StatusNotFound, errors.New("注册任务不存在"))
		return
	}
	view.Rows = rows
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) apiTHRowProxy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		ProxyURL *string `json:"proxyUrl"`
	}
	if err := readBody(r, &in); err != nil || in.ProxyURL == nil {
		apiError(w, http.StatusBadRequest, errors.New("请提供 proxyUrl"))
		return
	}
	if err := thsignupstore.ValidateProxy(*in.ProxyURL); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	row, err := store.SetProxy(r.PathValue("id"), *in.ProxyURL)
	if err != nil {
		s.thStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, thsignupstore.SummaryOf(row))
}

func (s *Server) apiTHImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Group string `json:"group"`
	}
	if r.ContentLength > 0 {
		if err := readBody(r, &in); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
	}
	id := r.PathValue("id")
	if err := s.thImport(id, strings.TrimSpace(in.Group)); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	store, _ := s.thStore()
	row, _ := store.Get(id)
	writeJSON(w, http.StatusOK, thsignupstore.SummaryOf(row))
}

func (s *Server) apiTHRetry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		SolveCaptcha bool `json:"solveCaptcha"`
	}
	if r.ContentLength > 0 {
		if err := readBody(r, &in); err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
		}
	}
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	id := r.PathValue("id")
	row, err := store.Retry(id)
	if err != nil {
		s.thStoreError(w, err)
		return
	}
	s.thTasks.Add(1)
	go func() {
		defer s.thTasks.Done()
		_ = s.thRegistrar().run(id, row.Group, false, in.SolveCaptcha, 0)
	}()
	writeJSON(w, http.StatusAccepted, thsignupstore.SummaryOf(row))
}

// apiTHReverify finishes the e-mail confirmation of a row that registered but
// whose mail arrived after the patience budget. It never re-registers.
func (s *Server) apiTHReverify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	id := r.PathValue("id")
	row, err := store.Get(id)
	if err != nil {
		s.thStoreError(w, err)
		return
	}
	if row.Status != thsignupstore.Unverified && row.Status != thsignupstore.Registered {
		apiError(w, http.StatusConflict, errors.New("只有已注册的账号需要补验证"))
		return
	}
	cfg := s.cfg.Get().TokenHarbor
	proxy := row.ProxyURL
	if proxy == "" {
		proxy = cfg.ProxyURL
	}
	cookies := make([]thregister.NamedCookie, 0, len(row.Cookies))
	for _, c := range row.Cookies {
		cookies = append(cookies, thregister.NamedCookie{Name: c.Name, Value: c.Value})
	}
	flowCfg := thregister.Config{
		BaseURL:        thBaseURL(cfg.BaseURL),
		ProxyURL:       proxy,
		Timezone:       "UTC",
		MailTimeout:    time.Duration(cfg.MailTimeoutSec) * time.Second,
		RequestTimeout: 40 * time.Second,
		Mailbox:        thmailbox.New(row.MailboxURL, s.thHTTPClient(proxy)),
		Log: func(stage, detail string) {
			s.thLive(row.Email, stage, detail, proxy, "info")
		},
	}
	flow, err := thregister.New(flowCfg)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	defer flow.Close()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.MailTimeoutSec)*time.Second+time.Minute)
	defer cancel()
	s.thLive(row.Email, "补验证", "查找验证邮件", proxy, "info")
	if _, err := flow.Reverify(ctx, row.Email, cookies, row.CreatedAt); err != nil {
		s.thLive(row.Email, "补验证", err.Error(), proxy, "error")
		apiError(w, http.StatusBadRequest, err)
		return
	}
	updated, err := store.Update(id, func(rw *thsignupstore.Row) error {
		rw.Verified = true
		if rw.Status == thsignupstore.Unverified {
			rw.Status = thsignupstore.Registered
		}
		rw.Detail = "已注册，邮箱已验证"
		return nil
	})
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	s.thLive(row.Email, "补验证", "成功", proxy, "success")
	writeJSON(w, http.StatusOK, thsignupstore.SummaryOf(updated))
}

// apiTHLog returns registration log lines after ?after=<seq>, for the console's
// bottom-right tail panel (it polls instead of holding a stream open).
func (s *Server) apiTHLog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var after int64
	if v := r.URL.Query().Get("after"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			after = n
		}
	}
	entries, seq := s.thLog().since(after)
	if entries == nil {
		entries = []thLogEntry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "seq": seq})
}

func (s *Server) apiTHDeleteRow(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	store, err := s.thStore()
	if err != nil {
		apiError(w, http.StatusInternalServerError, err)
		return
	}
	if err := store.Delete(r.PathValue("id")); err != nil {
		s.thStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) thStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, thsignupstore.ErrNotFound):
		apiError(w, http.StatusNotFound, errors.New("注册记录不存在"))
	case errors.Is(err, thsignupstore.ErrInvalid):
		apiError(w, http.StatusConflict, errors.New("当前状态不允许这个操作"))
	default:
		apiError(w, http.StatusBadRequest, err)
	}
}

// ---- settings ------------------------------------------------------------------

func (s *Server) apiTHSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cfg := s.cfg.Get()
	th := cfg.TokenHarbor
	writeJSON(w, http.StatusOK, map[string]any{
		"tokenHarbor": th,
		"hasCaptcha":  strings.TrimSpace(th.CaptchaKey) != "",
		"revision":    cfg.Revision,
		"proxyLabel":  thsignupstore.ProxyLabel(th.ProxyURL),
	})
}

func (s *Server) apiTHSaveSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		TokenHarbor      *config.TokenHarbor `json:"tokenHarbor"`
		ExpectedRevision *int64              `json:"expectedRevision"`
		ClearCaptchaKey  bool                `json:"clearCaptchaKey"`
	}
	if err := readBody(r, &in); err != nil || in.TokenHarbor == nil {
		apiError(w, http.StatusBadRequest, errors.New("tokenHarbor 不能为空"))
		return
	}
	next := *in.TokenHarbor
	// The key is write-only: an empty value keeps the stored one unless the
	// caller explicitly clears it.
	if strings.TrimSpace(next.CaptchaKey) == "" && !in.ClearCaptchaKey {
		next.CaptchaKey = s.cfg.Get().TokenHarbor.CaptchaKey
	}
	if err := thsignupstore.ValidateProxy(next.ProxyURL); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(next.BaseURL) == "" {
		next.BaseURL = config.TokenHarborDefaults().BaseURL
	}
	if u, err := url.Parse(next.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		apiError(w, http.StatusBadRequest, errors.New("API 地址无效"))
		return
	}
	rev := int64(-1)
	if in.ExpectedRevision != nil {
		rev = *in.ExpectedRevision
	}
	cfg, err := s.cfg.Update(rev, func(c *config.Config) error {
		c.TokenHarbor = next
		return nil
	})
	if err != nil {
		s.configError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revision": cfg.Revision, "tokenHarbor": cfg.TokenHarbor})
}

// ---- helpers -------------------------------------------------------------------

// thStore opens the registration store on first use.
func (s *Server) thStore() (*thsignupstore.Store, error) {
	s.thStoreMu.Lock()
	defer s.thStoreMu.Unlock()
	if s.thSignups != nil {
		return s.thSignups, nil
	}
	st, err := thsignupstore.Open(s.dataDir+"/tokenharbor-signup.json", s.secret)
	if err != nil {
		return nil, err
	}
	s.thSignups = st
	return st, nil
}

// thHTTPClient is one egress transport, reused per proxy URL so a batch keeps
// its connections warm.
func (s *Server) thHTTPClient(proxy string) *http.Client {
	return &http.Client{Transport: s.transport(proxy), Timeout: 45 * time.Second}
}

// thBaseURL is the SITE root the registration flow talks to.
//
// The setting is the API root ("https://tokenharbor.ai/v1") because that is
// what the pool sends inference to, but registration drives the site's own
// endpoints (/login, /api/auth/…), so a "/v1" suffix is the one thing that must
// come off. Anything else is kept verbatim: silently rewriting a configured
// host is how a test pointed at a stub once ended up hitting production.
func thBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return thregister.DefaultBase
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/v1")
	if u.Path == "" {
		u.Path = ""
	}
	u.RawQuery, u.Fragment = "", ""
	return strings.TrimSuffix(u.String(), "/")
}

func thLabel(cfg config.TokenHarbor) string {
	if strings.TrimSpace(cfg.KeyLabel) != "" {
		return strings.TrimSpace(cfg.KeyLabel)
	}
	return "pool"
}

// thRedact keeps credentials out of a stored detail line.
func thRedact(s string) string {
	for _, marker := range []string{"thk_live_", "thk_", "Bearer "} {
		if i := strings.Index(s, marker); i >= 0 {
			end := i + len(marker) + 4
			if end < len(s) {
				s = s[:end] + "…" + s[end:]
			}
			break
		}
	}
	if len([]rune(s)) > 300 {
		s = string([]rune(s)[:300]) + "…"
	}
	return s
}
