package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/mistralcards"
	"account-pool-balancer/internal/mistralsignupstore"
)

// mistralManager serializes registration attempts and keeps the store in sync.
// A single worker at a time keeps proxy sessions and mailbox polling predictable.
type mistralManager struct {
	srv   *Server
	store *mistralsignupstore.Store

	mu      sync.Mutex
	running map[string]context.CancelFunc
	seq     chan struct{} // capacity 1: one registration at a time
}

func (s *Server) registerMistral(mux *http.ServeMux, a func(string, http.HandlerFunc)) {
	// 懒初始化：store 需要 routingSecret，而它在 New() 后期才就绪
	a("GET /api/mistral/candidates", s.apiMistralCandidates)
	a("POST /api/mistral/candidates", s.apiMistralCandidateAdd)
	a("POST /api/mistral/candidates/bulk", s.apiMistralCandidateBulk)
	a("GET /api/mistral/candidates/{id}", s.apiMistralCandidateGet)
	a("DELETE /api/mistral/candidates/{id}", s.apiMistralCandidateDelete)
	a("POST /api/mistral/candidates/{id}/register", s.apiMistralCandidateRegister)
	a("POST /api/mistral/candidates/{id}/cancel", s.apiMistralCandidateCancel)
	a("GET /api/mistral/status", s.apiMistralStatus)
	a("GET /api/mistral/settings", s.apiMistralSettings)
	a("PUT /api/mistral/settings", s.apiMistralSaveSettings)
	a("POST /api/mistral/settings/test", s.apiMistralTestProxy)
}

// mistralStore returns the lazily-opened encrypted store.
func (s *Server) mistralStore() (*mistralsignupstore.Store, error) {
	s.mistralMu.Lock()
	defer s.mistralMu.Unlock()
	if s.mistralStoreRef != nil {
		return s.mistralStoreRef, nil
	}
	path := filepath.Join(s.dataDir, "mistral-signup.json")
	st, err := mistralsignupstore.Open(path, s.secret)
	if err != nil {
		return nil, err
	}
	s.mistralStoreRef = st
	return st, nil
}

func (s *Server) mistralMgr() (*mistralManager, error) {
	// 注意：不能持有 mistralMu 时再调 mistralStore（同一把锁）
	st, err := s.mistralStore()
	if err != nil {
		return nil, err
	}
	s.mistralMu.Lock()
	defer s.mistralMu.Unlock()
	if s.mistralMgrRef != nil {
		return s.mistralMgrRef, nil
	}
	m := &mistralManager{srv: s, store: st, running: map[string]context.CancelFunc{}, seq: make(chan struct{}, 1)}
	s.mistralMgrRef = m
	return m, nil
}

// ---------------------------------------------------------------- handlers

func (s *Server) apiMistralCandidates(w http.ResponseWriter, r *http.Request) {
	m, err := s.mistralMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": m.store.List()})
}

