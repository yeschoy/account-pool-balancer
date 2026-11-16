package server

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/http"
	"sync"
	"time"

	"account-pool-balancer/internal/altapisignupstore"
	"account-pool-balancer/internal/config"
)

// ALTapi 自动补号。
//
// 两种触发方式，各自独立开关（都关时这条链路完全不跑）：
//
//   1. 阈值补号：号池里「可用账号」少于 MinAvailable 时，补到阈值为止。
//      可用 = enabled 且没被池子隔离/熔断的 altapi 账号。
//   2. 频率注册：每隔 RandomMinSec~RandomMaxSec 秒随机注册一个号。
//      随机而不是固定间隔，避免整点规律暴露「这是脚本」。
//
// 补号来源优先用「已有 Key 但没入池」的候选（免费，不用重新注册），
// 不够才发起新的注册批次。注册走已有的 runBatch，所以节奏控制、
// 代理轮换、自动入池全部复用，不另写一套。

// altAutoSettings 复用存储层的类型：配置跟注册机设置存在同一个 JSON 里。
type altAutoSettings = altapisignupstore.AutoSettings

// altAutoStatus 是自动补号的可观测状态，给前端展示。
type altAutoStatus struct {
	Enabled     bool      `json:"enabled"`
	Running     bool      `json:"running"`
	Available   int       `json:"available"`
	PoolTotal   int       `json:"poolTotal"`
	Importable  int       `json:"importable"` // 有 Key 未入池的候选数
	Queued      int       `json:"queued"`     // 待注册候选数
	NextRunAt   time.Time `json:"nextRunAt"`
	NextReason  string    `json:"nextReason"`
	LastCheck   time.Time `json:"lastCheck"`
	LastAction  string    `json:"lastAction"`
	LastError   string    `json:"lastError"`
	Registering bool      `json:"registering"`
	RoundCount  int       `json:"roundCount"` // 本次进程内触发次数
}

// altAutoManager 持有自动补号的运行时状态。
//
// 与 altapiManager 分开：后者管「一批注册怎么跑完」，这里管「什么时候该发起」。
type altAutoManager struct {
	mu       sync.Mutex
	settings altAutoSettings
	status   altAutoStatus
	// notBefore 是频率注册的下一个允许时间；每次触发后重新随机。
	notBefore time.Time
	trigger   chan struct{}
}

// validAltAutoSettings 校验自动补号配置。
//
// 两种模式**互斥**：阈值补号是"缺多少补多少"，频率注册是"按固定节奏持续补"。
// 同时开启会让池子持续膨胀——阈值刚补满，频率又接着注册，没有停止条件。
// 所以这里强制二选一，而不是允许并行。
func validAltAutoSettings(v altAutoSettings) error {
	if v.Enabled && v.ThresholdEnabled && v.FrequencyEnabled {
		return errors.New("两种补号模式只能选一个：按阈值补号 或 按频率注册")
	}
	if v.MinAvailable < 0 || v.MinAvailable > 10000 {
		return errors.New("最小可用数需在 0..10000")
	}
	if v.MaxPerRound < 1 || v.MaxPerRound > 50 {
		return errors.New("单轮上限需在 1..50")
	}
	if v.RandomMinSec < 60 || v.RandomMaxSec > 86400 {
		return errors.New("随机区间需在 60..86400 秒")
	}
	if v.RandomMaxSec < v.RandomMinSec {
		return errors.New("随机上限不能小于下限")
	}
	return nil
}

// defaultAltAutoSettings 给一套保守默认：关着，阈值 5，频率 10–20 分钟。
// 默认关闭是刻意的——自动注册会真的消耗邮箱和 IP 信誉，必须人工打开。
func defaultAltAutoSettings() altAutoSettings {
	return altAutoSettings{
		Enabled:          false,
		ThresholdEnabled: true,
		MinAvailable:     5,
		FrequencyEnabled: false,
		RandomMinSec:     600,
		RandomMaxSec:     1200,
		MaxPerRound:      5,
	}
}

