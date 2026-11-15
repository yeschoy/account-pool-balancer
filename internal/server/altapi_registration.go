package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/altapisignupstore"
	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/newapiconsole"
)

// altapiManager drives tools/altapi_registrar.py as a subprocess. The Python
// side prints one JSON event per line; nothing here parses human text, so a
// wording change in the script's log lines cannot break the pool.
type altapiManager struct {
	srv   *Server
	store *altapisignupstore.Store

	mu      sync.Mutex
	running map[string]context.CancelFunc
	seq     chan struct{} // capacity 1: one batch at a time

	logMu sync.Mutex
	logs  []altapiLogLine
}

type altapiLogLine struct {
	At      time.Time `json:"at"`
	Type    string    `json:"type"`
	Email   string    `json:"email,omitempty"`
	Message string    `json:"message,omitempty"`
}

const maxALTAPILogs = 500

func (s *Server) registerALTAPI(mux *http.ServeMux, a func(string, http.HandlerFunc)) {
	a("GET /api/altapi/candidates", s.apiALTAPICandidates)
	a("POST /api/altapi/candidates", s.apiALTAPICandidateAdd)
	a("POST /api/altapi/candidates/bulk", s.apiALTAPICandidateBulk)
	a("PATCH /api/altapi/candidates/{id}", s.apiALTAPICandidateUpdate)
	a("DELETE /api/altapi/candidates/{id}", s.apiALTAPICandidateDelete)
	a("POST /api/altapi/candidates/{id}/register", s.apiALTAPICandidateRegister)
	a("POST /api/altapi/candidates/bulk-register", s.apiALTAPIBulkRegister)
	a("POST /api/altapi/candidates/{id}/cancel", s.apiALTAPICandidateCancel)
	a("POST /api/altapi/candidates/{id}/import", s.apiALTAPICandidateImport)
	a("POST /api/altapi/candidates/import-all", s.apiALTAPIImportAll)
	a("POST /api/altapi/candidates/{id}/quota", s.apiALTAPICandidateQuota)
	a("POST /api/altapi/quota-refresh", s.apiALTAPIQuotaRefresh)
	a("POST /api/altapi/proxy-test", s.apiALTAPIProxyTest)
	a("GET /api/altapi/settings", s.apiALTAPISettings)
	a("PUT /api/altapi/settings", s.apiALTAPISaveSettings)
	a("GET /api/altapi/logs", s.apiALTAPILogs)
	a("GET /api/altapi/status", s.apiALTAPIStatus)
}

func (s *Server) altapiStore() (*altapisignupstore.Store, error) {
	s.mistralMu.Lock()
	defer s.mistralMu.Unlock()
	if s.altapiStoreRef != nil {
		return s.altapiStoreRef, nil
	}
	path := filepath.Join(s.dataDir, "altapi-signup.json")
	st, err := altapisignupstore.Open(path)
	if err != nil {
		return nil, err
	}
	s.altapiStoreRef = st
	return st, nil
}

func (s *Server) altapiMgr() (*altapiManager, error) {
	st, err := s.altapiStore()
	if err != nil {
		return nil, err
	}
	s.mistralMu.Lock()
	defer s.mistralMu.Unlock()
	if s.altapiMgrRef != nil {
		return s.altapiMgrRef, nil
	}
	m := &altapiManager{
		srv:     s,
		store:   st,
		running: map[string]context.CancelFunc{},
		seq:     make(chan struct{}, 1),
	}
	s.altapiMgrRef = m
	return m, nil
}

func (m *altapiManager) addLog(typ, email, msg string) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	m.logs = append(m.logs, altapiLogLine{At: time.Now(), Type: typ, Email: email, Message: msg})
	if len(m.logs) > maxALTAPILogs {
		m.logs = m.logs[len(m.logs)-maxALTAPILogs:]
	}
}

func (m *altapiManager) Logs() []altapiLogLine {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	out := make([]altapiLogLine, len(m.logs))
	copy(out, m.logs)
	return out
}