func (s *Server) apiMistralCandidateAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email        string `json:"email"`
		MailboxURL   string `json:"mailbox_url"`
		Password     string `json:"password"`
		Region       string `json:"region"`
		ProxyURL     string `json:"proxy_url"`
		SkipRegister bool   `json:"skip_register"`
		ImportToPool bool   `json:"import_to_pool"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	body.MailboxURL = strings.TrimSpace(body.MailboxURL)
	if body.Email == "" || body.MailboxURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email and mailbox_url are required"})
		return
	}
	if body.Region == "" {
		body.Region = "CA"
	}

	m, err := s.mistralMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c, err := m.store.Add(mistralsignupstore.Candidate{
		Email:        body.Email,
		Password:     body.Password,
		MailboxURL:   body.MailboxURL,
		Region:       body.Region,
		ProxyURL:     body.ProxyURL,
		Status:       mistralsignupstore.Queued,
		ImportToPool: body.ImportToPool,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidate": c})
}

func (s *Server) apiMistralCandidateGet(w http.ResponseWriter, r *http.Request) {
	m, err := s.mistralMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c, err := m.store.Get(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidate": c})
}

func (s *Server) apiMistralCandidateDelete(w http.ResponseWriter, r *http.Request) {
	m, err := s.mistralMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := m.store.Delete(r.PathValue("id")); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiMistralStatus(w http.ResponseWriter, r *http.Request) {
	m, err := s.mistralMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	m.mu.Lock()
	n := len(m.running)
	m.mu.Unlock()

	worker := mistralWorkerPath()
	_, statErr := os.Stat(worker)
	python := os.Getenv("MISTRAL_WORKER_PYTHON")
	if python == "" {
		python = "python3"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"running":      n,
		"worker_path":  worker,
		"worker_found": statErr == nil,
		"python":       python,
		"total":        len(m.store.List()),
	})
}

func (s *Server) apiMistralCandidateRegister(w http.ResponseWriter, r *http.Request) {
	m, err := s.mistralMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	c, err := m.store.Get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}

	var body struct {
		SkipRegister bool `json:"skip_register"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)

	// 已有同账号在跑则拒绝
	m.mu.Lock()
	if _, busy := m.running[id]; busy {
		m.mu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already running"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	m.running[id] = cancel
	m.mu.Unlock()

	// 异步执行，前端轮询列表
	go func() {
		defer func() {
			m.mu.Lock()
			delete(m.running, id)
			m.mu.Unlock()
			cancel()
		}()

		// 串行闸门：一次只跑一个注册
		select {
		case m.seq <- struct{}{}:
			defer func() { <-m.seq }()
		case <-ctx.Done():
			return
		}

		_, _ = m.store.Update(id, func(x *mistralsignupstore.Candidate) error {
			x.Status = mistralsignupstore.Running
			x.Error = ""
			x.Detail = ""
			return nil
		})

		res, runErr := m.srv.runMistralWorker(ctx, c, body.SkipRegister, c.ProxyIndex)
		if runErr != nil {
			_, _ = m.store.Update(id, func(x *mistralsignupstore.Candidate) error {
				x.Status = mistralsignupstore.Failed
				x.Error = runErr.Error()
				x.Detail = mistralErrDetail(res)
				return nil
			})
			return
		}

		_, _ = m.store.Update(id, func(x *mistralsignupstore.Candidate) error {
			if res.Password != "" {
				x.Password = res.Password
			}
			x.OrgUUID = res.OrgUUID
			x.WorkspaceID = res.WorkspaceUUID
			x.Keys = mistralsignupstore.Keys{
				API:     res.Keys["API"],
				Vibe:    res.Keys["Vibe"],
				Studio:  res.Keys["Studio"],
				VibeCLI: res.VibeCLIKey,
			}
			if res.OK {
				x.Status = mistralsignupstore.Ready
			} else {
				x.Status = mistralsignupstore.Failed
				x.Error = res.Error
			}
			return nil
		})

		// 勾选了「入池」就在注册成功后自动写入 Mistral 账号池
		if res.OK && c.ImportToPool {
			refreshed, err := m.store.Get(id)
			if err == nil {
				if acctID, impErr := m.srv.importMistralCandidate(refreshed); impErr == nil {
					_, _ = m.store.Update(id, func(x *mistralsignupstore.Candidate) error {
						x.ImportedAccountID = acctID
						return nil
					})
				} else {
					_, _ = m.store.Update(id, func(x *mistralsignupstore.Candidate) error {
						x.Detail = "入池失败：" + impErr.Error()
						return nil
					})
				}
			}
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true, "id": id})
}