// altAutoSettingsFrom 从注册机设置里取自动补号段。
func altAutoSettingsFrom(st altapisignupstore.Settings) altAutoSettings {
	var a altAutoSettings
	if st.Auto == nil {
		return defaultAltAutoSettings()
	}
	a = *st.Auto
	if err := validAltAutoSettings(a); err != nil {
		return defaultAltAutoSettings()
	}
	return a
}

// altAutoCtx 是自动补号用的服务级 context。必须在 New 期间初始化，
// 不能挂在 http 请求上——handler 返回就取消了。
func (s *Server) altAutoCtx() context.Context {
	s.altAutoMu.Lock()
	defer s.altAutoMu.Unlock()
	if s.altAutoRoot == nil {
		s.altAutoRoot, s.altAutoCancel = context.WithCancel(context.Background())
	}
	return s.altAutoRoot
}

// stopAltAuto 停止自动补号循环，供关闭流程调用。
func (s *Server) stopAltAuto() {
	s.altAutoMu.Lock()
	defer s.altAutoMu.Unlock()
	if s.altAutoCancel != nil {
		s.altAutoCancel()
		s.altAutoCancel = nil
		s.altAutoRoot = nil
	}
}

// initAltAuto 建 manager 并从存储里读回设置。
//
// 没有存储可用时（例如 data 目录还没建好）不阻断启动：自动补号是可选功能，
// 让它以默认（关闭）状态空转，比整个服务起不来好。
func (s *Server) initAltAuto() {
	a := &altAutoManager{
		settings: defaultAltAutoSettings(),
		trigger:  make(chan struct{}, 1),
	}
	if m, err := s.altapiMgr(); err == nil {
		a.settings = altAutoSettingsFrom(m.store.Settings())
	}
	a.status = altAutoStatus{LastAction: "等待首次检查"}
	s.altAuto = a
	go s.altAutoLoop(s.altAutoCtx())
}

// altAutoTrigger 立即触发一次判定（用户改完设置或点「立即检查」时用）。
func (s *Server) altAutoTrigger() {
	if s.altAuto == nil {
		return
	}
	select {
	case s.altAuto.trigger <- struct{}{}:
	default: // 已有待处理触发，合并
	}
}

// altAutoLoop 是自动补号的主循环。挂在服务级 context 上（不能继承
// r.Context()，否则 handler 返回即取消）。
func (s *Server) altAutoLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.altAutoCheck(ctx)
		case <-s.altAuto.trigger:
			s.altAutoCheck(ctx)
		}
	}
}

// altAutoCheck 是一次判定：该不该补、补多少。
func (s *Server) altAutoCheck(ctx context.Context) {
	a := s.altAuto
	a.mu.Lock()
	st := a.settings
	now := time.Now()
	a.status.LastCheck = now
	if !st.Enabled {
		// 关闭时仍然统计池子现状：用户要靠这个数判断「该不该开」。
		// 之前这里直接 return，导致 total/available 永远是 0，看起来像功能坏了。
		a.status.Running = false
		a.mu.Unlock()
		cfg := s.cfg.Get()
		available, total := s.altAutoAvailability(cfg)
		importable, queued := s.altAutoCountCandidates()
		a.mu.Lock()
		a.status.Available = available
		a.status.PoolTotal = total
		a.status.Importable = importable
		a.status.Queued = queued
		if a.status.LastAction == "" || a.status.LastAction == "等待首次检查" {
			a.status.LastAction = "未启用"
		}
		a.mu.Unlock()
		return
	}
	a.status.Running = true
	// 正在注册中就不要再发起，避免两批并发。
	if s.altapiBusy() {
		a.status.Registering = true
		a.mu.Unlock()
		return
	}
	a.status.Registering = false
	a.mu.Unlock()

	cfg := s.cfg.Get()
	available, total := s.altAutoAvailability(cfg)

	if _, err := s.altapiMgr(); err != nil {
		a.mu.Lock()
		a.status.LastError = err.Error()
		a.mu.Unlock()
		return
	}
	importable, queued := s.altAutoCountCandidates()

	a.mu.Lock()
	a.status.Available = available
	a.status.PoolTotal = total
	a.status.Importable = importable
	a.status.Queued = queued
	a.status.LastError = ""
	a.mu.Unlock()

	// 两种模式互斥（validAltAutoSettings 已保证），所以下面的 return 是安全的：
	// 阈值补号一触发，这一轮就不再走频率分支。

	// 一、阈值补号：可用不够就补到阈值。
	if st.ThresholdEnabled && available+importable < st.MinAvailable {
		want := st.MinAvailable - available - importable
		if want > st.MaxPerRound {
			want = st.MaxPerRound
		}
		if want > 0 {
			s.altAutoReplenish(ctx, want, importable, "低于阈值")
			return
		}
	}

	// 二、频率注册：到点了就注册一个。
	if st.FrequencyEnabled && !now.Before(a.notBefore) {
		s.altAutoRegister(ctx, 1, "按频率")
		a.mu.Lock()
		a.scheduleNextLocked()
		a.mu.Unlock()
		return
	}

	// 没触发也要更新「下次什么时候」给前端看。
	a.mu.Lock()
	if st.FrequencyEnabled && a.notBefore.IsZero() {
		a.scheduleNextLocked()
	}
	a.mu.Unlock()
}

