package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/fsutil"
	"account-pool-balancer/internal/perchactivate"
	"account-pool-balancer/internal/perchimport"
	"account-pool-balancer/internal/perchsignupstore"
)

type perchAutoSettings struct {
	Enabled      bool   `json:"enabled"`
	IntervalSec  int    `json:"intervalSec"`
	MinAvailable int    `json:"minAvailable"`
	Group        string `json:"group"`
}
type perchAutoStatus struct {
	Available int       `json:"available"`
	Running   bool      `json:"running"`
	LastCheck time.Time `json:"lastCheck"`
	LastError string    `json:"lastError"`
}
type perchAutoManager struct {
	mu             sync.Mutex
	settings       perchAutoSettings
	status         perchAutoStatus
	store          *perchsignupstore.Store
	trigger        chan struct{}
	active         bool
	workingID      string
	pendingID      string
	pendingSession []byte
	notBefore      time.Time
}

func validAutoSettings(v perchAutoSettings) bool {
	return v.IntervalSec >= 60 && v.IntervalSec <= 86400 && v.MinAvailable >= 1 && v.MinAvailable <= 10000 && strings.TrimSpace(v.Group) == v.Group && len(v.Group) > 0 && len(v.Group) <= 128 && !strings.ContainsAny(v.Group, "\r\n\x00")
}
func (s *Server) initPerchAuto() error {
	settings := perchAutoSettings{IntervalSec: 60, MinAvailable: 10, Group: "perch"}
	err := fsutil.ReadJSON(filepath.Join(s.dataDir, "perch-autosignup.json"), &settings)
	if err != nil && !errors.Is(err, fsutil.ErrNotExist) {
		return errors.New("设置读取失败")
	}
	if !validAutoSettings(settings) {
		return errors.New("设置无效")
	}
	store, err := perchsignupstore.Open(filepath.Join(s.dataDir, "perch-signup.enc"), s.secret)
	if err != nil {
		return errors.New("候选库存读取失败")
	}
	s.perchAuto = &perchAutoManager{settings: settings, notBefore: time.Now().Add(time.Duration(settings.IntervalSec) * time.Second), store: store, trigger: make(chan struct{}, 1)}
	return nil
}
func (s *Server) apiPerchAuto(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a := s.perchAuto
	a.mu.Lock()
	settings, status, pendingID := a.settings, a.status, a.pendingID
	a.mu.Unlock()
	type candidate struct {
		ID            string                  `json:"id"`
		Login         string                  `json:"login"`
		Status        perchsignupstore.Status `json:"status"`
		Proxy         string                  `json:"proxy"`
		Detail        string                  `json:"detail"`
		CanRetrySave  bool                    `json:"canRetrySave"`
		CanRevalidate bool                    `json:"canRevalidate"`
	}
	list := make([]candidate, 0)
	for _, x := range a.store.List() {
		item := candidate{ID: x.ID, Login: x.Login, Status: x.Status, Proxy: x.Proxy, Detail: autoCandidateDetail(x.Status), CanRevalidate: x.Status == perchsignupstore.Uncertain && x.HasSession && x.Proxy != "" && x.Proxy != "直连"}
		if x.ID == pendingID {
			item.Status = "save-failed"
			item.Detail = "会话仅存于内存，重启将丢失，请立即重试保存"
			item.CanRetrySave = true
		}
		list = append(list, item)
	}
	writeJSON(w, 200, map[string]any{"settings": settings, "candidates": list, "proxies": a.store.ListProxies(), "status": status})
}
func autoCandidateDetail(st perchsignupstore.Status) string {
	switch st {
	case perchsignupstore.Uncertain:
		return "执行结果不确定，请人工核查"
	case perchsignupstore.Failed:
		return "注册失败，请人工核查"
	case perchsignupstore.Ready:
		return "已激活，等待人工核查入池"
	}
	return ""
}
func (s *Server) apiPerchAutoSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var v perchAutoSettings
	if readBody(r, &v) != nil {
		apiError(w, 400, errors.New("请求格式无效"))
		return
	}
	v.Group = strings.TrimSpace(v.Group)
	if !validAutoSettings(v) {
		apiError(w, 400, errors.New("间隔至少 60 秒、目标至少 1 且分组不能为空"))
		return
	}
	a := s.perchAuto
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := fsutil.WriteJSONAtomic(filepath.Join(s.dataDir, "perch-autosignup.json"), v); err != nil {
		apiError(w, 500, errors.New("保存设置失败"))
		return
	}
	a.settings = v
	writeJSON(w, 200, v)
}
func (s *Server) apiPerchAutoCandidates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Accounts []registrationInput `json:"accounts"`
	}
	if readBody(r, &in) != nil || len(in.Accounts) == 0 || len(in.Accounts) > 100 {
		apiError(w, 400, errors.New("请选择 1-100 个有效账号"))
		return
	}
	cs := make([]perchsignupstore.Candidate, 0, len(in.Accounts))
	seen := map[string]bool{}
	for _, x := range in.Accounts {
		x.Login = strings.TrimSpace(x.Login)
		key := strings.ToLower(x.Login)
		proxy, err := normalizeRegistrationProxy(x.ProxyURL)
		if err != nil || key == "" || x.Password == "" || x.TOTPSecret == "" || x.MailboxURL == "" || seen[key] {
			apiError(w, 400, errors.New("账号输入无效或重复"))
			return
		}
		seen[key] = true
		cs = append(cs, perchsignupstore.Candidate{Login: x.Login, Password: x.Password, TOTPSecret: x.TOTPSecret, MailboxURL: x.MailboxURL, ProxyURL: proxy})
	}
	out, err := s.perchAuto.store.ImportWithProxies(cs)
	if err != nil {
		apiError(w, 500, errors.New("保存候选账号失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"candidates": out, "added": len(out), "duplicate": len(cs) - len(out)})
}
func (s *Server) apiPerchAutoProxies(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Proxies []struct {
			URL string `json:"url"`
		} `json:"proxies"`
	}
	if readBody(r, &in) != nil || len(in.Proxies) == 0 || len(in.Proxies) > 100 {
		apiError(w, 400, errors.New("请选择 1-100 个有效代理"))
		return
	}
	urls := make([]string, 0, len(in.Proxies))
	for _, p := range in.Proxies {
		v, err := normalizeRegistrationProxy(p.URL)
		if err != nil || v == "" {
			apiError(w, 400, errors.New("代理地址无效"))
			return
		}
		urls = append(urls, v)
	}
	out, err := s.perchAuto.store.ImportProxies(urls)
	if err != nil {
		apiError(w, 500, errors.New("保存代理失败"))
		return
	}
	writeJSON(w, 200, map[string]any{"proxies": out, "added": len(out), "duplicate": len(urls) - len(out)})
}
func (s *Server) apiPerchAutoDeleteProxy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.perchAuto.store.DeleteProxy(r.PathValue("id")) != nil {
		apiError(w, 409, errors.New("无法删除代理"))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}
func (s *Server) apiPerchAutoDeleteCandidate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a := s.perchAuto
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workingID == r.PathValue("id") || a.pendingID == r.PathValue("id") || a.store.Delete(r.PathValue("id")) != nil {
		apiError(w, 409, errors.New("无法删除候选账号，请确认账号存在且未运行"))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "deleted"})
}
func (s *Server) apiPerchAutoRetrySave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a := s.perchAuto
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingID == "" || a.pendingID != r.PathValue("id") || a.workingID != "" {
		apiError(w, 409, errors.New("没有可重试保存的会话"))
		return
	}
	if a.store.Update(a.pendingID, perchsignupstore.Uncertain, a.pendingSession, "") != nil {
		apiError(w, 500, errors.New("会话保存失败，重启将丢失"))
		return
	}
	a.pendingID = ""
	a.pendingSession = nil
	a.status.LastError = "会话已保存，注册结果需人工核查"
	writeJSON(w, 200, map[string]string{"status": "uncertain"})
}
func (s *Server) apiPerchAutoActivateCandidate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if readBody(r, &in) != nil || !in.Confirm {
		apiError(w, 400, errors.New("必须确认重新激活可能发送首条消息"))
		return
	}
	m := &s.perchRegistrations
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		apiError(w, 409, errors.New("已有注册任务运行"))
		return
	}
	m.running = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
	a := s.perchAuto
	a.mu.Lock()
	if a.workingID != "" || a.pendingID != "" {
		a.mu.Unlock()
		apiError(w, 409, errors.New("候选账号正在处理"))
		return
	}
	rec, err := a.store.Get(r.PathValue("id"))
	if err != nil || rec.Status != perchsignupstore.Uncertain || rec.Candidate.ProxyURL == "" || !validAutoSession(rec.Session) {
		a.mu.Unlock()
		apiError(w, 409, errors.New("会话不存在、无效或已过期"))
		return
	}
	if _, err = a.store.ResumeUncertain(rec.ID); err != nil {
		a.mu.Unlock()
		apiError(w, 500, errors.New("保存候选状态失败"))
		return
	}
	a.workingID = rec.ID
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.workingID = ""; a.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	if s.activateAutoSession(ctx, rec.Candidate.ProxyURL, rec.Session) != nil || ctx.Err() != nil {
		if a.store.Update(rec.ID, perchsignupstore.Uncertain, nil, "") != nil {
			apiError(w, 500, errors.New("保存候选状态失败，请人工核查"))
			return
		}
		apiError(w, 409, errors.New("激活结果不确定，请人工核查"))
		return
	}
	if a.store.Update(rec.ID, perchsignupstore.Ready, nil, "") != nil {
		apiError(w, 500, errors.New("保存激活状态失败，请人工核查"))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}