// scriptPaths resolves the registrar and its interpreter.
//
// ALTAPI_REGISTRAR wins (the image installs it elsewhere); otherwise the repo
// checkout is searched, because every local run is from a checkout.
func (m *altapiManager) scriptPaths(st altapisignupstore.Settings) (script, py string, err error) {
	var candidates []string
	if v := strings.TrimSpace(os.Getenv("ALTAPI_REGISTRAR")); v != "" {
		candidates = append(candidates, v)
	}
	candidates = append(candidates, "/usr/local/share/altapi-signup/altapi_registrar.py")
	if wd, e := os.Getwd(); e == nil {
		candidates = append(candidates,
			filepath.Join(wd, "tools", "altapi_registrar.py"),
			filepath.Join(wd, "..", "tools", "altapi_registrar.py"))
	}
	if exe, e := os.Executable(); e == nil {
		d := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(d, "tools", "altapi_registrar.py"),
			filepath.Join(d, "..", "tools", "altapi_registrar.py"))
	}
	for _, c := range candidates {
		if fi, e := os.Stat(c); e == nil && !fi.IsDir() {
			script = c
			break
		}
	}
	if script == "" {
		return "", "", fmt.Errorf("找不到 altapi_registrar.py（已查 %d 个位置）", len(candidates))
	}
	py = resolveALTAPIPython(st.PythonBin)
	return script, py, nil
}

// hasSocksProxy 判断代理池里是否有 socks 协议条目。
func hasSocksProxy(proxies []any) bool {
	for _, p := range proxies {
		s, _ := p.(string)
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "socks") {
			return true
		}
	}
	return false
}

// checkSocksSupport 用同一个解释器试 import socks。
func (m *altapiManager) checkSocksSupport(py string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, py, "-c", "import socks").CombinedOutput()
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if i := strings.LastIndex(msg, "ModuleNotFoundError"); i >= 0 {
		msg = strings.TrimSpace(msg[i:])
	}
	return fmt.Errorf("代理池有 socks 条目，但解释器 %s 没有 PySocks（%s）；"+
		"把「注册机解释器」留空以使用镜像内 venv，或改装好依赖的解释器", py, msg)
}

// resolveALTAPIPython 决定用哪个解释器跑注册机。
//
// 优先级：
//   1. 设置里写的**绝对路径**（用户明确指定，尊重它）
//   2. ALTAPI_PYTHON 环境变量
//   3. 镜像里的 /opt/altapi-signup venv（带 PySocks）
//   4. 裸 "python3"
//
// 「裸命令名」不算有效指定：设置页的默认值就是 "python3"，用户点过一次保存
// 就会把它写进配置文件，于是永远走系统解释器——而系统解释器没装 PySocks，
// 候选一旦绑了 socks5 出口就在 import 阶段全量失败（线上真实踩到：
// 12 条候选全是「需要 PySocks 才能用 socks 代理」）。
func resolveALTAPIPython(setting string) string {
	if p := strings.TrimSpace(setting); p != "" && filepath.IsAbs(p) {
		return p
	}
	if v := strings.TrimSpace(os.Getenv("ALTAPI_PYTHON")); v != "" {
		return v
	}
	if v := altapiDefaultPython(); v != "" {
		return v
	}
	if p := strings.TrimSpace(setting); p != "" {
		return p // 非绝对路径但用户确实写了，如 "python3.11"
	}
	return "python3"
}

// altapiDefaultPython 返回镜像里带 PySocks 的解释器。
//
// 注册机是纯标准库实现，只有候选绑了 socks5 出口时才 import socks；用系统
// python3 会在那条分支上抛 ModuleNotFoundError。Dockerfile 的
// /opt/altapi-signup 是唯一装了依赖的 venv，所以优先用它，找不到再退回 python3。
func altapiDefaultPython() string {
	const venv = "/opt/altapi-signup/bin/python3"
	if fi, err := os.Stat(venv); err == nil && !fi.IsDir() {
		return venv
	}
	return ""
}