func (s *Server) apiMistralCandidateCancel(w http.ResponseWriter, r *http.Request) {
	m, err := s.mistralMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	m.mu.Lock()
	cancel, ok := m.running[id]
	m.mu.Unlock()
	if ok {
		cancel()
	}
	_, _ = m.store.Update(id, func(x *mistralsignupstore.Candidate) error {
		if x.Status == mistralsignupstore.Running {
			x.Status = mistralsignupstore.Cancelled
		}
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------------------------------------------------------------- worker

type mistralWorkerResult struct {
	OK            bool              `json:"ok"`
	Email         string            `json:"email"`
	Password      string            `json:"password"`
	OrgUUID       string            `json:"org_uuid"`
	WorkspaceUUID string            `json:"workspace_uuid"`
	Keys          map[string]string `json:"keys"`
	VibeCLIKey    string            `json:"vibe_cli_key"`
	Status        string            `json:"status"`
	Error         string            `json:"error"`
}

func mistralWorkerPath() string {
	if p := os.Getenv("MISTRAL_WORKER"); p != "" {
		return p
	}
	for _, cand := range []string{
		filepath.Join("tools", "mistral", "worker.py"),
		"/usr/local/share/mistral-signup/worker.py",
	} {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return filepath.Join("tools", "mistral", "worker.py")
}

func mistralPythonPath() string {
	if p := os.Getenv("MISTRAL_WORKER_PYTHON"); p != "" {
		return p
	}
	return "python3"
}

func (s *Server) runMistralWorker(ctx context.Context, c mistralsignupstore.Candidate, skipRegister bool, proxyIndex int) (mistralWorkerResult, error) {
	var out mistralWorkerResult

	payload := map[string]any{
		"email":         c.Email,
		"mailbox_url":   c.MailboxURL,
		"password":      c.Password,
		"region":        c.Region,
		"skip_register": skipRegister,
		"proxy_index":   proxyIndex,
	}
	if c.ProxyURL != "" {
		payload["proxy"] = c.ProxyURL
	}
	input, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}

	cfg := s.cfg.Get().MistralSignup

	python := mistralPythonPath()
	if cfg.WorkerPython != "" {
		python = cfg.WorkerPython
	}
	worker := mistralWorkerPath()
	if cfg.WorkerPath != "" {
		worker = cfg.WorkerPath
	}

	cmd := exec.CommandContext(ctx, python, worker)
	cmd.Stdin = bytes.NewReader(input)
	// 把界面里配置的代理注入 worker 环境
	env := os.Environ()
	var stdout, stderr bytes.Buffer
	// 代理列表：一行一个完整 URL
	if urls := cfg.AllProxies(); len(urls) > 0 {
		env = append(env, "MISTRAL_PROXY_URLS="+strings.Join(urls, "\n"))
	}
	// 银行卡列表：一行一张，格式 卡号----有效期----CVV
	// 只注入解析通过的行，避免把明显的脏数据带进 worker。
	if cards := cfg.AllCards(); len(cards) > 0 {
		if parsed, errs := mistralcards.ParseLines(cards); len(parsed) > 0 {
			lines := make([]string, 0, len(parsed))
			for _, c := range parsed {
				lines = append(lines, fmt.Sprintf("%s----%s----%s",
					c.Number, c.Expiry(), c.CVV))
			}
			env = append(env, "MISTRAL_CARDS="+strings.Join(lines, "\n"))
			if len(errs) > 0 {
				// 只记条数，不记明文
				fmt.Fprintf(&stderr, "mistral: %d card line(s) skipped (invalid)\n", len(errs))
			}
		}
	}
	if cfg.MailboxProxy != "" {
		env = append(env, "MIDA_PROXY="+cfg.MailboxProxy)
	}
	cmd.Env = env
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr != nil {
		// worker 可能已经把有意义的 JSON 写到 stdout
		if jerr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &out); jerr == nil {
			if out.Error == "" {
				out.Error = runErr.Error()
			}
			out.Status = "failed"
			return out, fmt.Errorf("%s", out.Error)
		}
		detail := tailLines(stderr.String(), 20)
		if detail == "" {
			detail = runErr.Error()
		}
		return out, errors.New("mistral worker failed: " + detail)
	}

	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &out); err != nil {
		return out, fmt.Errorf("worker returned invalid json: %v (stderr: %s)", err, tailLines(stderr.String(), 5))
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = "registration failed"
		}
		return out, errors.New(out.Error)
	}
	return out, nil
}

