package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perchactivate"
	"account-pool-balancer/internal/perchimport"
)

type registrationInput struct {
	Login      string `json:"login"`
	Password   string `json:"password"`
	TOTPSecret string `json:"totpSecret"`
	MailboxURL string `json:"mailboxUrl"`
	ProxyURL   string `json:"proxyUrl"`
}

type registrationRow struct {
	Login     string `json:"login"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	Email     string `json:"email,omitempty"`
	AccountID string `json:"accountId,omitempty"`
	CanImport bool   `json:"canImport"`
	Proxy     string `json:"proxy"`
	doc       []byte
	proxyURL  string
}

type registrationJob struct {
	ID        string            `json:"id"`
	Status    string            `json:"status"`
	Accounts  []registrationRow `json:"accounts"`
	CreatedAt time.Time         `json:"createdAt"`
}

func (job *registrationJob) hasPendingImport() bool {
	for _, row := range job.Accounts {
		if row.CanImport {
			return true
		}
	}
	return false
}

type registrationManager struct {
	mu       sync.Mutex
	jobs     map[string]*registrationJob
	running  bool
	worker   func(context.Context, registrationInput) ([]byte, error)
	activate func(context.Context, string, []byte) error
}

func (s *Server) apiPerchRegister(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Accounts   []registrationInput `json:"accounts"`
		AutoImport bool                `json:"autoImport"`
		Group      string              `json:"group"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, 400, err)
		return
	}
	if len(in.Accounts) == 0 || len(in.Accounts) > 100 {
		apiError(w, 400, errors.New("请选择 1-100 个账号"))
		return
	}
	seen := map[string]bool{}
	for i := range in.Accounts {
		a := &in.Accounts[i]
		a.Login = strings.TrimSpace(a.Login)
		if a.Login == "" || a.Password == "" || a.TOTPSecret == "" || a.MailboxURL == "" {
			apiError(w, 400, errors.New("账号、密码、2FA 和接码页均不能为空"))
			return
		}
		key := strings.ToLower(a.Login)
		if seen[key] {
			apiError(w, 400, errors.New("存在重复账号"))
			return
		}
		seen[key] = true
		proxy, err := normalizeRegistrationProxy(a.ProxyURL)
		if err != nil {
			apiError(w, 400, err)
			return
		}
		a.ProxyURL = proxy
	}
	m := &s.perchRegistrations
	m.mu.Lock()
	if m.jobs == nil {
		m.jobs = make(map[string]*registrationJob)
	}
	for id, job := range m.jobs {
		if !job.CreatedAt.IsZero() && time.Since(job.CreatedAt) > 24*time.Hour && job.Status != "running" && !job.hasPendingImport() {
			delete(m.jobs, id)
		}
	}
	if !m.running && len(m.jobs) >= 20 {
		var oldest *registrationJob
		for _, job := range m.jobs {
			if job.Status != "completed" {
				continue
			}
			if !job.hasPendingImport() && (oldest == nil || job.CreatedAt.Before(oldest.CreatedAt) || (job.CreatedAt.Equal(oldest.CreatedAt) && job.ID < oldest.ID)) {
				oldest = job
			}
		}
		if oldest != nil {
			delete(m.jobs, oldest.ID)
		}
	}
	if m.running || len(m.jobs) >= 20 {
		m.mu.Unlock()
		apiError(w, http.StatusConflict, errors.New("已有注册任务运行或任务数已达上限"))
		return
	}
	job := &registrationJob{ID: config.NewID("reg_"), Status: "running", CreatedAt: time.Now()}
	for _, a := range in.Accounts {
		job.Accounts = append(job.Accounts, registrationRow{Login: a.Login, Status: "queued", Proxy: registrationProxyLabel(a.ProxyURL), proxyURL: a.ProxyURL})
	}
	m.jobs[job.ID] = job
	m.running = true
	m.mu.Unlock()
	go s.runPerchRegistration(job.ID, in.Accounts, in.AutoImport, strings.TrimSpace(in.Group))
	writeJSON(w, http.StatusAccepted, map[string]string{"id": job.ID})
}

func (s *Server) registrationView(job *registrationJob) registrationJob {
	view := *job
	view.Accounts = append([]registrationRow(nil), job.Accounts...)
	for i := range view.Accounts {
		row := &view.Accounts[i]
		if row.Status == "imported" && row.AccountID != "" {
			if account, ok := s.cfg.Get().Account(row.AccountID); ok && account.Provider == config.ProviderPerch {
				row.Proxy = registrationProxyLabel(account.ProxyURL)
			} else {
				row.Proxy = "账号已删除"
			}
		}
	}
	return view
}

func (s *Server) apiPerchRegistrations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	m := &s.perchRegistrations
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs := make([]registrationJob, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, s.registrationView(job))
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].ID < jobs[j].ID
		}
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) apiPerchRegistration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	m := &s.perchRegistrations
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[r.PathValue("id")]
	if job == nil {
		apiError(w, 404, errors.New("注册任务不存在或已过期"))
		return
	}
	writeJSON(w, 200, s.registrationView(job))
}

