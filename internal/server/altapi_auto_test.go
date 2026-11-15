package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/altapisignupstore"
	"account-pool-balancer/internal/config"
)

func altAutoTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// 默认必须是关闭的：自动注册真实消耗邮箱与出口 IP 信誉，不能默认替用户打开。
func TestALTAAutoDefaultsOff(t *testing.T) {
	d := defaultAltAutoSettings()
	if d.Enabled {
		t.Error("自动补号默认必须关闭")
	}
	if d.MinAvailable != 5 || d.MaxPerRound != 5 {
		t.Errorf("默认阈值/上限异常: %+v", d)
	}
	if d.RandomMinSec < 60 || d.RandomMaxSec < d.RandomMinSec {
		t.Errorf("默认随机区间异常: %+v", d)
	}
}

func TestALTAAutoSettingsValidation(t *testing.T) {
	ok := defaultAltAutoSettings()
	ok.Enabled = true
	if err := validAltAutoSettings(ok); err != nil {
		t.Fatalf("默认值应合法: %v", err)
	}
	bad := []altAutoSettings{
		{MinAvailable: -1, RandomMinSec: 600, RandomMaxSec: 1200, MaxPerRound: 5},
		{MinAvailable: 5, RandomMinSec: 60, RandomMaxSec: 1200, MaxPerRound: 0},
		{MinAvailable: 5, RandomMinSec: 60, RandomMaxSec: 1200, MaxPerRound: 51},
		{MinAvailable: 5, RandomMinSec: 30, RandomMaxSec: 1200, MaxPerRound: 5},   // 下限太小
		{MinAvailable: 5, RandomMinSec: 1200, RandomMaxSec: 600, MaxPerRound: 5},  // 上限 < 下限
		{MinAvailable: 5, RandomMinSec: 60, RandomMaxSec: 99999, MaxPerRound: 5},  // 上限太大
	}
	for i, b := range bad {
		if err := validAltAutoSettings(b); err == nil {
			t.Errorf("用例 %d 应被判非法: %+v", i, b)
		}
	}
}

