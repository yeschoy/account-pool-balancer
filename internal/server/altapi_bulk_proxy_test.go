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
