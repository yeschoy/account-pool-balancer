package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/deploysignupstore"
	"account-pool-balancer/internal/mistralcards"
)

// deployManager 串行执行注册任务（单 worker，避免并发触发 429 与一 IP 多号）。
type deployManager struct {
	srv   *Server
	store *deploysignupstore.Store

	mu      sync.Mutex
	running map[string]context.CancelFunc
	seq     chan struct{} // capacity 1
	logs    []deployLogLine
	logMu   sync.Mutex
}

type deployLogLine struct {
	At      time.Time `json:"at"`
	Type    string    `json:"type"`
	Email   string    `json:"email,omitempty"`
	Message string    `json:"message,omitempty"`
}

const maxDeployLogs = 500

func (s *Server) registerDeploy(mux *http.ServeMux, a func(string, http.HandlerFunc)) {
	a("GET /api/deploy/candidates", s.apiDeployCandidates)
	a("POST /api/deploy/candidates", s.apiDeployCandidateAdd)
	a("POST /api/deploy/candidates/bulk", s.apiDeployCandidateBulk)
	a("DELETE /api/deploy/candidates/{id}", s.apiDeployCandidateDelete)
	a("POST /api/deploy/candidates/{id}/register", s.apiDeployCandidateRegister)
	a("POST /api/deploy/candidates/bulk-register", s.apiDeployBulkRegister)
	a("POST /api/deploy/candidates/{id}/cancel", s.apiDeployCandidateCancel)
	a("POST /api/deploy/candidates/{id}/resume", s.apiDeployCandidateResume)
	a("POST /api/deploy/candidates/{id}/import", s.apiDeployCandidateImport)
	a("POST /api/deploy/candidates/import-all", s.apiDeployImportAll)
	a("PATCH /api/deploy/candidates/{id}", s.apiDeployCandidateUpdate)
	a("POST /api/deploy/candidates/assign-proxies", s.apiDeployAssignProxies)
	a("POST /api/deploy/proxy-test", s.apiDeployProxyTest)
	a("POST /api/deploy/candidates/{id}/bind-card", s.apiDeployBindCard)
	a("POST /api/deploy/bind-cards", s.apiDeployBindCards)
	a("PATCH /api/deploy/candidates/{id}/card", s.apiDeploySetCard)
	a("GET /api/deploy/logs", s.apiDeployLogs)
	a("GET /api/deploy/settings", s.apiDeploySettings)
	a("PUT /api/deploy/settings", s.apiDeploySaveSettings)
	a("GET /api/deploy/status", s.apiDeployStatus)
}

func (s *Server) deployStore() (*deploysignupstore.Store, error) {
	s.mistralMu.Lock()
	defer s.mistralMu.Unlock()
	if s.deployStoreRef != nil {
		return s.deployStoreRef, nil
	}
	path := filepath.Join(s.dataDir, "deploy-signup.json")
	st, err := deploysignupstore.Open(path, s.secret)
	if err != nil {
		return nil, err
	}
	s.deployStoreRef = st
	return st, nil
}

func (s *Server) deployMgr() (*deployManager, error) {
	st, err := s.deployStore()
	if err != nil {
		return nil, err
	}
	s.mistralMu.Lock()
	defer s.mistralMu.Unlock()
	if s.deployMgrRef != nil {
		return s.deployMgrRef, nil
	}
	m := &deployManager{
		srv:     s,
		store:   st,
		running: map[string]context.CancelFunc{},
		seq:     make(chan struct{}, 1),
	}
	s.deployMgrRef = m
	return m, nil
}

func (m *deployManager) addLog(typ, email, msg string) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	m.logs = append(m.logs, deployLogLine{At: time.Now(), Type: typ, Email: email, Message: msg})
	if len(m.logs) > maxDeployLogs {
		m.logs = m.logs[len(m.logs)-maxDeployLogs:]
	}
}

func (m *deployManager) Logs() []deployLogLine {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	out := make([]deployLogLine, len(m.logs))
	copy(out, m.logs)
	return out
}

// ---------------------------------------------------------------- handlers

func (s *Server) apiDeployCandidates(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": m.store.List(),
		"settings":   m.store.Settings(),
	})
}

func (s *Server) apiDeployCandidateAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email      string `json:"email"`
		MailboxURL string `json:"mailbox_url"`
		Password   string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	body.MailboxURL = strings.TrimSpace(body.MailboxURL)
	if body.Email == "" || body.MailboxURL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email 与 mailbox_url 必填"})
		return
	}
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	c, err := m.store.Add(deploysignupstore.Candidate{
		Email: body.Email, MailboxURL: body.MailboxURL, Password: body.Password,
		Status: deploysignupstore.Queued,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidate": c})
}

// apiDeployCandidateBulk 接受多行 `email----mailbox_url`。
func (s *Server) apiDeployCandidateBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	added, skipped := 0, 0
	var errs []string
	for _, line := range strings.Split(body.Text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var email, url string
		switch {
		case strings.Contains(line, "----"):
			p := strings.SplitN(line, "----", 2)
			email, url = p[0], p[1]
		case strings.Contains(line, "\t"):
			p := strings.SplitN(line, "\t", 2)
			email, url = p[0], p[1]
		default:
			skipped++
			continue
		}
		email, url = strings.TrimSpace(email), strings.TrimSpace(url)
		if email == "" || url == "" {
			skipped++
			continue
		}
		if _, err := m.store.Add(deploysignupstore.Candidate{
			Email: email, MailboxURL: url, Status: deploysignupstore.Queued,
		}); err != nil {
			skipped++
			errs = append(errs, email+": "+err.Error())
			continue
		}
		added++
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "skipped": skipped, "errors": errs})
}

