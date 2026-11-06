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
	"account-pool-balancer/internal/egressid"
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

// thRegisterInput accepts legacy email input and GitHub's four/five-field formats.
type thRegisterInput struct {
	Email           string  `json:"email"`
	Password        string  `json:"password"`
	MailboxURL      string  `json:"mailboxUrl,omitempty"`
	Provider        string  `json:"provider,omitempty"`
	Login           string  `json:"login,omitempty"`
	GitHubEmail     string  `json:"githubEmail,omitempty"`
	TOTPSecret      string  `json:"totpSecret,omitempty"`
	GitHubProxyURL  string  `json:"githubProxyUrl,omitempty"`
	SessionProxyURL *string `json:"sessionProxyUrl,omitempty"`
	// ProxyURL overrides the egress for this one mailbox.
	ProxyURL string `json:"proxyUrl,omitempty"`
}

type thRegisterRequest struct {
	Accounts []thRegisterInput `json:"accounts"`
	Proxies  []string          `json:"proxies,omitempty"`
	// GitHub authorization uses Proxies; these are only for the later TH session.
	SessionProxies []string `json:"sessionProxies,omitempty"`
	Group          string   `json:"group,omitempty"`
	// Retained only to reject stale clients instead of silently auto-importing.
	AutoImport bool   `json:"autoImport"`
	SaveOnly   bool   `json:"saveOnly"`
	ProxyMode  string `json:"proxyMode,omitempty"`
	// SolveCaptcha buys a Turnstile token when the site asks for one. Off by
	// default: the caller pays for each attempt it enables.
	SolveCaptcha bool `json:"solveCaptcha"`
	// Concurrency is accepted for legacy clients (0..thMaxConcurrency), but
	// the effective global registration budget is always one.
	Concurrency int `json:"concurrency,omitempty"`
}

// Legacy request bounds stay compatible; no requested value raises the
// effective global budget above one.
const thMaxConcurrency = 10
const thRegistrationConcurrency = 1

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

// budget is one immutable global slot, even when legacy requests carry
// different concurrency settings. Rebuilding a channel would bypass the cap.
func (r *thRegistrar) budget(_ int) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sem == nil {
		r.sem = make(chan struct{}, thRegistrationConcurrency)
	}
	return r.sem
}

// ---- API ---------------------------------------------------------------------