// jobFile is the JSON the registrar reads with --job-file.
type altapiJobFile struct {
	Settings map[string]any     `json:"settings"`
	Items    []altapiJobItem    `json:"items"`
}
type altapiJobItem struct {
	Index      int    `json:"index"`
	Email      string `json:"email"`
	MailboxURL string `json:"mailbox_url"`
	Password   string `json:"password,omitempty"`
	Username   string `json:"username,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
}

// runBatch registers the given candidate ids in one subprocess.
func (m *altapiManager) runBatch(ids []string) {
	if len(ids) == 0 {
		return
	}
	// one batch at a time; the send window is per-mailbox but the exit IP
	// budget is shared, so serializing batches keeps CF calm
	m.seq <- struct{}{}
	defer func() { <-m.seq }()

	st := m.store.Settings()
	script, py, err := m.scriptPaths(st)
	if err != nil {
		m.addLog("error", "", err.Error())
		return
	}

	proxies := make([]any, 0, len(st.ProxyPool))
	for _, p := range st.ProxyPool {
		if strings.TrimSpace(p) != "" {
			proxies = append(proxies, p)
		}
	}

	// 启动前自检：池子里有 socks 代理而解释器没装 PySocks 时，整批会在第一条
	// 就全量失败（线上踩过：12 条候选全报「需要 PySocks」）。这里先探一次，
	// 一次性把原因写进日志并中止，而不是让用户等 12 条跑完才看明白。
	if hasSocksProxy(proxies) {
		if err := m.checkSocksSupport(py); err != nil {
			m.addLog("error", "", err.Error())
			return
		}
	}

	job := altapiJobFile{Items: []altapiJobItem{}}
	job.Settings = map[string]any{
		"base_url":         st.BaseURL,
		"min_interval_sec": st.MinIntervalSec,
		"max_retries":      st.MaxRetries,
		"verify_timeout_sec": st.VerifyTimeoutSec,
		"auto_verify":      st.AutoVerify,
		"import_to_pool":   st.ImportToPool,
		"token_group":      st.TokenGroup,
		"token_name":       st.TokenName,
		"aff_code":         st.AffCode,
		"proxy_pool":       proxies,
		"pace_scale":       st.PaceScale,
		"code_delay_sec":   st.CodeDelaySec,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		c, err := m.store.Get(id)
		if err != nil {
			continue
		}
		if c.Status == altapisignupstore.Cancelled {
			continue
		}
		idx := len(job.Items)
		job.Items = append(job.Items, altapiJobItem{
			Index: idx, Email: c.Email, MailboxURL: c.MailboxURL,
			Password: c.Password, Username: c.Username, ProxyURL: c.ProxyURL,
		})
		valid = append(valid, id)
		_, _ = m.store.Update(id, func(cc *altapisignupstore.Candidate) error {
			cc.Status = altapisignupstore.Running
			cc.Attempts++
			cc.Error = ""
			cc.Detail = "已排队"
			return nil
		})
		m.mu.Lock()
		m.running[id] = cancel
		m.mu.Unlock()
	}
	if len(job.Items) == 0 {
		return
	}
	defer func() {
		m.mu.Lock()
		for _, id := range valid {
			delete(m.running, id)
		}
		m.mu.Unlock()
	}()

	tmp, err := os.CreateTemp("", "altapi-job-*.json")
	if err != nil {
		m.addLog("error", "", err.Error())
		return
	}
	jobPath := tmp.Name()
	defer os.Remove(jobPath)
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", " ")
	if err := enc.Encode(job); err != nil {
		tmp.Close()
		m.addLog("error", "", err.Error())
		return
	}
	tmp.Close()

	cmd := exec.CommandContext(ctx, py, script, "--job-file", jobPath)
	cmd.Dir = filepath.Dir(filepath.Dir(script))
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
	m.addLog("start", "", fmt.Sprintf("开始注册 %d 个", len(job.Items)))

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		m.handleEvent(ev, valid)
	}
	_ = cmd.Wait()

	// A python crash leaves rows in running; mark them failed so they can rerun.
	for _, id := range valid {
		_, _ = m.store.Update(id, func(c *altapisignupstore.Candidate) error {
			if c.Status == altapisignupstore.Running || c.Status == altapisignupstore.SentCode {
				c.Status = altapisignupstore.Failed
				if c.Error == "" {
					c.Error = "注册进程异常中断"
				}
			}
			return nil
		})
	}
	m.addLog("done", "", "批次结束")
}

func (m *altapiManager) handleEvent(ev map[string]any, ids []string) {
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
		detail, _ := ev["detail"].(string)
		_, _ = m.store.Update(id, func(c *altapisignupstore.Candidate) error {
			if s, ok := ev["status"].(string); ok {
				c.Status = altapisignupstore.Status(s)
			}
			if detail != "" {
				c.Detail = detail
			} else if typ == "item_done" && c.Error == "" {
				c.Detail = ""
			}
			if s, ok := ev["password"].(string); ok && s != "" {
				c.Password = s
			}
			if s, ok := ev["username"].(string); ok && s != "" {
				c.Username = s
			}
			if s, ok := ev["session_cookie"].(string); ok {
				c.SessionCookie = s
			}
			if s, ok := ev["api_key"].(string); ok {
				c.APIKey = s
			}
			if s, ok := ev["token_id"].(string); ok {
				c.TokenID = s
			}
			if s, ok := ev["account_id"].(string); ok {
				c.AccountID = s
			}
			if s, ok := ev["group"].(string); ok {
				c.Group = s
			}
			if v, ok := ev["quota"].(float64); ok {
				c.Quota = v
			}
			if v, ok := ev["used_quota"].(float64); ok {
				c.UsedQuota = v
			}
			if v, ok := ev["quota_per_unit"].(float64); ok {
				c.QuotaPerUnit = v
			}
			if v, ok := ev["balance_usd"].(float64); ok {
				c.BalanceUSD = v
				c.QuotaCheckedAt = time.Now()
			}
			if s, ok := ev["error"].(string); ok && s != "" {
				c.Error = s
			}
			if s, ok := ev["ua"].(string); ok {
				c.UA = s
			}
			return nil
		})
		if typ == "item_done" {
			m.addLog("item_done", email, fmt.Sprintf("状态=%v", ev["status"]))
			// 自动入池：设置里开了 import_to_pool 时，注册出 Key 就立刻进号池，
			// 不用再去列表里逐条点「导入号池」。失败只记日志，不影响注册结果。
			if st, ok := ev["status"].(string); ok && st == string(altapisignupstore.KeyCreated) {
				m.autoImport(id, email)
			}
		}
	}
}

// autoImport 把一个刚建好 Key 的候选导入号池。
//
// 已导入的跳过（幂等）：批量重试可能让同一个候选再走一次 item_done，
// 而 importALTAPICandidate 已经有 ImportedAcctID 守卫，这里只负责记日志。
func (m *altapiManager) autoImport(id, email string) {
	if !m.store.Settings().ImportToPool {
		return
	}
	c, err := m.store.Get(id)
	if err != nil {
		return
	}
	if c.ImportedAcctID != "" {
		return
	}
	acct, err := m.srv.importALTAPICandidate(c)
	if err != nil {
		m.addLog("import_failed", email, "自动入池失败："+err.Error())
		return
	}
	m.addLog("imported", email, "已自动入池 "+acct.ID)
}

// ---------------------------------------------------------------- handlers

func (s *Server) apiALTAPICandidates(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": m.store.List(),
		"summary":    m.store.Summarize(),
		"settings":   m.store.Settings(),
	})
}

func (s *Server) apiALTAPICandidateAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email      string `json:"email"`
		MailboxURL string `json:"mailboxUrl"`
		ProxyURL   string `json:"proxyUrl"`
		Username   string `json:"username"`
		Password   string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, err := m.store.Add(altapisignupstore.Candidate{
		Email: in.Email, MailboxURL: in.MailboxURL, ProxyURL: in.ProxyURL,
		Username: in.Username, Password: in.Password,
		Status: altapisignupstore.Queued,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// apiALTAPICandidateBulk accepts the same "email----mailbox" lines the CLI
// uses, because that is what an iCloud share list already looks like.
func (s *Server) apiALTAPICandidateBulk(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lines string `json:"lines"`
		// Proxies 可选：按导入成功的顺序轮转绑定，一个候选一个出口。
		// 传了就用它（并把池子存进设置，下一次导入不用再贴一遍）；
		// 不传则回退到已保存的 settings.proxy_pool——否则用户在「注册设置」
		// 里存好代理池，导入时新候选仍然全是直连，得再手动改一遍。
		Proxies []string `json:"proxies"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var pool []string
	for _, p := range in.Proxies {
		if p = strings.TrimSpace(p); p != "" {
			pool = append(pool, p)
		}
	}
	// 显式传了代理：顺手存成池子，下次导入直接复用。
	// 没传：沿用已保存的池子。
	st := m.store.Settings()
	if len(pool) > 0 {
		if !equalStrings(st.ProxyPool, pool) {
			st.ProxyPool = pool
			if err := m.store.SaveSettings(st); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	} else {
		pool = st.ProxyPool
	}
	added, skipped := 0, []string{}
	for _, ln := range strings.Split(in.Lines, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		var email, box string
		switch {
		case strings.Contains(ln, "----"):
			p := strings.SplitN(ln, "----", 2)
			email, box = strings.TrimSpace(p[0]), strings.TrimSpace(p[1])
		case strings.Contains(ln, ","):
			p := strings.SplitN(ln, ",", 2)
			email, box = strings.TrimSpace(p[0]), strings.TrimSpace(p[1])
		default:
			skipped = append(skipped, ln)
			continue
		}
		cand := altapisignupstore.Candidate{
			Email: email, MailboxURL: box, Status: altapisignupstore.Queued,
		}
		if len(pool) > 0 {
			cand.ProxyURL = pool[added%len(pool)]
		}
		if _, err := m.store.Add(cand); err != nil {
			skipped = append(skipped, email)
			continue
		}
		added++
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"added": added, "skipped": skipped,
		// proxyPool 回给前端，让「导入结果」能说清这次按哪个池子分配的。
		"proxyPool": len(pool),
	})
}