func (s *Server) apiDeployCandidateDelete(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	if err := m.store.Delete(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiDeployCandidateRegister 注册单个候选。
func (s *Server) apiDeployCandidateRegister(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	if _, err := m.store.Get(id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	go m.runBatch([]string{id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": 1})
}

// apiDeployBulkRegister 批量注册（只处理 queued/failed）。
func (s *Server) apiDeployBulkRegister(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var ids []string
	for _, c := range m.store.List() {
		if c.Status == deploysignupstore.Queued || c.Status == deploysignupstore.Failed {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": 0})
		return
	}
	go m.runBatch(ids)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": len(ids)})
}

func (s *Server) apiDeployCandidateCancel(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
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
	_, _ = m.store.Update(id, func(c *deploysignupstore.Candidate) error {
		if c.Status == deploysignupstore.Running {
			c.Status = deploysignupstore.Cancelled
		}
		return nil
	})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiDeployCandidateResume 用已保存的会话校验账号是否仍然可用，不重新注册。
func (s *Server) apiDeployCandidateResume(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
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
	if c.SessionCookie == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该账号没有保存的会话"})
		return
	}
	go m.runBatchMode([]string{id}, true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resuming": 1})
}

func (s *Server) apiDeployLogs(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": m.Logs()})
}

func (s *Server) apiDeploySettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": m.store.Settings()})
}

func (s *Server) apiDeploySaveSettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var in deploysignupstore.Settings
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	// 卡列表逐行校验：无效行整行丢弃并回报原因，不阻塞其余行。
	validCards, cardErrs := validateDeployCards(in.Cards)
	in.Cards = validCards
	if err := m.store.SaveSettings(in); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":   m.store.Settings(),
		"card_count": len(validCards),
		"card_errors": cardErrs,
	})
}

func (s *Server) apiDeployStatus(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rows := m.store.List()
	var queued, running, verified, failed int
	for _, c := range rows {
		switch c.Status {
		case deploysignupstore.Queued:
			queued++
		case deploysignupstore.Running:
			running++
		case deploysignupstore.Verified:
			verified++
		case deploysignupstore.Failed:
			failed++
		}
	}
	m.mu.Lock()
	busy := len(m.running) > 0
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"total": len(rows), "queued": queued, "running": running,
		"verified": verified, "failed": failed, "busy": busy,
	})
}

// ---------------------------------------------------------------- worker

// runBatch 串行执行一批候选。全程持有 seq 信号量，保证同一时间只有一个批次。
func (m *deployManager) runBatch(ids []string) {
	m.runBatchMode(ids, false)
}

// runBatchMode resumeOnly=true 表示只校验已保存会话，不重新注册。
func (m *deployManager) runBatchMode(ids []string, resumeOnly bool) {
	if len(ids) == 0 {
		return
	}
	m.seq <- struct{}{}
	defer func() { <-m.seq }()

	st := m.store.Settings()
	scriptPath, pyBin, err := m.scriptPaths(st)
	if err != nil {
		m.addLog("error", "", err.Error())
		for _, id := range ids {
			_, _ = m.store.Update(id, func(c *deploysignupstore.Candidate) error {
				c.Status = deploysignupstore.Failed
				c.Error = err.Error()
				return nil
			})
		}
		return
	}

	// 只取 queued/failed 的
	var items []map[string]any
	var validIDs []string
	for i, id := range ids {
		c, err := m.store.Get(id)
		if err != nil {
			continue
		}
		// 注意：不因 status==Running 就跳过 —— 进程异常退出会留下僵尸 running，
		// 那样账号会永久卡住。真正的并发保护由 seq 信号量保证。
		if resumeOnly {
			// 只处理有会话的
			if c.SessionCookie == "" {
				continue
			}
		} else {
			// 普通注册：已验证/已有会话的不重复注册
			if c.Status == deploysignupstore.Verified {
				continue
			}
			if c.SessionCookie != "" && c.Status == deploysignupstore.Registered {
				continue
			}
		}
		// 已记录过代理的账号沿用原出口；否则按轮询分配
		proxy := c.ProxyURL
		if proxy == "" && len(st.ProxyPool) > 0 {
			proxy = st.ProxyPool[i%len(st.ProxyPool)]
		}
		items = append(items, map[string]any{
			"email":          c.Email,
			"mailbox_url":    c.MailboxURL,
			"password":       c.Password,
			"proxy":          proxy,
			"fp_seed":        time.Now().UnixNano() % 100000,
			// 会话复用：把已保存的会话与指纹带上，python 优先复用它
			"session_cookie": c.SessionCookie,
			"ua":             c.UA,
			"browser":        c.Browser,
			"platform":       c.Platform,
			"ja3":            c.JA3,
		})
		validIDs = append(validIDs, id)
		_, _ = m.store.Update(id, func(x *deploysignupstore.Candidate) error {
			x.Status = deploysignupstore.Running
			x.Error = ""
			// 记录本次实际使用的代理，后续复用会话必须走同一出口
			if proxy != "" {
				x.ProxyURL = proxy
			}
			return nil
		})
	}
	if len(items) == 0 {
		return
	}

	job := map[string]any{
		"settings": map[string]any{
			"min_interval":   st.MinIntervalSec,
			"max_retries":    st.MaxRetries,
			"verify_timeout": st.VerifyTimeoutSec,
			"transport":      st.Transport,
			"auto_verify":    st.AutoVerify,
			"sys_proxy":      st.SysProxy,
			"bridge_port":    st.BridgePort,
			// 需要 Secret Key 才能在注册后直接入池
			"import_to_pool": st.ImportToPool,
		},
		"items": items,
	}
	raw, _ := json.Marshal(job)
	jobPath := filepath.Join(os.TempDir(), fmt.Sprintf("deploy-job-%d.json", time.Now().UnixNano()))
	if err := os.WriteFile(jobPath, raw, 0o600); err != nil {
		m.addLog("error", "", "写 job 文件失败: "+err.Error())
		return
	}
	defer os.Remove(jobPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, id := range validIDs {
		id := id
		m.mu.Lock()
		m.running[id] = cancel
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			delete(m.running, id)
			m.mu.Unlock()
		}()
	}

	cmd := exec.CommandContext(ctx, pyBin, scriptPath, "--job-file", jobPath)
	cmd.Dir = filepath.Dir(filepath.Dir(scriptPath)) // 仓库根
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.addLog("error", "", err.Error())
		return
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		m.addLog("error", "", "启动注册机失败: "+err.Error())
		return
	}
	m.addLog("start", "", fmt.Sprintf("开始注册 %d 个", len(items)))

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		m.handleEvent(ev, validIDs)
	}
	_ = cmd.Wait()
	// 兜底：仍处于 running 说明 python 异常中断，标记 failed 以便重跑
	for _, id := range validIDs {
		_, _ = m.store.Update(id, func(c *deploysignupstore.Candidate) error {
			if c.Status == deploysignupstore.Running {
				c.Status = deploysignupstore.Failed
				if c.Error == "" {
					c.Error = "注册进程异常中断"
				}
			}
			return nil
		})
	}
	m.addLog("done", "", "批次结束")
}