// 设置往返：保存到存储、读回一致，并且是跟着注册机设置一起落盘的。
func TestALTAAutoSettingsRoundTrip(t *testing.T) {
	s := altAutoTestServer(t)
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	in := altAutoSettings{
		Enabled: true, ThresholdEnabled: false, MinAvailable: 9,
		FrequencyEnabled: true, RandomMinSec: 300, RandomMaxSec: 900, MaxPerRound: 3,
	}
	body, _ := json.Marshal(in)
	rec := httptest.NewRecorder()
	s.apiALTAPIAutoSettings(rec, httptest.NewRequest(http.MethodPut, "/api/altapi/auto", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	// 存储里读回
	got := altAutoSettingsFrom(m.store.Settings())
	if got != in {
		t.Fatalf("存储读回 %+v, want %+v", got, in)
	}
	// GET 也应返回同一份
	rec2 := httptest.NewRecorder()
	s.apiALTAPIAutoSettings(rec2, httptest.NewRequest(http.MethodGet, "/api/altapi/auto", nil))
	var out struct {
		Settings altAutoSettings `json:"settings"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Settings != in {
		t.Fatalf("GET 返回 %+v, want %+v", out.Settings, in)
	}
}

// 非法设置必须被拒，且不能把存储改坏。
func TestALTAAutoSettingsRejectsInvalid(t *testing.T) {
	s := altAutoTestServer(t)
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	before := m.store.Settings()
	body, _ := json.Marshal(altAutoSettings{MinAvailable: -5, RandomMinSec: 1, RandomMaxSec: 2, MaxPerRound: 0})
	rec := httptest.NewRecorder()
	s.apiALTAPIAutoSettings(rec, httptest.NewRequest(http.MethodPut, "/api/altapi/auto", strings.NewReader(string(body))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("非法设置应 400，实际 %d", rec.Code)
	}
	after := m.store.Settings()
	if altAutoSettingsFrom(after) != altAutoSettingsFrom(before) {
		t.Error("非法请求不应改动已保存的设置")
	}
}

// 可用数统计：禁用与硬熔断都不算可用。
func TestALTAAutoAvailability(t *testing.T) {
	s := altAutoTestServer(t)
	_, err := s.cfg.Update(-1, func(cfg *config.Config) error {
		cfg.Accounts = append(cfg.Accounts,
			config.Account{ID: "alt-a", Provider: config.ProviderALTAPI, Name: "a", Enabled: true, RuntimeKey: "sk-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Group: config.GroupForProvider(config.ProviderALTAPI)},
			config.Account{ID: "alt-b", Provider: config.ProviderALTAPI, Name: "b", Enabled: false, RuntimeKey: "sk-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Group: config.GroupForProvider(config.ProviderALTAPI)},
			config.Account{ID: "xx", Provider: config.ProviderOpenDesign, Name: "x", Enabled: true, RuntimeKey: "sk-amr-x"},
		)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	available, total := s.altAutoAvailability(s.cfg.Get())
	if total != 2 {
		t.Errorf("只应统计 altapi 账号，total=%d", total)
	}
	if available != 1 {
		t.Errorf("禁用的不该算可用，available=%d", available)
	}
}

// 阈值补号：可用数低于阈值时，优先导入已建 Key 的候选，而不是重新注册。
func TestALTAAutoPrefersImportOverRegister(t *testing.T) {
	s := altAutoTestServer(t)
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	// 一条有 Key 未入池的候选
	c, err := m.store.Add(altapisignupstore.Candidate{
		Email: "imp@mail.example.com", Status: altapisignupstore.KeyCreated,
		APIKey: "sk-auto-imp-0000000000000000000000000000", SessionCookie: "sess",
	})
	if err != nil {
		t.Fatal(err)
	}
	st := m.store.Settings()
	auto := defaultAltAutoSettings()
	auto.Enabled = true
	auto.ThresholdEnabled = true
	auto.MinAvailable = 3
	st.Auto = &auto
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	if s.altAuto == nil {
		t.Fatal("自动补号管理器未初始化")
	}
	s.altAuto.mu.Lock()
	s.altAuto.settings = auto
	s.altAuto.mu.Unlock()

	s.altAutoReplenish(t.Context(), 3, 1, "低于阈值")

	got, _ := m.store.Get(c.ID)
	if got.ImportedAcctID == "" {
		t.Error("阈值补号应优先把已建 Key 的候选导入号池")
	}
}

// 没有可注册候选时，不该报错，只记一条「缺库存」。
func TestALTAAutoRegisterNoCandidatesIsNotError(t *testing.T) {
	s := altAutoTestServer(t)
	if s.altAuto == nil {
		t.Fatal("自动补号管理器未初始化")
	}
	// 不 panic、不阻塞即为通过
	s.altAutoRegister(t.Context(), 2, "按频率")
	time.Sleep(50 * time.Millisecond)
}

// 关闭状态下也要统计池子现状。
//
// 之前 !Enabled 时直接 return，total/available 永远是 0，页面上看起来像
// 功能坏了，用户也没有依据判断"该不该开"。
func TestALTAAutoStatusReportedWhenDisabled(t *testing.T) {
	s := altAutoTestServer(t)
	if s.altAuto == nil {
		t.Fatal("自动补号管理器未初始化")
	}
	_, err := s.cfg.Update(-1, func(cfg *config.Config) error {
		cfg.Accounts = append(cfg.Accounts,
			config.Account{ID: "alt-x", Provider: config.ProviderALTAPI, Name: "x",
				Enabled: true, RuntimeKey: "sk-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
				Group: config.GroupForProvider(config.ProviderALTAPI)},
		)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	s.altAuto.mu.Lock()
	s.altAuto.settings = defaultAltAutoSettings() // Enabled=false
	s.altAuto.mu.Unlock()

	s.altAutoCheck(t.Context())

	v := s.altAutoView()
	if v.Enabled {
		t.Fatal("默认应关闭")
	}
	if v.PoolTotal != 1 || v.Available != 1 {
		t.Errorf("关闭时也要报告池子现状，实际 total=%d available=%d", v.PoolTotal, v.Available)
	}
	if v.LastCheck.IsZero() {
		t.Error("LastCheck 应被更新")
	}
}

// 自动补号发起的注册必须强制入池，不受 import_to_pool 开关约束。
//
// 手动注册时这个开关是用户意图（可能想先看结果再决定入池）；自动补号不同——
// 补了号不进号池，可用数还是不够，下一轮会再补，等于无限消耗邮箱与出口 IP。
func TestALTAAutoForcesImportEvenWhenSwitchOff(t *testing.T) {
	s := altAutoTestServer(t)
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	// 关掉手动开关
	st := m.store.Settings()
	st.ImportToPool = false
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}

	mk := func(email string) altapisignupstore.Candidate {
		c, err := m.store.Add(altapisignupstore.Candidate{
			Email: email, Status: altapisignupstore.Queued,
			APIKey: "sk-forced-import-0000000000000000000000000", SessionCookie: "sess",
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	manual := mk("manual@mail.example.com")
	auto := mk("auto@mail.example.com")

	// 手动来源：开关关着，不该入池
	m.autoImport(manual.ID, manual.Email, originManual)
	if got, _ := m.store.Get(manual.ID); got.ImportedAcctID != "" {
		t.Error("手动注册在开关关闭时不该自动入池")
	}

	// 自动来源：开关关着也必须入池
	m.autoImport(auto.ID, auto.Email, originAuto)
	if got, _ := m.store.Get(auto.ID); got.ImportedAcctID == "" {
		t.Error("自动补号必须强制入池，不受 import_to_pool 开关约束")
	}
}

// runBatch 的来源必须一路传到 autoImport：三个入口各自标注正确。
func TestALTABatchOriginWiring(t *testing.T) {
	regSrc := readServerFile(t, "altapi_registration.go")
	// 两个手动入口
	if c := strings.Count(regSrc, "go m.runBatch(ids, originManual)"); c != 1 {
		t.Errorf("批量注册入口应标 originManual，实际 %d 处", c)
	}
	if c := strings.Count(regSrc, "go m.runBatch([]string{id}, originManual)"); c != 1 {
		t.Errorf("单条注册入口应标 originManual，实际 %d 处", c)
	}
	if c := strings.Count(regSrc, "go m.runBatch(ids, originAuto)"); c != 0 {
		t.Error("registration.go 不该出现自动来源")
	}
	autoSrc := readServerFile(t, "altapi_auto.go")
	if !strings.Contains(autoSrc, "go m.runBatch(ids, originAuto)") {
		t.Error("自动补号入口应标 originAuto")
	}
	// autoImport 的守卫必须先放行 auto
	if !strings.Contains(regSrc, "if origin != originAuto && !m.store.Settings().ImportToPool") {
		t.Error("autoImport 守卫应让 originAuto 跳过开关检查")
	}
	// Python job 也要强制
	if !strings.Contains(regSrc, "if origin == originAuto {") || !strings.Contains(regSrc, "importToPool = true") {
		t.Error("job settings 里应对 originAuto 强制 import_to_pool=true")
	}
}

// readServerFile 读同目录下的源码文件，用于结构性断言。
func readServerFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(".", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 频率注册必须每次重新随机，且落在 [min, max] 闭区间内。
//
// 用户要求「填 5 和 10，就是随机 5–10 分钟注册一个」。这里用秒级小值验证
// 分布，避免测试真的等几分钟。
func TestALTAAutoScheduleIsRandomWithinRange(t *testing.T) {
	s := altAutoTestServer(t)
	if s.altAuto == nil {
		t.Fatal("自动补号管理器未初始化")
	}
	a := s.altAuto
	a.mu.Lock()
	a.settings = altAutoSettings{
		Enabled: true, ThresholdEnabled: false, MinAvailable: 5,
		FrequencyEnabled: true, RandomMinSec: 300, RandomMaxSec: 600, MaxPerRound: 1,
	}
	a.mu.Unlock()

	var lo, hi time.Time
	seen := map[int]bool{}
	for i := 0; i < 50; i++ {
		a.mu.Lock()
		before := time.Now()
		a.scheduleNextLocked()
		delta := int(a.notBefore.Sub(before).Seconds())
		a.mu.Unlock()

		if delta < 300 || delta > 601 { // 闭区间，容许一秒的取整误差
			t.Fatalf("第 %d 次间隔 %ds 越界，应在 300..600", i, delta)
		}
		seen[delta] = true
		if lo.IsZero() || delta < int(lo.Sub(before).Seconds()) {
			lo = before.Add(time.Duration(delta) * time.Second)
		}
		if delta > int(hi.Sub(hi).Seconds()) {
			hi = before.Add(time.Duration(delta) * time.Second)
		}
	}
	// 50 次抽样至少应出现 10 个不同值，否则说明不是真随机
	if len(seen) < 10 {
		t.Errorf("抽样 50 次只有 %d 个不同值，随机性不足", len(seen))
	}
}

// 上下限相等时退化为固定值，且不 panic（rand.Intn(0) 会 panic）。
func TestALTAAutoScheduleEqualBounds(t *testing.T) {
	s := altAutoTestServer(t)
	a := s.altAuto
	a.mu.Lock()
	a.settings = altAutoSettings{RandomMinSec: 120, RandomMaxSec: 120, MaxPerRound: 1}
	a.scheduleNextLocked()
	delta := int(time.Until(a.notBefore).Seconds())
	a.mu.Unlock()
	if delta < 118 || delta > 121 {
		t.Errorf("等上下限应得到 120s，实际 %ds", delta)
	}
}

// 上限小于下限时按上限处理，不能 panic。
func TestALTAAutoScheduleInvertedBounds(t *testing.T) {
	s := altAutoTestServer(t)
	a := s.altAuto
	a.mu.Lock()
	a.settings = altAutoSettings{RandomMinSec: 600, RandomMaxSec: 300, MaxPerRound: 1}
	a.scheduleNextLocked() // 不应 panic
	delta := int(time.Until(a.notBefore).Seconds())
	a.mu.Unlock()
	if delta < 598 || delta > 601 {
		t.Errorf("倒置区间应退化为下限 600s，实际 %ds", delta)
	}
}
