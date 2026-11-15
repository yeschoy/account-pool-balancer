package server

import (
	"encoding/json"
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