func mistralErrDetail(r mistralWorkerResult) string {
	if r.Error != "" {
		return r.Error
	}
	return ""
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------- settings

// apiMistralSettings returns the signup runner settings. ProxyPassword is
// returned verbatim so the dashboard can round-trip edits; the endpoint is
// admin-only and same-origin.
func (s *Server) apiMistralSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get().MistralSignup
	parsed, cardErrs := mistralcards.ParseLines(cfg.AllCards())
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":     cfg,
		"hasProxy":     cfg.HasProxy(),
		"hasCards":     cfg.HasCards(),
		"cardCount":    len(parsed),
		"cardErrors":   cardErrorsJSON(cardErrs),
		"accountCount": len(mustMistralList(s)),
	})
}

func mustMistralList(s *Server) []mistralsignupstore.Candidate {
	m, err := s.mistralMgr()
	if err != nil {
		return nil
	}
	return m.store.List()
}

func (s *Server) apiMistralSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Settings         *config.MistralSignup `json:"settings"`
		ExpectedRevision *int64                `json:"expectedRevision"`
	}
	if err := readBody(r, &in); err != nil {
		apiError(w, http.StatusBadRequest, err)
		return
	}
	if in.Settings == nil {
		apiError(w, http.StatusBadRequest, errors.New("settings is required"))
		return
	}
	next := *in.Settings
	// 代理 URL 列表：去空行
	urls := make([]string, 0, len(next.ProxyURLs))
	for _, u := range next.ProxyURLs {
		if u = strings.TrimSpace(u); u != "" {
			urls = append(urls, u)
		}
	}
	next.ProxyURLs = urls

	// 银行卡列表：去空行后原样保存。
	// 解析失败的行仍然保留，这样用户能在页面上看到并修正，而不是被静默丢弃。
	cards := make([]string, 0, len(next.CardLines))
	for _, c := range next.CardLines {
		if c = strings.TrimSpace(c); c != "" {
			cards = append(cards, c)
		}
	}
	next.CardLines = cards

	// 解析结果只用于回显诊断，不影响保存
	parsed, cardErrs := mistralcards.ParseLines(cards)

	rev := int64(-1)
	if in.ExpectedRevision != nil {
		rev = *in.ExpectedRevision
	}
	if _, err := s.cfg.Update(rev, func(c *config.Config) error {
		c.MistralSignup = next
		return nil
	}); err != nil {
		apiError(w, http.StatusConflict, err)
		return
	}
	cfg := s.cfg.Get().MistralSignup
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":  cfg,
		"hasProxy":  cfg.HasProxy(),
		"hasCards":  cfg.HasCards(),
		"cardCount": len(parsed),
		"cardErrors": cardErrorsJSON(cardErrs),
		"revision":  s.cfg.Get().Revision,
	})
}

// cardErrorsJSON 把解析错误转成前端易用的结构。
func cardErrorsJSON(errs []mistralcards.ParseError) []map[string]any {
	out := make([]map[string]any, 0, len(errs))
	for _, e := range errs {
		out = append(out, map[string]any{
			"line":   e.Line,
			"raw":    e.Raw,
			"reason": e.Reason,
		})
	}
	return out
}

// apiMistralTestProxy checks one candidate proxy by asking a public IP service
// through it. It never runs a full registration.
func (s *Server) apiMistralTestProxy(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get().MistralSignup
	if !cfg.HasProxy() {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "代理未配置，请先在代理列表里填写",
		})
		return
	}
	urls := cfg.AllProxies()
	// 每次测试随机挑一个，便于轮换验证
	idx := 0
	if len(urls) > 1 {
		idx = randIndex(len(urls))
	}
	proxyURL := urls[idx]
	sid := fmt.Sprintf("#%d", idx)

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "curl", "-s", "-m", "30",
		"-x", proxyURL, "https://api.ipify.org?format=json")
	out, err := cmd.Output()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "sid": sid, "error": err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "sid": sid, "response": string(bytes.TrimSpace(out)),
	})
}