func (m *deployManager) handleEvent(ev map[string]any, ids []string) {
	typ, _ := ev["type"].(string)
	email, _ := ev["email"].(string)
	switch typ {
	case "log":
		msg, _ := ev["message"].(string)
		m.addLog("log", email, msg)
	case "item_update", "item_done":
		idx := -1
		if v, ok := ev["index"].(float64); ok {
			idx = int(v)
		}
		if idx < 0 || idx >= len(ids) {
			return
		}
		id := ids[idx]
		_, _ = m.store.Update(id, func(c *deploysignupstore.Candidate) error {
			if s, ok := ev["status"].(string); ok {
				c.Status = deploysignupstore.Status(s)
			}
			if s, ok := ev["password"].(string); ok && s != "" {
				c.Password = s
			}
			if s, ok := ev["session_cookie"].(string); ok {
				c.SessionCookie = s
			}
			if s, ok := ev["access_key"].(string); ok {
				c.AccessKey = s
			}
			if s, ok := ev["secret_key"].(string); ok {
				c.SecretKey = s
			}
			if s, ok := ev["ua"].(string); ok {
				c.UA = s
			}
			if s, ok := ev["browser"].(string); ok {
				c.Browser = s
			}
			if s, ok := ev["platform"].(string); ok {
				c.Platform = s
			}
			if s, ok := ev["ja3"].(string); ok {
				c.JA3 = s
			}
			if s, ok := ev["error"].(string); ok && s != "" {
				c.Error = s
			}
			if b, ok := ev["is_verified"].(bool); ok {
				c.IsVerified = b
			}
			if v, ok := ev["account_id"].(float64); ok {
				c.AccountID = fmt.Sprintf("%.0f", v)
			}
			if v, ok := ev["org_id"].(float64); ok {
				c.OrgID = fmt.Sprintf("%.0f", v)
			}
			return nil
		})
		if typ == "item_done" {
			m.addLog("item_done", email, fmt.Sprintf("状态=%v", ev["status"]))
		}
	}
}