// registrationProxyLabel exposes only the proxy scheme and endpoint.
func registrationProxyLabel(raw string) string {
	if raw == "" {
		return "直连"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "代理地址无效"
	}
	return u.Scheme + "://" + u.Host
}

func normalizeRegistrationProxy(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if err := config.ValidateProxyURL(raw); err != nil {
		return "", errors.New("代理地址无效")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.Port() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("代理地址无效")
	}
	if u.Scheme == "socks5h" {
		u.Scheme = "socks5"
	}
	return u.String(), nil
}

func (s *Server) apiPerchRegistrationProxy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Login    string  `json:"login"`
		ProxyURL *string `json:"proxyUrl"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, errors.New("请求格式无效"))
		return
	}
	if in.Login == "" || in.ProxyURL == nil {
		apiError(w, http.StatusBadRequest, errors.New("请选择账号并提供代理地址"))
		return
	}
	proxy, err := normalizeRegistrationProxy(*in.ProxyURL)
	if err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	m := &s.perchRegistrations
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[r.PathValue("id")]
	if job == nil {
		apiError(w, http.StatusNotFound, errors.New("注册任务不存在或已过期"))
		return
	}
	for i := range job.Accounts {
		row := &job.Accounts[i]
		if row.Login != in.Login {
			continue
		}
		if row.Status != "queued" && !(row.Status == "activated" && row.CanImport) {
			apiError(w, http.StatusConflict, errors.New("仅排队中或待入池账号可以修改代理"))
			return
		}
		row.proxyURL, row.Proxy = proxy, registrationProxyLabel(proxy)
		writeJSON(w, http.StatusOK, map[string]string{"proxy": row.Proxy})
		return
	}
	apiError(w, http.StatusNotFound, errors.New("账号不属于此任务"))
}

func (s *Server) apiPerchRegistrationImport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Logins []string `json:"logins"`
		Group  string   `json:"group"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, 400, err)
		return
	}
	if len(in.Logins) == 0 || len(in.Logins) > 100 {
		apiError(w, 400, errors.New("请选择账号"))
		return
	}
	m := &s.perchRegistrations
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[r.PathValue("id")]
	if job == nil {
		apiError(w, 404, errors.New("注册任务不存在或已过期"))
		return
	}
	selected := make(map[string]bool, len(in.Logins))
	for _, login := range in.Logins {
		if selected[login] {
			apiError(w, 400, errors.New("账号重复"))
			return
		}
		selected[login] = true
	}
	for _, row := range job.Accounts {
		if selected[row.Login] && !row.CanImport {
			apiError(w, 400, errors.New("只能导入已激活账号"))
			return
		}
		if selected[row.Login] {
			delete(selected, row.Login)
		}
	}
	if len(selected) != 0 {
		apiError(w, 400, errors.New("账号不属于此任务"))
		return
	}
	selected = make(map[string]bool, len(in.Logins))
	for _, login := range in.Logins {
		selected[login] = true
	}
	for i := range job.Accounts {
		if selected[job.Accounts[i].Login] {
			s.importRegistered(&job.Accounts[i], strings.TrimSpace(in.Group))
		}
	}
	writeJSON(w, 200, s.registrationView(job))
}

func (s *Server) importRegistered(row *registrationRow, group string) {
	parsed, err := perchimport.ParseJSON(row.doc)
	if err != nil {
		row.Detail = "会话解析失败"
		return
	}
	res := perchimport.Result{ParsedDocs: []perchimport.Parsed{parsed}, Sources: []string{row.Login}}
	out, err := s.importPerch(res, group, func(plans []perchImportPlan) error {
		if plans[0].Account != nil && plans[0].Account.ProxyURL != row.proxyURL {
			return errors.New("已有账号的代理与注册代理不同，请先核对代理设置")
		}
		plans[0].ProxyURL = row.proxyURL
		return nil
	})
	if err != nil {
		row.Detail = err.Error()
		return
	}
	if len(out.Outcomes) == 0 {
		row.Detail = "入池失败"
		return
	}
	result := out.Outcomes[0]
	if result.Status != "created" && result.Status != "updated" && result.Status != "duplicate" {
		row.Detail = "入池失败：" + result.Reason
		return
	}
	row.AccountID, row.Status, row.Detail, row.CanImport, row.doc = result.AccountID, "imported", "已加入号池", false, nil
}