func (s *Server) apiPerchAutoImportCandidate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var in struct {
		Group string `json:"group"`
	}
	if readBody(r, &in) != nil {
		apiError(w, 400, errors.New("请求格式无效"))
		return
	}
	in.Group = strings.TrimSpace(in.Group)
	if !validAutoSettings(perchAutoSettings{IntervalSec: 60, MinAvailable: 1, Group: in.Group}) {
		apiError(w, 400, errors.New("分组无效"))
		return
	}
	a := s.perchAuto
	a.mu.Lock()
	if a.workingID != "" {
		a.mu.Unlock()
		apiError(w, 409, errors.New("候选账号正在处理"))
		return
	}
	rec, err := a.store.Get(r.PathValue("id"))
	if err != nil || rec.Status != perchsignupstore.Ready {
		a.mu.Unlock()
		apiError(w, 409, errors.New("仅已激活候选账号可以入池"))
		return
	}
	a.workingID = rec.ID
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.workingID = ""; a.mu.Unlock() }()
	if err := s.importReadyPerchCandidate(rec, in.Group); err != nil {
		apiError(w, 409, err)
		return
	}
	writeJSON(w, 200, map[string]string{"status": "completed"})
}
func (s *Server) apiPerchAutoRun(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	a := s.perchAuto
	a.mu.Lock()
	active := a.active
	enabled := a.settings.Enabled
	nextCheck := a.status.LastCheck.Add(time.Duration(a.settings.IntervalSec) * time.Second)
	tooSoon := time.Now().Before(a.notBefore) || (!a.status.LastCheck.IsZero() && time.Now().Before(nextCheck))
	a.mu.Unlock()
	if !enabled {
		apiError(w, 409, errors.New("请先启用自动补号"))
		return
	}
	if !active {
		apiError(w, 503, errors.New("后台调度未运行"))
		return
	}
	if tooSoon {
		apiError(w, http.StatusTooManyRequests, errors.New("尚未到下一次检查时间"))
		return
	}
	select {
	case a.trigger <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "scheduled"})
}
func (s *Server) runPerchAuto(ctx context.Context) {
	a := s.perchAuto
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.active {
		a.mu.Unlock()
		return
	}
	a.active = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.active = false; a.mu.Unlock() }()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.perchAutoCheck(ctx)
		case <-a.trigger:
			s.perchAutoCheck(ctx)
		}
	}
}
func (s *Server) perchAutoCheck(ctx context.Context) {
	a := s.perchAuto
	a.mu.Lock()
	settings := a.settings
	now := time.Now()
	if !settings.Enabled || now.Before(a.notBefore) || (!a.status.LastCheck.IsZero() && now.Sub(a.status.LastCheck) < time.Duration(settings.IntervalSec)*time.Second) {
		a.mu.Unlock()
		return
	}
	a.status.LastCheck = now
	if a.pendingID != "" {
		a.mu.Unlock()
		return
	}
	a.status.LastError = ""
	a.mu.Unlock()
	available := 0
	blocked := false
	cfg := s.cfg.Get()
	poolViews := s.pool.Views(cfg)
	for _, v := range s.perchAccountViews(cfg) {
		if v.Group != settings.Group {
			continue
		}
		if !v.Enabled || v.Status == "disabled" || v.Status == "login-expired" {
			continue
		}
		if pv, ok := poolViews[v.ID]; ok && pv.State != nil && pv.State.HardQuarantined && pv.State.RuleID == "perch-banned" {
			continue
		}
		if v.Status == "normal" {
			available++
		}
		if v.QuotaError != "" {
			blocked = true
		}
		switch v.Status {
		case "service-down", "refresh-failed", "rate-limited", "pending", "restricted", "tier-pending":
			blocked = true
		}
	}
	for _, x := range a.store.List() {
		if x.Status == perchsignupstore.Failed || x.Status == perchsignupstore.Uncertain || x.Status == perchsignupstore.Ready || x.Status == perchsignupstore.Running {
			blocked = true
			break
		}
	}
	a.mu.Lock()
	a.status.Available = available
	if blocked {
		a.status.LastError = "账号状态不确定，请人工核查后再补号"
	}
	a.mu.Unlock()
	if blocked || available >= settings.MinAvailable || ctx.Err() != nil {
		return
	}
	m := &s.perchRegistrations
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
	a.mu.Lock()
	if a.workingID != "" || a.pendingID != "" {
		a.mu.Unlock()
		return
	}
	rec, err := a.store.Claim()
	if err != nil {
		a.mu.Unlock()
		if errors.Is(err, perchsignupstore.ErrNoProxy) {
			s.perchAutoError("候选账号未绑定代理，代理库存为空")
		} else if !errors.Is(err, perchsignupstore.ErrNoQueued) {
			s.perchAutoError("领取候选账号失败")
		}
		return
	}
	a.workingID = rec.ID
	a.status.Running = true
	a.mu.Unlock()
	defer func() { a.mu.Lock(); a.workingID = ""; a.status.Running = false; a.mu.Unlock() }()
	s.perchAutoRegister(ctx, rec, settings.Group)
}
func (s *Server) perchAutoError(msg string) {
	a := s.perchAuto
	a.mu.Lock()
	a.status.LastError = msg
	a.mu.Unlock()
}
func (s *Server) perchAutoRegister(parent context.Context, rec perchsignupstore.Record, group string) {
	a := s.perchAuto
	m := &s.perchRegistrations
	update := func(st perchsignupstore.Status, doc []byte, id string) bool {
		if a.store.Update(rec.ID, st, doc, id) != nil {
			s.perchAutoError("保存候选状态失败，请人工核查")
			return false
		}
		return true
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	c := rec.Candidate
	input := registrationInput{Login: c.Login, Password: c.Password, TOTPSecret: c.TOTPSecret, MailboxURL: c.MailboxURL, ProxyURL: c.ProxyURL}
	worker := m.worker
	if worker == nil {
		worker = runPerchWorker
	}
	started := time.Now()
	doc, err := worker(ctx, input)
	s.logPerchDiagnostic("", "register", autoPerchProxySource(c.ProxyURL), "", started, err)
	if err != nil || ctx.Err() != nil {
		update(perchsignupstore.Uncertain, nil, "")
		s.perchAutoError("注册结果不确定，请人工核查")
		return
	}
	if !update(perchsignupstore.Running, doc, "") {
		a.mu.Lock()
		a.pendingID = rec.ID
		a.pendingSession = append([]byte(nil), doc...)
		a.status.LastError = "会话保存失败，当前仅保存在内存，重启将丢失；请立即重试保存"
		a.mu.Unlock()
		return
	}
	if !validAutoSession(doc) {
		update(perchsignupstore.Uncertain, nil, "")
		s.perchAutoError("注册会话无效，请人工核查")
		return
	}
	if s.activateAutoSession(ctx, c.ProxyURL, doc) != nil || ctx.Err() != nil {
		update(perchsignupstore.Uncertain, nil, "")
		s.perchAutoError("Starter 激活结果不确定，请人工核查")
		return
	}
	if !update(perchsignupstore.Ready, nil, "") {
		return
	}
	if ctx.Err() != nil {
		s.perchAutoError("已激活账号需要人工核查")
		return
	}
	rec.Session = doc
	if err := s.importReadyPerchCandidate(rec, group); err != nil {
		s.perchAutoError(err.Error())
	}
}

func validAutoSession(doc []byte) bool {
	parsed, err := perchimport.ParseJSON(doc)
	var envelope struct {
		Session json.RawMessage `json:"supabase_session"`
		Auth    struct {
			URL string `json:"supabase_url"`
			Key string `json:"supabase_anon_key"`
		} `json:"auth_context"`
	}
	var session struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresAt    int64  `json:"expires_at"`
		User         struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	return err == nil && parsed.UserID != "" && parsed.AccessToken != "" && parsed.RefreshToken != "" && parsed.ExpiresAt > time.Now().Unix() && json.Unmarshal(doc, &envelope) == nil && json.Unmarshal(envelope.Session, &session) == nil && session.AccessToken == parsed.AccessToken && session.RefreshToken == parsed.RefreshToken && session.ExpiresAt == parsed.ExpiresAt && session.User.ID == parsed.UserID
}
func autoPerchProxySource(proxy string) string {
	if proxy != "" {
		return "explicit_proxy"
	}
	return "direct"
}

func (s *Server) activateAutoSession(ctx context.Context, proxy string, doc []byte) (resultErr error) {
	started := time.Now()
	defer func() {
		s.logPerchDiagnostic("", "activate", autoPerchProxySource(proxy), "", started, resultErr)
	}()
	if s.perchRegistrations.activate != nil {
		return s.perchRegistrations.activate(ctx, proxy, doc)
	}
	var envelope struct {
		Session json.RawMessage `json:"supabase_session"`
		Auth    struct {
			URL string `json:"supabase_url"`
			Key string `json:"supabase_anon_key"`
		} `json:"auth_context"`
	}
	if json.Unmarshal(doc, &envelope) != nil {
		return errors.New("无效会话")
	}
	transport := &http.Transport{}
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return err
		}
		transport.Proxy = http.ProxyURL(u)
	}
	defer transport.CloseIdleConnections()
	_, err := perchactivate.Activate(ctx, perchactivate.Config{HTTPClient: &http.Client{Transport: transport}, SupabaseURL: envelope.Auth.URL, SupabaseKey: envelope.Auth.Key}, envelope.Session)
	return err
}