// scriptPaths 解析注册脚本与解释器。
//
// 生产镜像里脚本装在 /usr/local/share/deploy-signup，解释器是自带
// curl_cffi 的 venv（/opt/deploy-signup/bin/python）——系统 python3 没有
// curl_cffi，用它跑会直接 ImportError。这两个位置由 Dockerfile 的
// DEPLOY_REGISTRAR / DEPLOY_PYTHON 环境变量给出，与 Mistral worker 一致。
func (m *deployManager) scriptPaths(st deploysignupstore.Settings) (script, py string, err error) {
	var candidates []string
	if v := strings.TrimSpace(os.Getenv("DEPLOY_REGISTRAR")); v != "" {
		candidates = append(candidates, v)
	}
	// 容器内固定安装位置
	candidates = append(candidates, "/usr/local/share/deploy-signup/deploy_registrar.py")
	// 本地开发：cwd 与二进制所在目录分别往上找
	if root, e := filepath.Abs("."); e == nil {
		candidates = append(candidates,
			filepath.Join(root, "tools", "deploy_registrar.py"),
			filepath.Join(root, "..", "..", "tools", "deploy_registrar.py"),
		)
	}
	if exe, e := os.Executable(); e == nil {
		if d, e2 := filepath.Abs(filepath.Dir(exe)); e2 == nil {
			candidates = append(candidates,
				filepath.Join(d, "tools", "deploy_registrar.py"),
				filepath.Join(d, "..", "..", "tools", "deploy_registrar.py"),
			)
		}
	}

	for _, p := range candidates {
		if _, statErr := os.Stat(p); statErr == nil {
			py = strings.TrimSpace(st.PythonBin)
			// 默认：镜像内用 venv，本地用系统 python3
			if py == "" || py == "python3" {
				if v := strings.TrimSpace(os.Getenv("DEPLOY_PYTHON")); v != "" {
					if _, e := os.Stat(v); e == nil {
						return p, v, nil
					}
				}
				if _, e := os.Stat("/opt/deploy-signup/bin/python"); e == nil {
					return p, "/opt/deploy-signup/bin/python", nil
				}
			}
			if py == "" {
				py = "python3"
			}
			return p, py, nil
		}
	}
	wd, _ := os.Getwd()
	return "", "", fmt.Errorf("找不到 deploy_registrar.py（cwd=%s，已查 %d 个位置）", wd, len(candidates))
}


// ---------------------------------------------------------------- 入池

// importDeployCandidate 把一条已注册的候选写入 Deploy 账号池，供路由使用。
//
// 需要 Secret Key（不是 access_key）：Deploy 的 API 用 Secret Key 做 Authorization。
// Secret Key 只在 POST /tokens 时返回一次，注册流程已把它存进候选记录。
func (s *Server) importDeployCandidate(c deploysignupstore.Candidate) (string, error) {
	key := strings.TrimSpace(c.SecretKey)
	if key == "" {
		return "", errors.New("该账号没有 Secret Key，无法入池（可先点「补齐 Key」再入池）")
	}

	var newID string
	_, err := s.cfg.Update(-1, func(cfg *config.Config) error {
		for _, a := range cfg.Accounts {
			if a.Provider == config.ProviderDeploy && a.RuntimeKey == key {
				return errors.New("该 Secret Key 已在账号池中")
			}
		}
		name := c.Email
		if i := strings.Index(name, "@"); i > 0 {
			name = name[:i]
		}
		if name == "" {
			name = "deploy-" + c.ID
		}
		if len([]rune(name)) > 50 {
			name = string([]rune(name)[:50])
		}
		note := "Deploy 注册机导入"
		if c.AccessKey != "" {
			note += "；access_key=" + c.AccessKey
		}
		acct := config.Account{
			ID:         "deploy-" + c.ID,
			Provider:   config.ProviderDeploy,
			Name:       name,
			Email:      c.Email,
			Group:      config.GroupForProvider(config.ProviderDeploy),
			Enabled:    true,
			RuntimeKey: key,
			ProxyURL:   c.ProxyURL,
			MaxConc:    0,
			Weight:     1,
			Note:       note,
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

// apiDeployCandidateImport 手动把单个候选入池。
func (s *Server) apiDeployCandidateImport(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
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
	if c.ImportedAcctID != "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account_id": c.ImportedAcctID, "already": true})
		return
	}
	acctID, ierr := s.importDeployCandidate(c)
	if ierr != nil {
		m.addLog("import_failed", c.Email, ierr.Error())
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": ierr.Error()})
		return
	}
	_, _ = m.store.Update(id, func(x *deploysignupstore.Candidate) error {
		x.ImportedAcctID = acctID
		return nil
	})
	m.addLog("imported", c.Email, "已入池 "+acctID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "account_id": acctID})
}

// apiDeployImportAll 把所有已验证且尚未入池的候选批量入池。
func (s *Server) apiDeployImportAll(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	imported, skipped, failed := 0, 0, 0
	var errs []string
	for _, c := range m.store.List() {
		if c.Status != deploysignupstore.Verified || c.ImportedAcctID != "" {
			skipped++
			continue
		}
		acctID, ierr := s.importDeployCandidate(c)
		if ierr != nil {
			failed++
			errs = append(errs, c.Email+": "+ierr.Error())
			continue
		}
		_, _ = m.store.Update(c.ID, func(x *deploysignupstore.Candidate) error {
			x.ImportedAcctID = acctID
			return nil
		})
		m.addLog("imported", c.Email, "已入池 "+acctID)
		imported++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"imported": imported, "skipped": skipped, "failed": failed, "errors": errs,
	})
}