func randIndex(n int) int {
	if n <= 1 {
		return 0
	}
	b := make([]byte, 1)
	_, _ = rand.Read(b)
	return int(b[0]) % n
}

// apiMistralCandidateBulk 批量添加候选。每行 "邮箱----接码地址"。
// 代理按候选顺序轮转分配（可重复使用）。
func (s *Server) apiMistralCandidateBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Accounts     string `json:"accounts"` // 多行文本
		Region       string `json:"region"`
		SkipRegister bool   `json:"skip_register"`
		ImportToPool bool   `json:"import_to_pool"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if body.Region == "" {
		body.Region = "CA"
	}

	m, err := s.mistralMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	type badLine struct {
		Line  int    `json:"line"`
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	var added []mistralsignupstore.Candidate
	var rejected []badLine
	existing := map[string]bool{}
	for _, c := range m.store.List() {
		existing[c.Email] = true
	}

	// 已有序号基数：新候选接着往后排，保证代理轮转连续
	base := len(m.store.List())

	for i, raw := range strings.Split(body.Accounts, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "----", 2)
		if len(parts) != 2 {
			rejected = append(rejected, badLine{i + 1, line, "缺少 ---- 分隔符"})
			continue
		}
		email := strings.TrimSpace(parts[0])
		mailbox := strings.TrimSpace(parts[1])
		if email == "" || mailbox == "" {
			rejected = append(rejected, badLine{i + 1, line, "邮箱或接码地址为空"})
			continue
		}
		if !strings.Contains(email, "@") {
			rejected = append(rejected, badLine{i + 1, line, "邮箱格式不正确"})
			continue
		}
		if existing[email] {
			rejected = append(rejected, badLine{i + 1, line, "该邮箱已存在"})
			continue
		}
		c, err := m.store.Add(mistralsignupstore.Candidate{
			Email:        email,
			MailboxURL:   mailbox,
			Region:       body.Region,
			Status:       mistralsignupstore.Queued,
			ImportToPool: body.ImportToPool,
			ProxyIndex:   base + len(added),
		})
		if err != nil {
			rejected = append(rejected, badLine{i + 1, line, err.Error()})
			continue
		}
		existing[email] = true
		added = append(added, c)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"added":    added,
		"rejected": rejected,
		"count":    len(added),
	})
}

// ---------------------------------------------------------------- 入池

// importMistralCandidate 把一个已完成注册的候选写入 Mistral 账号池。
// 返回新账号 ID。重复 Key 会被拒绝。
func (s *Server) importMistralCandidate(c mistralsignupstore.Candidate) (string, error) {
	key := strings.TrimSpace(c.Keys.API)
	if key == "" {
		return "", errors.New("该候选没有 API Key，无法入池")
	}
	if !strings.HasPrefix(key, config.MistralKeyPrefix) {
		return "", fmt.Errorf("Key 前缀不是 %s", config.MistralKeyPrefix)
	}

	var newID string
	_, err := s.cfg.Update(-1, func(cfg *config.Config) error {
		for _, a := range cfg.Accounts {
			if a.Provider == config.ProviderMistral && a.RuntimeKey == key {
				return errors.New("该 Key 已在账号池中")
			}
		}
		name := c.Email
		if i := strings.Index(name, "@"); i > 0 {
			name = name[:i]
		}
		if len([]rune(name)) > 50 {
			name = string([]rune(name)[:50])
		}
		acct := config.Account{
			ID:          "mistral-" + c.ID,
			Provider:    config.ProviderMistral,
			Name:        name,
			Email:       c.Email,
			Group:       config.MistralGroup,
			Enabled:     true,
			RuntimeKey:  key,
			WorkspaceID: c.WorkspaceID,
			MaxConc:     0,
			Weight:      1,
			Note:        "Mistral 注册机导入",
		}
		cfg.Accounts = append(cfg.Accounts, acct)
		newID = acct.ID
		return nil
	})
	if err != nil {
		return "", err
	}
	return newID, nil
}