func (s *Server) registerTokenHarbor(mux *http.ServeMux, a func(string, http.HandlerFunc)) {
	a("POST /api/tokenharbor/registrations", s.apiTHRegister)
	a("GET /api/tokenharbor/registrations", s.apiTHRegistrations)
	a("GET /api/tokenharbor/overview", s.apiTHOverview)
	a("GET /api/tokenharbor/registrations/{id}", s.apiTHRegistration)
	a("PUT /api/tokenharbor/registrations/{id}/proxy", s.apiTHRowProxy)
	a("POST /api/tokenharbor/registrations/{id}/import", s.apiTHImport)
	a("POST /api/accounts/{id}/return-to-registration", s.apiTHReturn)
	a("POST /api/tokenharbor/registrations/{id}/retry", s.apiTHRetry)
	a("POST /api/tokenharbor/registrations/{id}/reverify", s.apiTHReverify)
	a("POST /api/tokenharbor/registrations/{id}/subscription", s.apiTHSubscription)
	a("GET /api/tokenharbor/proxies", s.apiTHProxies)
	a("PUT /api/tokenharbor/proxies", s.apiTHSaveProxies)
	a("POST /api/tokenharbor/registrations/{id}/checkout", s.apiTHCheckout)
	a("GET /api/tokenharbor/registrations/{id}/checkout", s.apiTHSavedCheckout)
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
	if in.Concurrency < 0 || in.Concurrency > thMaxConcurrency {
		apiError(w, http.StatusBadRequest, errors.New("注册并发参数必须为 0-10；实际固定单并发"))
		return
	}
	if in.AutoImport {
		apiError(w, http.StatusBadRequest, errors.New("注册后不再自动入池；请付款后检测有效套餐，再手动入池"))
		return
	}
	if in.ProxyMode != "" && in.ProxyMode != "sequential" && in.ProxyMode != "random" {
		apiError(w, http.StatusBadRequest, errors.New("代理分配方式仅支持 sequential 或 random"))
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
	if len(in.SessionProxies) > 1000 {
		apiError(w, http.StatusBadRequest, errors.New("最多 1000 个 TokenHarbor 后续代理"))
		return
	}
	for _, p := range in.SessionProxies {
		if err := thsignupstore.ValidateProxy(p); err != nil || strings.TrimSpace(p) == "" {
			apiError(w, http.StatusBadRequest, errors.New("TokenHarbor 后续代理地址无效"))
			return
		}
	}
	rows := make([]thsignupstore.Row, 0, len(in.Accounts))
	seen := map[string]bool{}
	for _, acct := range in.Accounts {
		if acct.Provider != "" && acct.Provider != "email" && acct.Provider != "github" {
			apiError(w, http.StatusBadRequest, errors.New("不支持的注册来源"))
			return
		}
		if fields := len(strings.Split(acct.Email, "----")); acct.Provider == "github" || acct.Login != "" || fields == 4 || fields == 5 {
			row, err := thParseGitHubInput(acct)
			if err != nil {
				apiError(w, http.StatusBadRequest, err)
				return
			}
			identity := "github:" + row.GitHub.Login
			if seen[identity] {
				apiError(w, http.StatusBadRequest, errors.New("GitHub 账号重复"))
				return
			}
			seen[identity] = true
			rows = append(rows, row)
			continue
		}
		email, password, mailbox, err := thParseRegisterInput(acct)
		if err != nil {
			apiError(w, http.StatusBadRequest, err)
			return
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
			Password:   password,
			MailboxURL: mailbox,
			ProxyURL:   strings.TrimSpace(acct.ProxyURL),
		})
	}
	// Bind egress at save/queue time. A proxy may serve multiple accounts.
	cfg := s.cfg.Get().TokenHarbor
	if len(proxies) == 0 {
		proxies = store.Proxies()
	}
	if err := thAssignProxies(rows, proxies, cfg.ProxyURL, in.ProxyMode); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	for i := range rows {
		if rows[i].GitHub == nil {
			continue
		}
		if rows[i].GitHub.ProxyURL == "" {
			rows[i].GitHub.ProxyURL = rows[i].ProxyURL
		}
		// The post-OAuth route may be IPv6, but is never used for GitHub.
		rows[i].ProxyURL = rows[i].GitHub.ProxyURL
		if len(in.SessionProxies) > 0 {
			rows[i].ProxyURL = strings.TrimSpace(in.SessionProxies[i%len(in.SessionProxies)])
		}
		if in.Accounts[i].SessionProxyURL != nil {
			rows[i].ProxyURL = strings.TrimSpace(*in.Accounts[i].SessionProxyURL)
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
	add := store.Add
	if in.SaveOnly {
		add = store.AddSaved
	}
	summaries, err := add(attempt, rows)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	created := false
	for _, row := range summaries {
		created = created || row.Attempt == attempt
	}
	if !created && len(summaries) > 0 {
		// Resubmission reopens the original records, never overwrites bound
		// egress/passwords or silently starts a previously saved account.
		writeJSON(w, http.StatusOK, map[string]any{"id": summaries[0].Attempt, "rows": summaries})
		return
	}
	if in.SaveOnly {
		writeJSON(w, http.StatusCreated, map[string]any{"id": attempt, "rows": summaries})
		return
	}
	conc := thRegistrationConcurrency
	s.thLive("", "任务创建", fmt.Sprintf("%d 个邮箱，并发 %d，代理 %d 个", len(rows), conc, len(proxies)), "", "info")
	s.thRegisterAttempt(attempt, in.SolveCaptcha, conc, summaries)
	writeJSON(w, http.StatusAccepted, map[string]any{"id": attempt, "rows": summaries})
}

// thRegisterAttempt starts the background work for the queued rows.
func (s *Server) thRegisterAttempt(attempt string, solve bool, conc int, rows []thsignupstore.Summary) {
	reg := s.thRegistrar()
	if conc <= 0 {
		conc = s.cfg.Get().TokenHarbor.RegisterConcurrency
	}
	for _, row := range rows {
		if row.Status != thsignupstore.Queued || row.Attempt != attempt {
			continue
		}
		id := row.ID
		// All rows share the fixed global budget, regardless of task settings.
		s.thTasks.Add(1)
		go func() {
			defer s.thTasks.Done()
			_ = reg.run(id, solve, conc)
		}()
	}
}

// run registers one row. Pool import is always a separate explicit action.
func (r *thRegistrar) run(id string, solve bool, conc int) error {
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
	proxy := row.Egress(cfg.ProxyURL)
	logProxy := proxy
	if row.GitHub != nil && !row.GitHub.OAuthComplete {
		logProxy = row.GitHub.ProxyURL
	}
	if strings.TrimSpace(logProxy) == "" {
		// Everything this run touches — the site, the mailbox, the captcha
		// service — would see this host's own address. Say so once, before any
		// request leaves, instead of leaving it to be inferred from upstream
		// dashboards later.
		s.thLive(row.Email, "出口", NoEgressWarning, "", "warn")
	}
	fail := func(stage string, err error) error {
		detail := stage + "失败：" + thRedact(err.Error())
		_, _ = store.Update(id, func(rw *thsignupstore.Row) error {
			rw.Status = thsignupstore.Failed
			rw.Detail = detail
			return nil
		})
		s.logTHDiagnostic(row.Email, stage, logProxy, err)
		s.thLive(row.Email, stage, err.Error(), logProxy, "error")
		return errors.New(detail)
	}

	select {
	case sem <- struct{}{}:
		defer func() { <-sem }()
	case <-s.thStopping:
		return errors.New("服务正在停止")
	case <-time.After(24 * time.Hour):
		return fail("排队", errors.New("等待单并发名额超过24小时；未发起注册，请人工核对后重新排队"))
	}

	flowCfg := thregister.Config{
		BaseURL:           thBaseURL(cfg.BaseURL),
		ProxyURL:          proxy,
		Seed:              id,
		EgressRegion:      thEgressRegion(cfg, proxy),
		Label:             thLabel(cfg),
		VerificationSince: row.CreatedAt,
		MailTimeout:       time.Duration(cfg.MailTimeoutSec) * time.Second,
		RequestTimeout:    40 * time.Second,
		BeforeSignup:      r.checkSignup,
		Checkpoint: func(res thregister.Result) error {
			_, err := store.Update(id, func(rw *thsignupstore.Row) error {
				thSaveResult(rw, res)
				return nil
			})
			return err
		},
		Log: func(stage, detail string) {
			if stage == "TokenHarbor 会话" {
				logProxy = proxy
			}
			s.thLive(row.Email, stage, detail, logProxy, "info")
		},
	}
	if row.GitHub != nil {
		flowCfg.UserAgent = thregister.OAuthUserAgent
	}
	if row.MailboxURL != "" {
		// The mailbox is a separate HTTPS service, not the account's upstream.
		// Select direct delivery explicitly; never fall back after proxy failure.
		flowCfg.Mailbox = &thmailbox.Client{URL: strings.TrimSpace(row.MailboxURL), HTTPClient: s.thHTTPClient(""), Seed: id}
	}
	if solve && row.GitHub == nil {
		flowCfg.Solver = thcaptcha.New(thcaptcha.Config{
			BaseURL:    cfg.CaptchaBase,
			APIKey:     cfg.CaptchaKey,
			HTTPClient: s.thHTTPClient(proxy),
			Seed:       id,
			PollEvery:  time.Duration(cfg.SolverPollMs) * time.Millisecond,
			Timeout:    time.Duration(cfg.SolveTimeoutSec) * time.Second,
		})
	}
	flow, err := thregister.New(flowCfg)
	if err != nil {
		return fail("准备", err)
	}
	defer flow.Close()

	s.thLive(row.Email, "开始注册", "", logProxy, "info")
	// 12m of work + the mail budget, which the flow spends inside this window.
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute+time.Duration(cfg.MailTimeoutSec)*time.Second)
	defer cancel()
	var res thregister.Result
	newSignup := row.GitHub == nil && len(row.Cookies) == 0
	if row.GitHub != nil {
		res = thStoredResult(row)
		if !res.OAuthComplete {
			err = r.checkSignup(ctx)
		}
		if err == nil {
			email := ""
			if strings.Contains(row.Email, "@") {
				email = row.Email
			}
			res, err = flow.RunGitHub(ctx, thregister.GitHubCredentials{
				Login: row.GitHub.Login, Password: row.Password, Email: email,
				TOTPSecret: row.GitHub.TOTPSecret, ProxyURL: row.GitHub.ProxyURL,
			}, res)
		}
	} else if len(row.Cookies) > 0 {
		res = thStoredResult(row)
		if thregister.HasSession(res.Cookies) {
			s.thLive(row.Email, "恢复会话", "继续已保存登录会话，不重复注册", proxy, "info")
		} else {
			s.thLive(row.Email, "登录恢复", "仅有注册中间 Cookie；先尝试原账号登录", proxy, "info")
			err = flow.Login(ctx, row.Email, row.Password)
			if err == nil {
				res.Cookies = flow.Session()
			} else if errors.Is(err, thregister.ErrLoginCredentialsMismatch) &&
				row.APIKey == "" && !row.Verified && row.Account == "" && row.Checkout == nil &&
				!thregister.HasSession(flow.Session()) {
				// User-authorized exception for this precise login rejection.
				// Preserve the row/credentials/route and the single worker;
				// a rejected or uncertain signup is never repeated in this run.
				newSignup = true
				s.thLive(row.Email, "重新注册", "官网返回邮箱密码不匹配；按原凭据、原出口重新注册一次", proxy, "warn")
			}
		}
		if err == nil && !newSignup {
			res, err = flow.Resume(ctx, res)
		}
	}
	if newSignup {
		s.thLive(row.Email, "注册调度", "固定单并发；不额外设置注册提交间隔，受限仅结束当前账号，不暂停其他账号", proxy, "info")
		err = r.checkSignup(ctx)
		if err == nil {
			res, err = flow.Run(ctx, thregister.Credentials{
				Email: row.Email, Password: row.Password, MailboxURL: row.MailboxURL, ProxyURL: proxy,
			})
		}
	}
	if err != nil {
		stage := "注册"
		var limited *thregister.RateLimitError
		if errors.As(err, &limited) {
			return fail("注册受限", err)
		}
		switch {
		case errors.Is(err, thregister.ErrCaptchaRequired):
			stage = "验证码"
		case errors.Is(err, thregister.ErrKeyRejected):
			stage = "账号判定"
		case errors.Is(err, thregister.ErrMailboxRejected):
			stage = "接码接口"
		case errors.Is(err, thregister.ErrLoginCredentialsMismatch):
			stage = "登录被拒"
		case errors.Is(err, thregister.ErrSessionRejected):
			stage = "注册被拒"
		}
		return fail(stage, err)
	}
	status := thsignupstore.Registered
	if !res.VerifiedEmail {
		status = thsignupstore.Unverified
	}
	_, err = store.Update(id, func(rw *thsignupstore.Row) error {
		rw.Status = status
		thSaveResult(rw, res)
		rw.Detail = "已注册"
		if res.MailTimedOut {
			rw.Detail = "已注册，待邮箱验证：验证信超过等待时间才到，可点「补验证」"
		} else if !res.VerifiedEmail {
			rw.Detail += "（待邮箱验证，尚未确认推理权限）"
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
	// Registration success does not establish paid subscription entitlement.
	_, _ = s.thCheckSubscription(ctx, id)
	return nil
}

// thImport adds a registered row's key to the pool as one account.
func (s *Server) thImport(ctx context.Context, id, group string) error {
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
	if !row.Verified {
		return errors.New("该账号尚未确认邮箱验证，请先补验证")
	}
	if strings.TrimSpace(row.APIKey) == "" {
		return errors.New("该账号没有 API Key")
	}
	checkedConfig := s.cfg.Get().TokenHarbor
	originalStatus, originalAccount := row.Status, row.Account
	row, err = s.thCheckSubscription(ctx, id)
	if err != nil {
		return err
	}
	if row.Status != originalStatus || row.Account != originalAccount {
		return errors.New("套餐检测期间入池状态变化，请重新操作")
	}
	if !row.Subscription.Active() {
		return errors.New("当前没有有效付费套餐（Free/未订阅或已到期）；支付链接不代表付款成功，请付款后重新检测")
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
	s.thAdmissionMu.Lock()
	defer s.thAdmissionMu.Unlock()
	_, err = store.Update(id, func(rw *thsignupstore.Row) error {
		if rw.UpdatedAt != row.UpdatedAt {
			return errors.New("套餐检测后账号状态变化，请重新操作")
		}
		accountID := row.Account
		_, err := s.cfg.Update(-1, func(c *config.Config) error {
			if c.TokenHarbor.BaseURL != checkedConfig.BaseURL || c.TokenHarbor.ProxyURL != checkedConfig.ProxyURL {
				return errors.New("套餐检测期间上游或代理设置变化，请重新检测")
			}
			// Re-imports dedupe by the full native key, never by email or mask.
			for i := range c.Accounts {
				if c.Accounts[i].Provider == config.ProviderTokenHarbor && c.Accounts[i].RuntimeKey == row.APIKey {
					accountID = c.Accounts[i].ID
					return nil
				}
			}
			a := config.Account{
				ID: config.NewID("acc_"), Provider: config.ProviderTokenHarbor,
				Name: row.Email, Email: row.Email, Group: group, Enabled: true,
				RuntimeKey: row.APIKey, ProxyURL: row.Egress(checkedConfig.ProxyURL), CreatedAt: time.Now(),
			}
			c.Accounts = append(c.Accounts, a)
			accountID = a.ID
			return nil
		})
		if err != nil {
			return err
		}
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
	if err := s.thImport(r.Context(), id, strings.TrimSpace(in.Group)); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	store, _ := s.thStore()
	row, _ := store.Get(id)
	if row.Account != "" {
		s.quota.RefreshSession(row.Account)
	}
	writeJSON(w, http.StatusOK, thsignupstore.SummaryOf(row))
}

func (s *Server) apiTHRetry(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		SolveCaptcha bool `json:"solveCaptcha"`
		Concurrency  int  `json:"concurrency,omitempty"`
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
	if in.Concurrency < 0 || in.Concurrency > thMaxConcurrency {
		apiError(w, http.StatusBadRequest, errors.New("注册并发必须为 0-10"))
		return
	}
	id := r.PathValue("id")
	registrar := s.thRegistrar()
	row, err := store.Retry(id)
	if err != nil {
		s.thStoreError(w, err)
		return
	}
	s.thTasks.Add(1)
	go func() {
		defer s.thTasks.Done()
		_ = registrar.run(id, in.SolveCaptcha, in.Concurrency)
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
	proxy := row.Egress(cfg.ProxyURL)
	cookies := make([]thregister.NamedCookie, 0, len(row.Cookies))
	for _, c := range row.Cookies {
		cookies = append(cookies, thregister.NamedCookie{Name: c.Name, Value: c.Value,
			Domain: c.Domain, Path: c.Path, Expires: c.Expires, Secure: c.Secure, HTTPOnly: c.HTTPOnly, SameSite: c.SameSite})
	}
	flowCfg := thregister.Config{
		BaseURL:        thBaseURL(cfg.BaseURL),
		ProxyURL:       proxy,
		Seed:           row.ID,
		EgressRegion:   thEgressRegion(cfg, proxy),
		MailTimeout:    time.Duration(cfg.MailTimeoutSec) * time.Second,
		RequestTimeout: 40 * time.Second,
		UserAgent:      thRowUserAgent(row),
		Mailbox:        &thmailbox.Client{URL: strings.TrimSpace(row.MailboxURL), HTTPClient: s.thHTTPClient(""), Seed: row.ID},
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
		current := s.cfg.Get().TokenHarbor
		if rw.UpdatedAt != row.UpdatedAt || current.BaseURL != cfg.BaseURL || rw.Egress(current.ProxyURL) != proxy {
			return errors.New("补验证期间账号或出口变化，请重新检测验证状态")
		}
		thSaveResult(rw, thregister.Result{APIKey: rw.APIKey, VerifiedEmail: true,
			FreeModelsEnabled: rw.FreeModels, Cookies: flow.Session()})
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
	s.thAdmissionMu.Lock()
	defer s.thAdmissionMu.Unlock()
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
	th.RegisterConcurrency = thRegistrationConcurrency
	hasCaptcha := strings.TrimSpace(th.CaptchaKey) != ""
	proxyLabel := thsignupstore.ProxyLabel(th.ProxyURL)
	th.CaptchaKey, th.ProxyURL = "", ""
	writeJSON(w, http.StatusOK, map[string]any{
		"tokenHarbor": th,
		"hasCaptcha":  hasCaptcha,
		"revision":    cfg.Revision,
		"proxyLabel":  proxyLabel,
	})
}

func (s *Server) apiTHSaveSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		TokenHarbor      *config.TokenHarbor `json:"tokenHarbor"`
		ExpectedRevision *int64              `json:"expectedRevision"`
		ClearCaptchaKey  bool                `json:"clearCaptchaKey"`
		ClearProxy       bool                `json:"clearProxy"`
	}
	if err := readBody(r, &in); err != nil || in.TokenHarbor == nil {
		apiError(w, http.StatusBadRequest, errors.New("tokenHarbor 不能为空"))
		return
	}
	next := *in.TokenHarbor
	next.RegisterConcurrency = thRegistrationConcurrency
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
		// Registration settings do not own the pool scheduler or model polling.
		next.Routing, next.ModelSyncSec, next.AccountSyncSec = c.TokenHarbor.Routing, c.TokenHarbor.ModelSyncSec, c.TokenHarbor.AccountSyncSec
		if strings.TrimSpace(next.CaptchaKey) == "" && !in.ClearCaptchaKey {
			next.CaptchaKey = c.TokenHarbor.CaptchaKey
		}
		if strings.TrimSpace(next.ProxyURL) == "" && !in.ClearProxy {
			next.ProxyURL = c.TokenHarbor.ProxyURL
		}
		c.TokenHarbor = next
		return nil
	})
	if err != nil {
		s.configError(w, err)
		return
	}
	view := cfg.TokenHarbor
	view.CaptchaKey, view.ProxyURL = "", ""
	writeJSON(w, http.StatusOK, map[string]any{"revision": cfg.Revision, "tokenHarbor": view})
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

// thEgressRegion is the country the run's proxy exits from, which decides the
// timezone and language the signup form claims. It is explicit configuration,
// never inferred: guessing a country from a proxy hostname would produce a
// zone that contradicts the exit IP, which is worse than no claim at all.
//
// An empty result is the safe default. The flow then keeps its own
// unremarkable UTC value rather than inventing one this code cannot verify.
func thEgressRegion(cfg config.TokenHarbor, proxy string) string {
	region := strings.ToUpper(strings.TrimSpace(cfg.EgressRegion))
	if region == "" || strings.TrimSpace(proxy) == "" {
		// A direct run egresses from the host's own address, which the
		// configured region does not describe.
		return ""
	}
	for _, known := range egressid.Regions() {
		if known == region {
			return region
		}
	}
	return ""
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
