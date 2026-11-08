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
		Email:      body.Email,
		Password:   body.Password,
		MailboxURL: body.MailboxURL,
		Region:     body.Region,
		ProxyURL:   body.ProxyURL,
		Status:     mistralsignupstore.Queued,
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

		res, runErr := m.srv.runMistralWorker(ctx, c, body.SkipRegister)
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

func (s *Server) runMistralWorker(ctx context.Context, c mistralsignupstore.Candidate, skipRegister bool) (mistralWorkerResult, error) {
	var out mistralWorkerResult

	payload := map[string]any{
		"email":         c.Email,
		"mailbox_url":   c.MailboxURL,
		"password":      c.Password,
		"region":        c.Region,
		"skip_register": skipRegister,
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
	if cfg.ProxyUserTemplate != "" {
		env = append(env, "ZOOPROXY_USER_TPL="+cfg.ProxyUserTemplate)
	}
	if cfg.ProxyPassword != "" {
		env = append(env, "ZOOPROXY_PASSWORD="+cfg.ProxyPassword)
	}
	if len(cfg.ProxySIDs) > 0 {
		env = append(env, "ZOOPROXY_SIDS="+strings.Join(cfg.ProxySIDs, ","))
	}
	if cfg.ProxyHostPort != "" {
		env = append(env, "ZOOPROXY_HOST_PORT="+cfg.ProxyHostPort)
	}
	if cfg.Region != "" {
		env = append(env, "MISTRAL_REGION="+cfg.Region)
	}
	if cfg.MailboxProxy != "" {
		env = append(env, "MIDA_PROXY="+cfg.MailboxProxy)
	}
	cmd.Env = env
	var stdout, stderr bytes.Buffer
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
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":     cfg,
		"hasProxy":     cfg.HasProxy(),
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
	// 去掉空白 sid，避免出现空串代理
	sids := make([]string, 0, len(next.ProxySIDs))
	for _, sid := range next.ProxySIDs {
		sid = strings.TrimSpace(sid)
		if sid != "" {
			sids = append(sids, sid)
		}
	}
	next.ProxySIDs = sids
	next.ProxyUserTemplate = strings.TrimSpace(next.ProxyUserTemplate)
	next.ProxyHostPort = strings.TrimSpace(next.ProxyHostPort)
	next.Region = strings.TrimSpace(next.Region)

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
		"settings": cfg,
		"hasProxy": cfg.HasProxy(),
		"revision": s.cfg.Get().Revision,
	})
}

// apiMistralTestProxy checks one candidate proxy by asking a public IP service
// through it. It never runs a full registration.
func (s *Server) apiMistralTestProxy(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get().MistralSignup
	if !cfg.HasProxy() {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok": false, "error": "代理未配置（需要用户名模板、密码和至少一个 sid）",
		})
		return
	}
	sid := cfg.ProxySIDs[0]
	if len(cfg.ProxySIDs) > 1 {
		sid = cfg.ProxySIDs[randIndex(len(cfg.ProxySIDs))]
	}
	proxyURL := cfg.ProxyURLFor(sid)

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
