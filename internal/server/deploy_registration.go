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
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/deploysignupstore"
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
	if err := m.store.SaveSettings(in); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"settings": m.store.Settings()})
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