// equalStrings 比较两个字符串切片是否逐位相等（导入时避免无谓写盘）。
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Server) apiALTAPICandidateUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in struct {
		Email      *string `json:"email"`
		MailboxURL *string `json:"mailboxUrl"`
		ProxyURL   *string `json:"proxyUrl"`
		Session    *string `json:"sessionCookie"`
		APIKey     *string `json:"apiKey"`
		AccountID  *string `json:"accountId"`
		TokenID    *string `json:"tokenId"`
		Status     *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, err := m.store.Update(id, func(c *altapisignupstore.Candidate) error {
		if in.Email != nil {
			c.Email = *in.Email
		}
		if in.MailboxURL != nil {
			c.MailboxURL = *in.MailboxURL
		}
		if in.ProxyURL != nil {
			c.ProxyURL = *in.ProxyURL
		}
		if in.Session != nil {
			c.SessionCookie = *in.Session
		}
		if in.APIKey != nil {
			c.APIKey = *in.APIKey
		}
		// account_id is new-api's user id. The console's /api/user/self
		// requires New-Api-User to equal it, so the quota read is impossible
		// without it — it is not decoration.
		if in.AccountID != nil {
			c.AccountID = strings.TrimSpace(*in.AccountID)
		}
		if in.TokenID != nil {
			c.TokenID = strings.TrimSpace(*in.TokenID)
		}
		if in.Status != nil {
			c.Status = altapisignupstore.Status(*in.Status)
		}
		return nil
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) apiALTAPICandidateDelete(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := m.store.Delete(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) apiALTAPICandidateRegister(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	go m.runBatch([]string{id})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": 1})
}

func (s *Server) apiALTAPIBulkRegister(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var in struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	ids := in.IDs
	if in.All || len(ids) == 0 {
		ids = nil
		for _, c := range m.store.List() {
			if c.Status == altapisignupstore.Queued || c.Status == altapisignupstore.Failed {
				ids = append(ids, c.ID)
			}
		}
	}
	go m.runBatch(ids)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": len(ids)})
}

func (s *Server) apiALTAPICandidateCancel(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	m.mu.Lock()
	cancel := m.running[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if _, err := m.store.Update(id, func(c *altapisignupstore.Candidate) error {
		c.Status = altapisignupstore.Cancelled
		c.Detail = "已取消"
		return nil
	}); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiALTAPICandidateImport puts a finished candidate into the pool as a
// config.Account. The API key is the runtime credential; the session cookie
// becomes ControlKey so the quota poller can read the real balance.
func (s *Server) apiALTAPICandidateImport(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id := r.PathValue("id")
	c, err := m.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	acct, err := s.importALTAPICandidate(c)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, acct)
}

func (s *Server) apiALTAPIImportAll(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	imported, failed := 0, []string{}
	for _, c := range m.store.List() {
		if c.APIKey == "" || c.ImportedAcctID != "" {
			continue
		}
		if _, err := s.importALTAPICandidate(c); err != nil {
			failed = append(failed, c.Email+": "+err.Error())
			continue
		}
		imported++
	}
	writeJSON(w, http.StatusOK, map[string]any{"imported": imported, "failed": failed})
}

// importALTAPICandidate puts one candidate into the pool.
//
// RuntimeKey is the API key (what /v1/* authenticates with). ControlKey holds
// the console session cookie, which is the **only** way the quota poller can
// read a real balance — the inference key cannot.
func (s *Server) importALTAPICandidate(c altapisignupstore.Candidate) (config.Account, error) {
	key := strings.TrimSpace(c.APIKey)
	if key == "" {
		return config.Account{}, fmt.Errorf("候选 %s 还没有 API Key", c.Email)
	}
	if c.ImportedAcctID != "" {
		return config.Account{}, fmt.Errorf("候选 %s 已导入账号 %s", c.Email, c.ImportedAcctID)
	}

	var acct config.Account
	_, err := s.cfg.Update(-1, func(cfg *config.Config) error {
		// One key = one wallet. A second row would be selectable and burn the
		// same money, so the key is the identity.
		for _, a := range cfg.Accounts {
			if a.Provider == config.ProviderALTAPI && a.RuntimeKey == key {
				return fmt.Errorf("该 API Key 已在账号 %s 里", a.Name)
			}
		}
		name := c.Username
		if name == "" {
			name = c.Email
			if i := strings.Index(name, "@"); i > 0 {
				name = name[:i]
			}
		}
		if name == "" {
			name = "altapi-" + c.ID
		}
		if len([]rune(name)) > 50 {
			name = string([]rune(name)[:50])
		}
		note := "altapi 注册机导入"
		if c.AccountID != "" {
			note += "；uid=" + c.AccountID
		}
		acct = config.Account{
			ID:         "alt-" + c.ID,
			Provider:   config.ProviderALTAPI,
			Name:       name,
			Email:      c.Email,
			UserID:     c.AccountID,
			Group:      config.GroupForProvider(config.ProviderALTAPI),
			Enabled:    true,
			RuntimeKey: key,
			ControlKey: newapiconsole.SessionValue(c.SessionCookie),
			ProxyURL:   c.ProxyURL,
			MaxConc:    8,
			Weight:     1,
			Note:       note,
		}
		cfg.Accounts = append(cfg.Accounts, acct)
		return nil
	})
	if err != nil {
		return config.Account{}, err
	}

	if m, merr := s.altapiMgr(); merr == nil {
		_, _ = m.store.Update(c.ID, func(cc *altapisignupstore.Candidate) error {
			cc.ImportedAcctID = acct.ID
			return nil
		})
		m.addLog("imported", c.Email, "已入池 "+acct.ID)
	}
	return acct, nil
}

func (s *Server) apiALTAPISettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, m.store.Settings())
}

func (s *Server) apiALTAPISaveSettings(w http.ResponseWriter, r *http.Request) {
	var in altapisignupstore.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := m.store.SaveSettings(in); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, m.store.Settings())
}

func (s *Server) apiALTAPILogs(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"logs": m.Logs()})
}