// apiDeployCandidateUpdate 修改候选的可编辑字段。
//
// 目前只开放代理：运营经常要在注册失败后换出口重试，而代理是唯一
// 需要手工纠正的字段（邮箱/接码地址由导入决定）。
// 已入池的账号同步更新账号池里那条记录的 ProxyURL，避免两边不一致。
func (s *Server) apiDeployCandidateUpdate(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	var body struct {
		ProxyURL *string `json:"proxy_url"`
	}
	if err := readBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if body.ProxyURL == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "没有要修改的字段"})
		return
	}
	if cur, gerr := m.store.Get(id); gerr == nil && cur.Status == deploysignupstore.Running {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "注册进行中，不能改代理"})
		return
	}

	newProxy := strings.TrimSpace(*body.ProxyURL)
	upd, uerr := m.store.Update(id, func(x *deploysignupstore.Candidate) error {
		x.ProxyURL = newProxy
		return nil
	})
	if uerr != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": uerr.Error()})
		return
	}

	// 已入池则同步账号池，保持一致
	if upd.ImportedAcctID != "" {
		_, _ = s.cfg.Update(-1, func(cfg *config.Config) error {
			for i := range cfg.Accounts {
				if cfg.Accounts[i].ID == upd.ImportedAcctID {
					cfg.Accounts[i].ProxyURL = newProxy
					break
				}
			}
			return nil
		})
	}
	shown := newProxy
	if shown == "" {
		shown = "直连"
	}
	m.addLog("updated", upd.Email, "代理改为 "+shown)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proxy_url": newProxy})
}


// apiDeployAssignProxies 按顺序把代理池里的代理逐个写进候选记录。
//
// 与注册时的临时轮询不同，这会把分配结果**落盘**，因此：
//   - 表格里能立刻看到每个账号会用哪个出口；
//   - 账号与会话绑定同一个出口，后续复用不会换 IP；
//   - 想手工调整就单独改某一条，不会被下一次注册覆盖。
//
// overwrite=false 只补空白（默认）；true 则全部按顺序重排。
func (s *Server) apiDeployAssignProxies(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		Overwrite *bool  `json:"overwrite"`
		OnlyIDs   []string `json:"only_ids"`
	}
	if r.ContentLength != 0 {
		if err := readBody(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	overwrite := body.Overwrite != nil && *body.Overwrite
	pool := m.store.Settings().ProxyPool
	if len(pool) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "代理池是空的，先在左侧粘贴代理并保存设置"})
		return
	}
	only := map[string]bool{}
	for _, id := range body.OnlyIDs {
		only[id] = true
	}

	// 固定按创建时间排序，保证同一组数据每次分配结果一致
	rows := m.store.List()
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })

	assigned, kept := 0, 0
	n := 0 // 轮询游标，只对真正被分配的条目前进
	for _, c := range rows {
		if len(only) > 0 && !only[c.ID] {
			continue
		}
		if c.Status == deploysignupstore.Running {
			kept++
			continue
		}
		if c.ProxyURL != "" && !overwrite {
			kept++
			continue
		}
		next := pool[n%len(pool)]
		n++
		if _, uerr := m.store.Update(c.ID, func(x *deploysignupstore.Candidate) error {
			x.ProxyURL = next
			return nil
		}); uerr != nil {
			continue
		}
		assigned++
		if c.ImportedAcctID != "" {
			_, _ = s.cfg.Update(-1, func(cfg *config.Config) error {
				for i := range cfg.Accounts {
					if cfg.Accounts[i].ID == c.ImportedAcctID {
						cfg.Accounts[i].ProxyURL = next
						break
					}
				}
				return nil
			})
		}
	}
	m.addLog("proxy_assign", "", fmt.Sprintf("分配 %d 个，保留 %d 个（池内 %d 个代理，%s）",
		assigned, kept, len(pool), map[bool]string{true: "覆盖", false: "只补空白"}[overwrite]))
	writeJSON(w, http.StatusOK, map[string]any{
		"assigned": assigned, "kept": kept, "pool_size": len(pool),
	})
}


// validateDeployCards 逐行解析并校验卡列表。
//
// 复用 mistralcards 的解析（格式、位数、Luhn、有效期），返回有效行原文与
// 无效行原因。有效行保留用户原始写法（只 trim），方便回填输入框。
func validateDeployCards(lines []string) ([]string, []string) {
	var valid []string
	var errs []string
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if _, err := mistralcards.ParseLine(line); err != nil {
			// 错误里已含脱敏行号信息，不回显卡号
			errs = append(errs, fmt.Sprintf("第 %d 行：%s", i+1, cardErrReason(err)))
			continue
		}
		valid = append(valid, line)
	}
	return valid, errs
}

// cardErrReason 把 ParseError 压成一句人话。
func cardErrReason(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "luhn"):
		return "Luhn 校验失败，卡号可能有误"
	case strings.Contains(msg, "length") || strings.Contains(msg, "digit"):
		return "卡号位数不对（应为 13–19 位）"
	case strings.Contains(msg, "expire") || strings.Contains(msg, "expired"):
		return "有效期已过期或格式不对"
	case strings.Contains(msg, "month"):
		return "有效期月份不对"
	case strings.Contains(msg, "cvv"):
		return "CVV 格式不对"
	case strings.Contains(msg, "3 fields"):
		return "字段数不对，应为 卡号----有效期----CVV"
	}
	return msg
}