func (s *Server) importReadyPerchCandidate(rec perchsignupstore.Record, group string) error {
	parsed, err := perchimport.ParseJSON(rec.Session)
	if err != nil || parsed.UserID == "" || parsed.AccessToken == "" || parsed.RefreshToken == "" || parsed.ExpiresAt <= time.Now().Unix() {
		return errors.New("会话无效或已过期，请人工核查")
	}
	c := rec.Candidate
	result, err := s.importPerch(perchimport.Result{ParsedDocs: []perchimport.Parsed{parsed}, Sources: []string{c.Login}}, group, func(plans []perchImportPlan) error {
		if len(plans) != 1 || (plans[0].Account != nil && (plans[0].Account.Group != group || plans[0].Account.ProxyURL != c.ProxyURL)) {
			return errors.New("账号不匹配")
		}
		plans[0].ProxyURL = c.ProxyURL
		return nil
	})
	if err != nil {
		return errors.New("账号身份、分组或代理不匹配")
	}
	if len(result.Outcomes) != 1 {
		return errors.New("入池失败，请人工核查")
	}
	out := result.Outcomes[0]
	if out.Status != "created" && out.Status != "updated" && out.Status != "duplicate" {
		return errors.New("入池失败，请人工核查")
	}
	acct, ok := s.cfg.Get().Account(out.AccountID)
	if !ok || acct.Provider != config.ProviderPerch || acct.PerchUserID != parsed.UserID || acct.Group != group {
		return errors.New("入池确认失败，请人工核查")
	}
	if s.perchAuto.store.Update(rec.ID, perchsignupstore.Completed, nil, out.AccountID) != nil {
		return errors.New("保存候选状态失败，请人工核查")
	}
	return nil
}