func (s *Server) runPerchRegistration(id string, accounts []registrationInput, auto bool, group string) {
	m := &s.perchRegistrations
	defer func() {
		m.mu.Lock()
		m.running = false
		if job := m.jobs[id]; job != nil {
			job.Status = "completed"
		}
		m.mu.Unlock()
	}()
	for i := range accounts {
		m.mu.Lock()
		job := m.jobs[id]
		a := accounts[i]
		accounts[i] = registrationInput{}
		a.ProxyURL = job.Accounts[i].proxyURL
		job.Accounts[i].Status = "registering"
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		stage := "注册"
		worker := m.worker
		if worker == nil {
			worker = runPerchWorker
		}
		proxySource := "direct"
		if a.ProxyURL != "" {
			proxySource = "explicit_proxy"
		}
		started := time.Now()
		doc, err := worker(ctx, a)
		if err == nil {
			var envelope struct {
				Session json.RawMessage `json:"supabase_session"`
				Auth    struct {
					URL string `json:"supabase_url"`
					Key string `json:"supabase_anon_key"`
				} `json:"auth_context"`
			}
			parsed, parseErr := perchimport.ParseJSON(doc)
			if parseErr != nil || parsed.AccessToken == "" || parsed.RefreshToken == "" || parsed.ExpiresAt <= time.Now().Unix() || json.Unmarshal(doc, &envelope) != nil || len(envelope.Session) == 0 {
				err = errors.New("注册返回的会话不完整")
			}
			s.logPerchDiagnostic("", "register", proxySource, "", started, err)
			if err == nil {
				stage = "Starter 激活"
				m.mu.Lock()
				job.Accounts[i].Status = "activating"
				job.Accounts[i].Email = parsed.Email
				m.mu.Unlock()
				activate := m.activate
				if activate == nil {
					activate = func(ctx context.Context, proxy string, data []byte) error {
						var e struct {
							Session json.RawMessage `json:"supabase_session"`
							Auth    struct {
								URL string `json:"supabase_url"`
								Key string `json:"supabase_anon_key"`
							} `json:"auth_context"`
						}
						if json.Unmarshal(data, &e) != nil {
							return errors.New("会话无效")
						}
						transport := &http.Transport{}
						if proxy != "" {
							u, parseErr := url.Parse(proxy)
							if parseErr != nil {
								return errors.New("代理地址无效")
							}
							transport.Proxy = http.ProxyURL(u)
						}
						defer transport.CloseIdleConnections()
						_, err := perchactivate.Activate(ctx, perchactivate.Config{HTTPClient: &http.Client{Transport: transport}, SupabaseURL: e.Auth.URL, SupabaseKey: e.Auth.Key}, e.Session)
						return err
					}
				}
				started := time.Now()
				err = activate(ctx, a.ProxyURL, doc)
				s.logPerchDiagnostic("", "activate", proxySource, "", started, err)
			}
		} else {
			s.logPerchDiagnostic("", "register", proxySource, "", started, err)
		}
		cancel()
		m.mu.Lock()
		row := &job.Accounts[i]
		if err != nil {
			row.Status = "failed"
			row.Detail = stage + "失败，请检查账号、代理和服务状态后重试"
			var workerErr *registrationWorkerError
			if errors.As(err, &workerErr) {
				row.Detail = workerErr.detail
			}
			if stage == "Starter 激活" {
				row.Detail = "Starter 激活未完成，首条消息的送达状态可能未知，请检查账号后再重试"
			}
		} else {
			row.Status = "activated"
			row.Detail = "Starter 已激活"
			row.CanImport = true
			row.doc = doc
			row.proxyURL = a.ProxyURL
			if auto {
				s.importRegistered(row, group)
			}
		}
		m.mu.Unlock()
	}
}

type registrationWorkerError struct{ detail string }

func (e *registrationWorkerError) Error() string { return e.detail }

func runPerchWorker(ctx context.Context, a registrationInput) ([]byte, error) {
	worker := os.Getenv("PERCH_REGISTER_WORKER")
	if worker == "" {
		worker = filepath.Join("tools", "perch-register", "worker.py")
		if _, err := os.Stat(worker); err != nil {
			worker = "/usr/local/share/perch-register/worker.py"
		}
	}
	python := os.Getenv("PERCH_REGISTER_PYTHON")
	if python == "" {
		python = "python3"
	}
	payload := map[string]string{"login": a.Login, "password": a.Password, "totp_secret": a.TOTPSecret, "mailbox_url": a.MailboxURL}
	if a.ProxyURL != "" {
		payload["proxy"] = a.ProxyURL
	}
	input, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, python, worker)
	cmd.Stdin = bytes.NewReader(input)
	var output, stderr bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		details := map[string]string{
			"worker: input_validation":       "注册输入校验失败",
			"worker: dependency_unavailable": "注册依赖不可用",
			"worker: github_oauth":           "GitHub 登录或授权阶段失败，请检查账号、代理或上游验证",
			"worker: session_validation":     "Supabase 会话校验阶段失败",
			"worker: internal_error":         "注册进程内部错误",
		}
		if detail, ok := details[strings.TrimSpace(stderr.String())]; ok {
			return nil, &registrationWorkerError{detail: detail}
		}
		return nil, fmt.Errorf("注册进程失败: %w", err)
	}
	if output.Len() > 1<<20 {
		return nil, errors.New("注册进程输出过大")
	}
	return output.Bytes(), nil
}