// apiALTAPIStatus is the pool overview: what the console's top cards show.
func (s *Server) apiALTAPIStatus(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sum := m.store.Summarize()

	// Pool side: accounts, balance read from snapshots, and the rule counts.
	cfg := s.cfg.Get()

	type poolRow struct {
		ID         string  `json:"id"`
		Name       string  `json:"name"`
		Email      string  `json:"email"`
		Group      string  `json:"group"`
		Enabled    bool    `json:"enabled"`
		// BalanceUSD is nil when the balance was not read: JSON null says
		// "unknown", where 0 would read as an empty account.
		BalanceUSD *float64 `json:"balanceUsd"`
		BalanceSrc string   `json:"balanceSource"`
		ReadHint   string  `json:"readHint,omitempty"`
		UsedUSD    float64 `json:"usedUsd"`
		Requests   int64   `json:"requests"`
		LastError  string  `json:"lastError,omitempty"`
	}
	var rows []poolRow
	var totalBalance float64
	withBalance, unknown := 0, 0
	for _, a := range cfg.Accounts {
		if a.Provider != config.ProviderALTAPI {
			continue
		}
		row := poolRow{ID: a.ID, Name: a.Name, Email: a.Email, Group: a.Group, Enabled: a.Enabled}
		if snap := s.quota.Get(a.ID); snap != nil {
			if d := snap.ALTAPIDiagnosis(time.Now()); d != nil {
				row.BalanceSrc = d.Source
				row.ReadHint = d.ReadHint
				row.UsedUSD = d.UsedUSD
				row.Requests = d.RequestCount
				if d.Source == "session" {
					bal := d.BalanceUSD
					row.BalanceUSD = &bal
					totalBalance += d.BalanceUSD
					withBalance++
				} else {
					unknown++
				}
			}
			row.LastError = snap.LastError
		} else {
			unknown++
		}
		rows = append(rows, row)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": sum,
		"signup":     map[string]any{"logs": len(m.Logs())},
		"pool": map[string]any{
			"accounts":       rows,
			"count":          len(rows),
			"totalBalanceUsd": round2(totalBalance),
			"withBalance":    withBalance,
			"balanceUnknown": unknown,
		},
		"rules": map[string]any{
			"error": len(cfg.ALTAPIErrorRules),
			"retry": len(cfg.ALTAPIRetryRules),
		},
		"routing": cfg.ALTAPI.EffectiveRouting().Strategy,
	})
}

func round2(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