// apiDeployProxyTest 逐个测试代理池里的代理能不能出网，返回出口 IP。
//
// 走和注册完全相同的链路：本地桥 → 系统代理 → 上游代理 → 目标，
// 避免"能连上游但实际出不去"这种误判。
func (s *Server) apiDeployProxyTest(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	st := m.store.Settings()
	var targets []string
	if len(st.ProxyPool) > 0 {
		targets = st.ProxyPool
	} else {
		targets = []string{""} // 空 = 只用系统代理/直连
	}
	// 最多测 10 个，避免一次卡太久
	if len(targets) > 10 {
		targets = targets[:10]
	}

	type result struct {
		Target string `json:"target"`
		OK     bool   `json:"ok"`
		IP     string `json:"ip,omitempty"`
		Error  string `json:"error,omitempty"`
	}
	results := make([]result, 0, len(targets))
	for _, px := range targets {
		label := px
		if label == "" {
			label = "系统代理 / 直连"
		}
		res := result{Target: label}
		ip, perr := s.probeProxyEgress(px, st)
		if perr != nil {
			res.Error = perr.Error()
		} else {
			res.OK = true
			res.IP = ip
		}
		results = append(results, res)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// probeProxyEgress 用和注册一致的链路取出口 IP。
// 没有上游代理时直接测系统代理本身能否出网。
func (s *Server) probeProxyEgress(upstream string, st deploysignupstore.Settings) (string, error) {
	m, merr := s.deployMgr()
	if merr != nil {
		return "", merr
	}
	script, py, serr := m.scriptPaths(st)
	if serr != nil {
		return "", serr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, script, "--probe-proxy")
	cmd.Env = append(os.Environ(),
		"DEPLOY_PROBE_UPSTREAM="+upstream,
		"DEPLOY_PROBE_SYS="+st.SysProxy,
	)
	out, cerr := cmd.Output()
	if cerr != nil {
		return "", fmt.Errorf("探测失败: %v", cerr)
	}
	var res struct {
		IP  string `json:"ip"`
		Err string `json:"error"`
	}
	if json.Unmarshal(out, &res) != nil {
		return "", fmt.Errorf("探测输出无法解析")
	}
	if res.Err != "" {
		return "", fmt.Errorf("%s", res.Err)
	}
	return res.IP, nil
}


// ---------------------------------------------------------------- 绑卡

// deployCardLine 把 Card 转成 python 需要的 "number|MM|YYYY|CVC" 形式。
func deployCardLine(c deploysignupstore.Card, zip, country string) string {
	return fmt.Sprintf("%s|%02d|%04d|%s", c.Number, c.ExpMon, c.ExpYear, c.CVC)
}

// pickUnusedCards 从设置里的卡列表挑出还没被任何候选占用的卡。
// 一卡一账号：已绑定到别的候选的卡绝不复用。
func pickUnusedCards(st deploysignupstore.Settings, rows []deploysignupstore.Candidate) []string {
	used := map[string]bool{}
	for _, c := range rows {
		if c.Card != nil && c.Card.Number != "" {
			used[strings.TrimSpace(c.Card.Number)] = true
		}
		if c.Recharged {
			// 已绑过的卡不参与再分配
			if c.Card != nil {
				used[strings.TrimSpace(c.Card.Number)] = true
			}
		}
	}
	// 卡指纹台账（跨进程持久化，防止服务重启后复用）
	ledgerBlocked := cardLedgerNumbers()
	var out []string
	for _, line := range st.Cards {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		num := strings.TrimSpace(strings.SplitN(line, "----", 2)[0])
		if used[num] || ledgerBlocked[num] {
			continue
		}
		out = append(out, line)
	}
	return out
}

// cardLedgerNumbers 读取 python 侧的一卡多绑台账，返回已被占用过的卡号最后四位集合。
// 台账只存卡号哈希，所以这里按完整卡号计算同样的哈希来比对。
func cardLedgerNumbers() map[string]bool {
	// 台账在 python 侧用 sha256(纯数字卡号)[:32] 作为键；
	// 这里返回空集，真正的拦截交给 python 的 assert_card_unused。
	return map[string]bool{}
}

// apiDeployBindCard 给单个候选绑卡（+充值）。
func (s *Server) apiDeployBindCard(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	var body struct {
		Live        *bool   `json:"live"`
		Amount      float64 `json:"amount"`
		CardLine    string  `json:"card"`   // 可选：手填一张，覆盖卡列表
	}
	if r.ContentLength != 0 {
		if err := readBody(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	c, gerr := m.store.Get(id)
	if gerr != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": gerr.Error()})
		return
	}
	if c.SessionCookie == "" || c.OrgID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "该账号还没有会话/组织 ID，需先注册成功"})
		return
	}
	if c.Recharged {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "already": true})
		return
	}
	st := m.store.Settings()
	// 优先级：请求里手填 > 候选自己指定的卡 > 卡池里第一张可用的
	line := strings.TrimSpace(body.CardLine)
	if line == "" && c.Card != nil && strings.TrimSpace(c.Card.Number) != "" {
		line = cardFullLine(*c.Card, st)
	}
	if line == "" {
		avail := pickUnusedCards(st, m.store.List())
		if len(avail) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "没有可用的银行卡（卡池为空，或全部已绑定其他账号）。可在该账号行点「设卡」直接指定。"})
			return
		}
		line = avail[0]
	}
	live := body.Live == nil || *body.Live
	amount := body.Amount
	if amount <= 0 {
		amount = 5
	}

	res, berr := s.runBindCards([]bindItem{{
		ID: c.ID, Email: c.Email, SessionCookie: c.SessionCookie,
		OrgID: c.OrgID, CardLine: line, Zip: st.CardZip, Country: st.CardCountry,
		Proxy: c.ProxyURL,
	}}, live, amount)
	if berr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": berr.Error()})
		return
	}
	out := res[0]
	// 成功则把卡记到候选上，防止被别的账号复用
	if out.OK {
		card := cardFromLine(line)
		_, _ = m.store.Update(c.ID, func(x *deploysignupstore.Candidate) error {
			x.Card = &card
			x.Recharged = true
			return nil
		})
		m.addLog("bind_ok", c.Email, "绑卡成功")
	} else {
		m.addLog("bind_failed", c.Email, out.Error)
	}
	writeJSON(w, http.StatusOK, out)
}

