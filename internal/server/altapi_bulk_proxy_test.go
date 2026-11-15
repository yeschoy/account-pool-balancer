package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"account-pool-balancer/internal/altapisignupstore"
	"account-pool-balancer/internal/config"
)

// 批量导入必须真的把代理绑到候选上。
//
// 后端此前只解码 lines，前端传的 proxies 被静默丢弃：页面上填了代理池、
// 点导入、看起来成功，实际每个候选仍然是直连。这条测试锁住绑定行为，
// 并锁住「空代理不清空已有绑定」这个 REST 语义。
func TestALTAPIBulkImportBindsProxies(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}

	proxies := []string{"socks5://u:p@1.2.3.4:1080", "http://u:p@5.6.7.8:8080"}
	body, _ := json.Marshal(map[string]any{
		"lines": "a@mail.example.com----https://box.test/m/a?token=1\n" +
			"b@mail.example.com----https://box.test/m/b?token=2\n" +
			"c@mail.example.com----https://box.test/m/c?token=3\n" +
			"broken-line-without-separator\n",
		"proxies": proxies,
	})
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/altapi/candidates/bulk", strings.NewReader(string(body)))
	s.apiALTAPICandidateBulk(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		Added   int      `json:"added"`
		Skipped []string `json:"skipped"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if out.Added != 3 {
		t.Fatalf("added=%d, want 3 (skipped=%v)", out.Added, out.Skipped)
	}
	if len(out.Skipped) != 1 || out.Skipped[0] != "broken-line-without-separator" {
		t.Fatalf("skipped=%v, want the separator-less line", out.Skipped)
	}

	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	cands := m.store.List()
	if len(cands) != 3 {
		t.Fatalf("stored %d candidates, want 3", len(cands))
	}
	// 轮转绑定：三个候选拿到 p0、p1、p0。
	want := []string{proxies[0], proxies[1], proxies[0]}
	for i, c := range cands {
		if c.ProxyURL != want[i] {
			t.Errorf("candidate[%d] %s proxy=%q, want %q", i, c.Email, c.ProxyURL, want[i])
		}
	}

	// 再导入一批不带 proxies：第一批显式传的代理已存成池子，新候选按池轮转绑定
	// （旧候选的绑定不受影响）。
	body2, _ := json.Marshal(map[string]any{
		"lines": "d@mail.example.com----https://box.test/m/d?token=4",
	})
	w2 := httptest.NewRecorder()
	s.apiALTAPICandidateBulk(w2, httptest.NewRequest(http.MethodPost, "/api/altapi/candidates/bulk", strings.NewReader(string(body2))))
	if w2.Code != http.StatusOK {
		t.Fatalf("second import status=%d body=%s", w2.Code, w2.Body.String())
	}
	for _, c := range m.store.List() {
		if c.Email == "d@mail.example.com" && c.ProxyURL != proxies[0] {
			t.Errorf("second batch proxy = %q, want %q (轮转到池首)", c.ProxyURL, proxies[0])
		}
		if c.Email == "a@mail.example.com" && c.ProxyURL != proxies[0] {
			t.Errorf("existing binding was clobbered: %q", c.ProxyURL)
		}
	}
}

// scriptPaths 在镜像里必须找到打进去的注册机，并优先用装好 PySocks 的 venv。
// 缺这一步时点「注册」只会得到「找不到 altapi_registrar.py」，而这个错误
// 只在真正注册时才出现，前端接线测试发现不了。
func TestALTAPIScriptPathsResolves(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	// go test 的 cwd 是包目录（internal/server），而仓库根的 tools/ 在两级之上。
	// 用 ALTAPI_REGISTRAR 显式指向真实脚本，验证解析与可读性这一层；
	// 镜像内的 /usr/local/share/altapi-signup 分支由 Dockerfile 保证。
	_, thisFile, _, _ := runtime.Caller(0)
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(thisFile)))
	real := filepath.Join(repoRoot, "tools", "altapi_registrar.py")
	t.Setenv("ALTAPI_REGISTRAR", real)
	script, py, err := m.scriptPaths(m.store.Settings())
	if err != nil {
		t.Fatalf("scriptPaths: %v", err)
	}
	if script != real {
		t.Errorf("script=%q, want %q", script, real)
	}
	if strings.TrimSpace(py) == "" {
		t.Error("python interpreter is empty")
	}
	if fi, err := os.Stat(script); err != nil || fi.IsDir() {
		t.Errorf("script %q not readable: %v", script, err)
	}
}

// 环境变量优先级：ALTAPI_PYTHON > venv > python3。
func TestALTAPIDefaultPythonFallsBack(t *testing.T) {
	// 测试机上通常没有 /opt/altapi-signup，此时必须返回空串让调用方退回 python3，
	// 而不是返回一个不存在的路径。
	got := altapiDefaultPython()
	if got != "" {
		if _, err := os.Stat(got); err != nil {
			t.Errorf("altapiDefaultPython returned missing path %q", got)
		}
	}
}

// 代理池保存后要能复用：新导入的候选按池顺序一号一个，候选多于代理时从头发复用。
//
// 以前导入只看请求里的 proxies，完全不碰 settings.proxy_pool：用户在「ALTapi 注册设置」
// 里存好池子，回到注册机页面不重新粘贴，导入出来的候选全是直连，得手动一条条改。
func TestALTAPIBulkImportUsesSavedProxyPool(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	st := m.store.Settings()
	st.ProxyPool = []string{"socks5://u:p@10.0.0.1:1080", "socks5://u:p@10.0.0.2:1080"}
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}

	// 只给 lines，不给 proxies —— 模拟用户已存好池子后直接点「导入」。
	body, _ := json.Marshal(map[string]any{
		"lines": "a@mail.example.com----https://box.test/a?token=1\n" +
			"b@mail.example.com----https://box.test/b?token=2\n" +
			"c@mail.example.com----https://box.test/c?token=3\n",
	})
	w := httptest.NewRecorder()
	s.apiALTAPICandidateBulk(w, httptest.NewRequest(http.MethodPost, "/api/altapi/candidates/bulk",
		strings.NewReader(string(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	want := map[string]string{
		"a@mail.example.com": "socks5://u:p@10.0.0.1:1080",
		"b@mail.example.com": "socks5://u:p@10.0.0.2:1080",
		"c@mail.example.com": "socks5://u:p@10.0.0.1:1080", // 池只有 2 个，第三个从头复用
	}
	for _, c := range m.store.List() {
		if got := want[c.Email]; got != c.ProxyURL {
			t.Errorf("%s proxy = %q, want %q", c.Email, c.ProxyURL, got)
		}
	}
}

// 请求里显式带 proxies 时用它，并顺手存成池子供下次复用。
func TestALTAPIBulkImportPersistsExplicitProxies(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	explicit := []string{"http://u:p@7.7.7.7:8080"}
	body, _ := json.Marshal(map[string]any{
		"lines":   "d@mail.example.com----https://box.test/d?token=4",
		"proxies": explicit,
	})
	w := httptest.NewRecorder()
	s.apiALTAPICandidateBulk(w, httptest.NewRequest(http.MethodPost, "/api/altapi/candidates/bulk",
		strings.NewReader(string(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	for _, c := range m.store.List() {
		if c.Email == "d@mail.example.com" && c.ProxyURL != explicit[0] {
			t.Errorf("proxy = %q, want %q", c.ProxyURL, explicit[0])
		}
	}
	saved := m.store.Settings().ProxyPool
	if !equalStrings(saved, explicit) {
		t.Errorf("saved pool = %v, want %v（显式代理应存成池子）", saved, explicit)
	}

	// 池子存住之后，下一次不带 proxies 的导入也要能拿到。
	body2, _ := json.Marshal(map[string]any{"lines": "e@mail.example.com----https://box.test/e?token=5"})
	w2 := httptest.NewRecorder()
	s.apiALTAPICandidateBulk(w2, httptest.NewRequest(http.MethodPost, "/api/altapi/candidates/bulk",
		strings.NewReader(string(body2))))
	if w2.Code != http.StatusOK {
		t.Fatalf("second import status=%d", w2.Code)
	}
	for _, c := range m.store.List() {
		if c.Email == "e@mail.example.com" && c.ProxyURL != explicit[0] {
			t.Errorf("second import proxy = %q, want %q", c.ProxyURL, explicit[0])
		}
	}
}

// 没有代理池时导入保持直连（不能凭空造代理）。
func TestALTAPIBulkImportWithoutPoolStaysDirect(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"lines": "f@mail.example.com----https://box.test/f?token=6",
	})
	w := httptest.NewRecorder()
	s.apiALTAPICandidateBulk(w, httptest.NewRequest(http.MethodPost, "/api/altapi/candidates/bulk",
		strings.NewReader(string(body))))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	for _, c := range m.store.List() {
		if c.Email == "f@mail.example.com" && c.ProxyURL != "" {
			t.Errorf("proxy = %q, want direct (empty)", c.ProxyURL)
		}
	}
}

// 设置里开了 import_to_pool 时，候选建出 Key 就应自动进号池。
//
// 以前 import_to_pool 只被塞进 Python job file，Go 侧完全没有自动导入逻辑：
// 用户勾着「注册成功后自动建 Key 并入池」，注册完列表里还是「未导入」，
// 得再去点「全部导入号池」。
func TestALTAPIAutoImportOnKeyCreated(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	st := m.store.Settings()
	st.ImportToPool = true
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}

	c, err := m.store.Add(altapisignupstore.Candidate{
		Email: "auto@mail.example.com", MailboxURL: "https://box.test/a?token=1",
		Status: altapisignupstore.Queued,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 模拟 Python 侧发出 item_done/key_created。
	m.handleEvent(map[string]any{
		"type":       "item_done",
		"index":      float64(0),
		"status":     string(altapisignupstore.KeyCreated),
		"email":      "auto@mail.example.com",
		"api_key":    "sk-test-auto-import-key-0000000000000000000000",
		"session_cookie": "sess-abc",
		"account_id": "777",
		"token_id":   "42",
		"group":      "vip",
		"quota":      float64(500000),
		"quota_per_unit": float64(500000),
		"balance_usd":    float64(1),
	}, []string{c.ID})

	got, err := m.store.Get(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ImportedAcctID == "" {
		t.Fatal("开了 import_to_pool 但没有自动入池")
	}
	// 幂等：再来一次同样的 item_done 不能建出第二个账号。
	before := len(s.cfg.Get().Accounts)
	m.handleEvent(map[string]any{
		"type": "item_done", "index": float64(0),
		"status": string(altapisignupstore.KeyCreated), "email": "auto@mail.example.com",
	}, []string{c.ID})
	if after := len(s.cfg.Get().Accounts); after != before {
		t.Fatalf("重复 item_done 又建了账号：%d -> %d", before, after)
	}
}

// 关掉 import_to_pool 时不能自动入池。
func TestALTAPIAutoImportDisabled(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	st := m.store.Settings()
	st.ImportToPool = false
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	c, _ := m.store.Add(altapisignupstore.Candidate{
		Email: "no-auto@mail.example.com", Status: altapisignupstore.Queued,
	})
	m.handleEvent(map[string]any{
		"type": "item_done", "index": float64(0),
		"status": string(altapisignupstore.KeyCreated), "email": "no-auto@mail.example.com",
		"api_key": "sk-test-no-auto-000000000000000000000000000",
	}, []string{c.ID})
	if got, _ := m.store.Get(c.ID); got.ImportedAcctID != "" {
		t.Fatal("关掉了 import_to_pool 仍然自动入池")
	}
}

// 注册机解释器的解析优先级。
//
// 线上真实事故：设置页的 python_bin 默认值就是 "python3"，用户点过一次保存
// 就把它写进配置文件，于是永远走系统解释器；而系统解释器没装 PySocks，
// 候选一旦绑了 socks5 出口就在第一批全部失败（12 条候选全报「需要 PySocks」）。
// 裸命令名不能压过镜像里装好依赖的 venv。
func TestResolveALTAPIPython(t *testing.T) {
	venv := altapiDefaultPython() // 测试机上通常为空

	cases := []struct {
		name    string
		setting string
		env     string
		want    func(got string) bool
	}{
		{
			name:    "空设置走默认",
			setting: "",
			want:    func(got string) bool { return got != "" },
		},
		{
			name:    "裸 python3 不能让 venv 失效",
			setting: "python3",
			want: func(got string) bool {
				if venv == "" {
					return got == "python3" // 没有 venv 时只能退回
				}
				return got == venv
			},
		},
		{
			name:    "绝对路径用户指定最高优先级",
			setting: "/custom/bin/python3",
			want:    func(got string) bool { return got == "/custom/bin/python3" },
		},
		{
			name:    "非绝对但用户确实写了",
			setting: "python3.11",
			want: func(got string) bool {
				if venv != "" {
					return got == venv // venv 优先，它才有依赖
				}
				return got == "python3.11"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveALTAPIPython(tc.setting)
			if !tc.want(got) {
				t.Errorf("resolveALTAPIPython(%q) = %q", tc.setting, got)
			}
		})
	}

	// 环境变量优先级：高于裸名，低于绝对路径。
	t.Setenv("ALTAPI_PYTHON", "/env/bin/python")
	if got := resolveALTAPIPython("python3"); got != "/env/bin/python" {
		t.Errorf("ALTAPI_PYTHON 未生效: %q", got)
	}
	if got := resolveALTAPIPython("/abs/bin/python"); got != "/abs/bin/python" {
		t.Errorf("绝对路径应压过环境变量: %q", got)
	}
}

// hasSocksProxy 只认 socks* 前缀（含 socks4/socks5/socks5h），http 代理不算。
func TestHasSocksProxy(t *testing.T) {
	yes := [][]any{
		{"socks5://u:p@1.2.3.4:1080"},
		{"http://a:1", "SOCKS5H://b:2"},
		{"socks4://c:3"},
	}
	no := [][]any{
		{},
		{"http://a:1", "https://b:2"},
	}
	for _, xs := range yes {
		if !hasSocksProxy(xs) {
			t.Errorf("hasSocksProxy(%v) = false, want true", xs)
		}
	}
	for _, xs := range no {
		if hasSocksProxy(xs) {
			t.Errorf("hasSocksProxy(%v) = true, want false", xs)
		}
	}
}

// 收码后的停顿必须明显长于其它步骤，且可配置。
//
// 整条链路里最像机器人的瞬间是「验证码一到就提交注册」：脚本会立刻填。
// 真人要切窗口、找信、复制、切回、粘贴，十几秒到一分多钟。
func TestALTAPICodeDelaySettings(t *testing.T) {
	st := altapisignupstore.DefaultSettings()
	if st.CodeDelaySec != 0 {
		t.Errorf("默认 code_delay_sec = %v, want 0（0 表示用注册机默认区间）", st.CodeDelaySec)
	}
	if st.PaceScale != 1.0 {
		t.Errorf("默认 pace_scale = %v, want 1.0", st.PaceScale)
	}

	// 往返：存盘读回不能丢
	s, err := altapisignupstore.Open(filepath.Join(t.TempDir(), "altapi-signup.json"))
	if err != nil {
		t.Fatal(err)
	}
	st.CodeDelaySec = 45
	st.PaceScale = 1.5
	if err := s.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	got := s.Settings()
	if got.CodeDelaySec != 45 || got.PaceScale != 1.5 {
		t.Fatalf("往返丢失: code_delay=%v pace=%v", got.CodeDelaySec, got.PaceScale)
	}
}

// 批量「分配代理」：只给未注册且直连的候选补出口。
//
// 已注册的账号出口已定，换 IP 会让上游看到「同一账号换地区登录」，风险更高，
// 必须走单条 PATCH 显式改。
func TestALTAPIAssignProxySkipsRegistered(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	st := m.store.Settings()
	st.ProxyPool = []string{"socks5://u:p@10.0.0.1:1080", "socks5://u:p@10.0.0.2:1080"}
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}

	mk := func(email string, status altapisignupstore.Status, proxy string) altapisignupstore.Candidate {
		c, err := m.store.Add(altapisignupstore.Candidate{
			Email: email, MailboxURL: "https://box.test/x?token=1",
			Status: status, ProxyURL: proxy,
		})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	queued := mk("q@mail.example.com", altapisignupstore.Queued, "")
	failed := mk("f@mail.example.com", altapisignupstore.Failed, "")
	hasProxy := mk("h@mail.example.com", altapisignupstore.Queued, "socks5://keep@1.1.1.1:1")
	registered := mk("r@mail.example.com", altapisignupstore.Registered, "")
	keyed := mk("k@mail.example.com", altapisignupstore.KeyCreated, "")

	rec := httptest.NewRecorder()
	s.apiALTAPIAssignProxy(rec, httptest.NewRequest(http.MethodPost,
		"/api/altapi/candidates/assign-proxy", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Assigned, Kept, Skipped, PoolSize int
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Assigned != 2 || out.Kept != 1 || out.Skipped != 2 || out.PoolSize != 2 {
		t.Fatalf("assigned=%d kept=%d skipped=%d pool=%d, want 2/1/2/2",
			out.Assigned, out.Kept, out.Skipped, out.PoolSize)
	}

	get := func(id string) altapisignupstore.Candidate {
		c, _ := m.store.Get(id)
		return c
	}
	if get(queued.ID).ProxyURL == "" || get(failed.ID).ProxyURL == "" {
		t.Error("未注册的候选应该被分配代理")
	}
	if got := get(hasProxy.ID).ProxyURL; got != "socks5://keep@1.1.1.1:1" {
		t.Errorf("已有代理被覆盖: %q", got)
	}
	if get(registered.ID).ProxyURL != "" {
		t.Error("已注册的候选不该被分配代理")
	}
	if get(keyed.ID).ProxyURL != "" {
		t.Error("已建 Key 的候选不该被分配代理")
	}

	// 池子为空时必须明确报错，不能静默什么都不做。
	st.ProxyPool = nil
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	s.apiALTAPIAssignProxy(rec2, httptest.NewRequest(http.MethodPost,
		"/api/altapi/candidates/assign-proxy", strings.NewReader(`{}`)))
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("空池应返回 400，实际 %d", rec2.Code)
	}
}

// 选中 ids 时只处理这几条。
func TestALTAPIAssignProxyHonoursIDs(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	st := m.store.Settings()
	st.ProxyPool = []string{"socks5://u:p@10.0.0.1:1080"}
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	a, _ := m.store.Add(altapisignupstore.Candidate{Email: "a@mail.example.com", Status: altapisignupstore.Queued})
	b, _ := m.store.Add(altapisignupstore.Candidate{Email: "b@mail.example.com", Status: altapisignupstore.Queued})

	body, _ := json.Marshal(map[string]any{"ids": []string{a.ID}})
	rec := httptest.NewRecorder()
	s.apiALTAPIAssignProxy(rec, httptest.NewRequest(http.MethodPost,
		"/api/altapi/candidates/assign-proxy", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	ga, _ := m.store.Get(a.ID)
	gb, _ := m.store.Get(b.ID)
	if ga.ProxyURL == "" {
		t.Error("选中的候选应被分配")
	}
	if gb.ProxyURL != "" {
		t.Error("未选中的候选不该被动")
	}
}

// 已入池的账号可以退回注册机重新跑。
//
// 账号 ID 是 "alt-<candidateID>"，所以能反查源候选；退回时清掉 ImportedAcctID
// 让候选重新可导入，并把账号停用，避免同一 Key 同时被号池调度和注册机重跑。
func TestALTAPIReturnToRegistrar(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}

	// 造候选 + 入池
	c, err := m.store.Add(altapisignupstore.Candidate{
		Email: "ret@mail.example.com", MailboxURL: "https://box.test/r?token=1",
		Status: altapisignupstore.KeyCreated,
		APIKey: "sk-ret-test-000000000000000000000000000000",
		SessionCookie: "sess-ret", AccountID: "900",
	})
	if err != nil {
		t.Fatal(err)
	}
	acct, err := s.importALTAPICandidate(c)
	if err != nil {
		t.Fatal(err)
	}
	if acct.ID != "alt-"+c.ID {
		t.Fatalf("账号 ID = %q, want alt-%s（退回依赖这个映射）", acct.ID, c.ID)
	}
	got, _ := m.store.Get(c.ID)
	if got.ImportedAcctID == "" {
		t.Fatal("导入后候选应带 imported_account_id")
	}

	body, _ := json.Marshal(map[string]any{"ids": []string{acct.ID}})
	rec := httptest.NewRecorder()
	s.apiALTAPIReturnToRegistrar(rec, httptest.NewRequest(http.MethodPost,
		"/api/altapi/return-to-registrar", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Returned int      `json:"returned"`
		Created  int      `json:"created"`
		Failed   []string `json:"failed"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Returned != 1 || len(out.Failed) != 0 {
		t.Fatalf("returned=%d failed=%v", out.Returned, out.Failed)
	}

	// 候选：清空入池标记，状态回到 failed（可重试）
	back, _ := m.store.Get(c.ID)
	if back.ImportedAcctID != "" {
		t.Errorf("退回后 imported_account_id 应清空，实际 %q", back.ImportedAcctID)
	}
	if back.Status != altapisignupstore.Failed {
		t.Errorf("退回后状态 = %q, want failed（可重试）", back.Status)
	}
	// 账号：保留但停用
	var live *config.Account
	for _, a := range s.cfg.Get().Accounts {
		if a.ID == acct.ID {
			x := a
			live = &x
		}
	}
	if live == nil {
		t.Fatal("退回不应删除账号")
	}
	if live.Enabled {
		t.Error("退回后账号应停用，避免同一 Key 双路调度")
	}
}