// altAutoCountCandidates 数「可导入」与「可注册」的候选。
//
// 可导入 = 已有 API Key 且未入池（零成本补号来源）。
// 可注册 = 有接码地址且状态可重试。
func (s *Server) altAutoCountCandidates() (importable, queued int) {
	m, err := s.altapiMgr()
	if err != nil {
		return 0, 0
	}
	for _, c := range m.store.List() {
		if c.APIKey != "" && c.ImportedAcctID == "" {
			importable++
		}
		if c.MailboxURL == "" || c.ImportedAcctID != "" {
			continue
		}
		switch c.Status {
		case altapisignupstore.Queued, altapisignupstore.Failed, altapisignupstore.Cancelled:
			queued++
		}
	}
	return importable, queued
}

// scheduleNextLocked 重新随机下一次频率注册的时间。调用方持锁。
func (a *altAutoManager) scheduleNextLocked() {
	lo, hi := a.settings.RandomMinSec, a.settings.RandomMaxSec
	if hi < lo {
		hi = lo
	}
	span := hi - lo
	sec := lo
	if span > 0 {
		sec += rand.Intn(span + 1)
	}
	a.notBefore = time.Now().Add(time.Duration(sec) * time.Second)
	a.status.NextRunAt = a.notBefore
	a.status.NextReason = "频率注册"
}

// altAutoAvailability 数号池里可用与总数。
//
// 可用 = enabled，且池子没有把它隔离/熔断（禁用、登录失效、硬熔断都算不可用）。
func (s *Server) altAutoAvailability(cfg *config.Config) (available, total int) {
	views := s.pool.Views(cfg)
	for _, acc := range cfg.Accounts {
		if acc.Provider != config.ProviderALTAPI {
			continue
		}
		total++
		if !acc.Enabled {
			continue
		}
		if v, ok := views[acc.ID]; ok && v.State != nil && v.State.HardQuarantined {
			continue
		}
		available++
	}
	return available, total
}

// altAutoReplenish 补 want 个：先把已有 Key 的候选导入（不花注册成本），
// 不够再发起注册。
func (s *Server) altAutoReplenish(ctx context.Context, want int, importable int, reason string) {
	m, err := s.altapiMgr()
	if err != nil {
		return
	}
	need := want
	if importable > 0 {
		done := 0
		for _, c := range m.store.List() {
			if done >= need || c.APIKey == "" || c.ImportedAcctID != "" {
				continue
			}
			if _, err := s.importALTAPICandidate(c); err != nil {
				continue
			}
			done++
		}
		if done > 0 {
			m.addLog("auto", "", reason+"：自动导入 "+itoa(done)+" 个已建 Key 候选")
			s.altAuto.note("自动导入 " + itoa(done) + " 个")
			need -= done
		}
	}
	if need > 0 {
		s.altAutoRegister(ctx, need, reason)
	}
}