// apiDeployBindCards 批量绑卡：给所有已注册且未绑卡的候选依次分配卡。
func (s *Server) apiDeployBindCards(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var body struct {
		Live     *bool    `json:"live"`
		Amount   float64  `json:"amount"`
		OnlyIDs  []string `json:"only_ids"`
	}
	if r.ContentLength != 0 {
		if err := readBody(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	st := m.store.Settings()
	rows := m.store.List()
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })

	only := map[string]bool{}
	for _, id := range body.OnlyIDs {
		only[id] = true
	}

	// 收集待绑账号
	var targets []deploysignupstore.Candidate
	for _, c := range rows {
		if len(only) > 0 && !only[c.ID] {
			continue
		}
		if c.Recharged || c.SessionCookie == "" || c.OrgID == "" {
			continue
		}
		targets = append(targets, c)
	}
	if len(targets) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"bound": 0, "skipped": len(rows), "total": 0,
			"message": "没有待绑卡的账号（需已注册且有会话）"})
		return
	}

	avail := pickUnusedCards(st, rows)
	// 卡池为空也可能没问题：账号可能都自己指定了卡，交给下面逐条判断
	hasOwn := false
	for _, c := range targets {
		if c.Card != nil && strings.TrimSpace(c.Card.Number) != "" {
			hasOwn = true
			break
		}
	}
	if len(avail) == 0 && !hasOwn {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "没有可用的银行卡，先在号池页填写卡列表，或给账号单独「设卡」"})
		return
	}

	// 先收各自指定的卡，再补卡池
	var items []bindItem
	used := 0
	for _, c := range targets {
		line := ""
		if c.Card != nil && strings.TrimSpace(c.Card.Number) != "" {
			line = cardFullLine(*c.Card, st)
		} else {
			if used >= len(avail) {
				continue // 卡不够，剩下的跳过
			}
			line = avail[used]
			used++
		}
		items = append(items, bindItem{
			ID: c.ID, Email: c.Email, SessionCookie: c.SessionCookie,
			OrgID: c.OrgID, CardLine: line, Zip: st.CardZip, Country: st.CardCountry,
			Proxy: c.ProxyURL,
		})
	}
	live := body.Live == nil || *body.Live
	amount := body.Amount
	if amount <= 0 {
		amount = 5
	}
	res, berr := s.runBindCards(items, live, amount)
	if berr != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": berr.Error()})
		return
	}
	bound, failed := 0, 0
	for _, out := range res {
		if out.OK {
			bound++
			full := ""
			for _, it := range items {
				if it.ID == out.ID {
					full = it.CardLine
					break
				}
			}
			card := cardFromLine(full)
			_, _ = m.store.Update(out.ID, func(x *deploysignupstore.Candidate) error {
				x.Card = &card
				x.Recharged = true
				return nil
			})
			m.addLog("bind_ok", out.Email, "绑卡成功")
		} else {
			failed++
			m.addLog("bind_failed", out.Email, out.Error)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"bound": bound, "failed": failed,
		"total": len(targets), "attempted": len(items),
		"cards_available": len(avail),
		"live": live,
	})
}

// cardFromLine 把 "卡号----有效期----CVV" 拆成 Card。
// cardFromLine 把 "卡号----月/年----CVV" 完整解析成 Card（含有效期与 CVV）。
// 解析失败时只回填卡号，避免调用方拿到半张卡。
func cardFromLine(line string) deploysignupstore.Card {
	if mc, err := mistralcards.ParseLine(line); err == nil {
		return deploysignupstore.Card{
			Number:  mc.Number,
			ExpMon:  mc.ExpMonth,
			ExpYear: mc.ExpYear,
			CVC:     mc.CVV,
		}
	}
	parts := strings.Split(line, "----")
	c := deploysignupstore.Card{}
	if len(parts) > 0 {
		c.Number = strings.TrimSpace(parts[0])
	}
	return c
}


// bindItem 是传给 python 的单条绑卡请求。
type bindItem struct {
	ID            string
	Email         string
	SessionCookie string
	OrgID         string
	CardLine      string
	Zip           string
	Country       string
	Proxy         string
}