// 手工添加的账号（没有源候选）退回时应新建一条候选。
func TestALTAPIReturnToRegistrarCreatesCandidate(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.altapiMgr()
	if err != nil {
		t.Fatal(err)
	}
	// 账号 ID 不是 alt-<候选> 形态：视作手工账号
	_, err = s.cfg.Update(-1, func(cfg *config.Config) error {
		cfg.Accounts = append(cfg.Accounts, config.Account{
			ID: "manual-1", Provider: config.ProviderALTAPI, Name: "manual",
			Email: "manual@mail.example.com", Enabled: true,
			RuntimeKey: "sk-manual", Group: config.GroupForProvider(config.ProviderALTAPI),
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]any{"ids": []string{"manual-1"}})
	rec := httptest.NewRecorder()
	s.apiALTAPIReturnToRegistrar(rec, httptest.NewRequest(http.MethodPost,
		"/api/altapi/return-to-registrar", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Returned int `json:"returned"`
		Created  int `json:"created"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Returned != 1 || out.Created != 1 {
		t.Fatalf("returned=%d created=%d, want 1/1", out.Returned, out.Created)
	}
	found := false
	for _, c := range m.store.List() {
		if c.Email == "manual@mail.example.com" && c.APIKey == "sk-manual" {
			found = true
		}
	}
	if !found {
		t.Error("没有为手工账号建出候选")
	}
}

// 非 altapi 账号、空 ids 要明确拒绝。
func TestALTAPIReturnToRegistrarGuards(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.apiALTAPIReturnToRegistrar(rec, httptest.NewRequest(http.MethodPost,
		"/api/altapi/return-to-registrar", strings.NewReader(`{"ids":[]}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("空 ids 应 400，实际 %d", rec.Code)
	}

	body, _ := json.Marshal(map[string]any{"ids": []string{"not-exist"}})
	rec2 := httptest.NewRecorder()
	s.apiALTAPIReturnToRegistrar(rec2, httptest.NewRequest(http.MethodPost,
		"/api/altapi/return-to-registrar", strings.NewReader(string(body))))
	if rec2.Code != http.StatusOK {
		t.Fatalf("status=%d", rec2.Code)
	}
	var out struct {
		Returned int      `json:"returned"`
		Failed   []string `json:"failed"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &out)
	if out.Returned != 0 || len(out.Failed) != 1 {
		t.Errorf("不存在的账号应记入 failed，实际 returned=%d failed=%v", out.Returned, out.Failed)
	}
}