// altAutoRegister 发起一批注册。只挑「有接码地址且可重试」的候选。
func (s *Server) altAutoRegister(ctx context.Context, n int, reason string) {
	m, err := s.altapiMgr()
	if err != nil {
		return
	}
	var ids []string
	for _, c := range m.store.List() {
		if len(ids) >= n {
			break
		}
		if c.MailboxURL == "" || c.ImportedAcctID != "" {
			continue
		}
		if c.Status == altapisignupstore.Queued || c.Status == altapisignupstore.Failed || c.Status == altapisignupstore.Cancelled {
			ids = append(ids, c.ID)
		}
	}
	if len(ids) == 0 {
		// 没有可注册的候选：不是故障，是缺库存。如实记下来，别让循环空转报错。
		m.addLog("auto", "", reason+"：没有可注册的候选（缺接码地址或都已入池）")
		s.altAuto.note("无候选可注册")
		return
	}
	m.addLog("auto", "", reason+"：自动发起注册 "+itoa(len(ids))+" 个")
	s.altAuto.note("发起注册 " + itoa(len(ids)) + " 个")
	// runBatch 内部有 seq 串行保护，且挂在服务级 context 上。
	// 自动发起的批次强制入池（originAuto），不受 import_to_pool 开关约束。
	go m.runBatch(ids, originAuto)
}

// note 记录最近一次动作，供前端展示。
func (a *altAutoManager) note(msg string) {
	a.mu.Lock()
	a.status.LastAction = msg
	a.status.RoundCount++
	a.mu.Unlock()
}

// altapiBusy 判断当前是否已有注册批次在跑。
func (s *Server) altapiBusy() bool {
	m, err := s.altapiMgr()
	if err != nil {
		return false
	}
	select {
	case m.seq <- struct{}{}:
		<-m.seq
		return false
	default:
		return true
	}
}

// itoa 避免为了一处转换引入 strconv 到本文件。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// altAutoStatusView 返回状态快照给前端。
func (a *altAutoManager) view() altAutoStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.status
	v.Enabled = a.settings.Enabled
	return v
}

// ---------------------------------------------------------------- API

// apiALTAPIAutoSettings 读写自动补号配置。
func (s *Server) apiALTAPIAutoSettings(w http.ResponseWriter, r *http.Request) {
	m, err := s.altapiMgr()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"settings": altAutoSettingsFrom(m.store.Settings()),
			"status":   s.altAutoView(),
		})
	case http.MethodPut:
		var in altAutoSettings
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := validAltAutoSettings(in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		st := m.store.Settings()
		st.Auto = &in
		if err := m.store.SaveSettings(st); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		// 立刻生效：更新内存设置并重置频率计时，用户不用等下一轮 tick。
		if s.altAuto != nil {
			s.altAuto.mu.Lock()
			s.altAuto.settings = in
			s.altAuto.notBefore = time.Time{}
			s.altAuto.status = altAutoStatus{LastAction: "设置已更新"}
			s.altAuto.mu.Unlock()
			s.altAutoTrigger()
		}
		m.addLog("auto", "", "自动补号设置已更新（总开关="+boolText(in.Enabled)+"）")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": in})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// apiALTAPIAutoStatus 只读状态，供页面轮询。
func (s *Server) apiALTAPIAutoStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": s.altAutoView()})
}

// apiALTAPIAutoRun 立即执行一次判定，方便验证配置而不必等 tick。
func (s *Server) apiALTAPIAutoRun(w http.ResponseWriter, r *http.Request) {
	s.altAutoTrigger()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) altAutoView() altAutoStatus {
	if s.altAuto == nil {
		return altAutoStatus{LastAction: "未初始化"}
	}
	return s.altAuto.view()
}

func boolText(b bool) string {
	if b {
		return "开"
	}
	return "关"
}