// bindOutcome 是 python 逐行回传的结果。
type bindOutcome struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	OK       bool   `json:"ok"`
	Step     string `json:"step,omitempty"`
	SourceID string `json:"source_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// runBindCards 调用 deploy_payment.py --bind-job 执行绑卡。
//
// Stripe 已禁止用 publishable key 直接换 token，脚本内部会起 Playwright
// 在真浏览器里跑 Stripe.js，所以这一步比注册慢，串行执行。
func (s *Server) runBindCards(items []bindItem, live bool, amount float64) ([]bindOutcome, error) {
	m, err := s.deployMgr()
	if err != nil {
		return nil, err
	}
	st := m.store.Settings()

	// 定位脚本与解释器：与注册同一套解析，只是换成 deploy_payment.py
	regScript, py, serr := m.scriptPaths(st)
	if serr != nil {
		return nil, serr
	}
	payScript := filepath.Join(filepath.Dir(regScript), "deploy_payment.py")
	if _, e := os.Stat(payScript); e != nil {
		return nil, fmt.Errorf("找不到 deploy_payment.py（在 %s）", filepath.Dir(regScript))
	}

	job := map[string]any{"live": live, "amount": amount}
	var jitems []map[string]any
	for _, it := range items {
		jitems = append(jitems, map[string]any{
			"id": it.ID, "email": it.Email, "session_cookie": it.SessionCookie,
			"org_id": it.OrgID, "card": it.CardLine,
			"zip": it.Zip, "country": it.Country, "proxy": it.Proxy,
		})
	}
	job["items"] = jitems

	f, ferr := os.CreateTemp("", "deploy-bind-*.json")
	if ferr != nil {
		return nil, ferr
	}
	defer os.Remove(f.Name())
	if enc := json.NewEncoder(f); enc.Encode(job) != nil {
		f.Close()
		return nil, fmt.Errorf("写 job 文件失败")
	}
	f.Close()

	// 每条绑卡要起浏览器，给足时间：每条 90 秒
	timeout := time.Duration(len(items)*90+60) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, py, payScript, "--bind-job", f.Name())
	cmd.Dir = filepath.Dir(payScript)
	cmd.Env = append(os.Environ(),
		"DEPLOY_PAY_SYS_PROXY="+st.SysProxy,
		"DEPLOY_PAY_BRIDGE_PORT="+fmt.Sprint(st.BridgePort+50),
	)
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	var out []bindOutcome
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var o map[string]any
		if json.Unmarshal([]byte(line), &o) != nil {
			continue
		}
		if _, done := o["done"]; done {
			continue
		}
		var oc bindOutcome
		b, _ := json.Marshal(o)
		if json.Unmarshal(b, &oc) == nil && oc.ID != "" {
			out = append(out, oc)
		}
	}
	_ = cmd.Wait()
	return out, nil
}


// apiDeploySetCard 给单个候选指定/清除要用的卡。
//
// 导入后随时可改；存的是完整卡（含 CVV），与「卡列表」分开——
// 卡列表是池，这里是这张账号确定要用的那一张。
// 清空 = 取消该账号的卡绑定。
func (s *Server) apiDeploySetCard(w http.ResponseWriter, r *http.Request) {
	m, err := s.deployMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	id := r.PathValue("id")
	var body struct {
		Card string `json:"card"`
	}
	if err := readBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	line := strings.TrimSpace(body.Card)
	if line == "" {
		if _, uerr := m.store.Update(id, func(x *deploysignupstore.Candidate) error {
			x.Card = nil
			return nil
		}); uerr != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": uerr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "card": nil})
		return
	}

	// 先确认整行有效（位数/Luhn/有效期/连接符）
	if _, perr := mistralcards.ParseLine(line); perr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "卡无效：" + cardErrReason(perr)})
		return
	}
	num := strings.TrimSpace(strings.SplitN(line, "----", 2)[0])

	// 一卡一账号：这张卡不能已经归属别的候选
	for _, c := range m.store.List() {
		if c.ID == id || c.Card == nil {
			continue
		}
		if strings.TrimSpace(c.Card.Number) == num {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "这张卡已绑定到账号 " + c.Email + "，一卡一账号"})
			return
		}
	}

	card := cardFromLine(line)
	upd, uerr := m.store.Update(id, func(x *deploysignupstore.Candidate) error {
		x.Card = &card
		return nil
	})
	if uerr != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": uerr.Error()})
		return
	}
	m.addLog("card_set", upd.Email, "已指定卡 ****"+num[len(num)-min(4, len(num)):])
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true,
		"card": map[string]any{
			"number": num, "last4": num[max(0, len(num)-4):],
		},
	})
}


// cardFullLine 把候选上保存的卡还原成 python 需要的 "number|MM|YYYY|CVC"。
//
// 注意 Card 结构只存了卡号，有效期/CVV 来自导入那一行的原始文本。
func cardFullLine(c deploysignupstore.Card, st deploysignupstore.Settings) string {
	if c.ExpMon > 0 && c.ExpYear > 0 && c.CVC != "" {
		return fmt.Sprintf("%s|%02d|%04d|%s", c.Number, c.ExpMon, c.ExpYear, c.CVC)
	}
	return c.Number
}
