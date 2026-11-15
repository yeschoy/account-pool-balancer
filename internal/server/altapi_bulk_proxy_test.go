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

	// 再导入一批不带 proxies：新候选直连，旧候选的绑定不能被动到。
	body2, _ := json.Marshal(map[string]any{
		"lines": "d@mail.example.com----https://box.test/m/d?token=4",
	})
	w2 := httptest.NewRecorder()
	s.apiALTAPICandidateBulk(w2, httptest.NewRequest(http.MethodPost, "/api/altapi/candidates/bulk", strings.NewReader(string(body2))))
	if w2.Code != http.StatusOK {
		t.Fatalf("second import status=%d body=%s", w2.Code, w2.Body.String())
	}
	for _, c := range m.store.List() {
		if c.Email == "d@mail.example.com" && c.ProxyURL != "" {
			t.Errorf("new candidate with no proxy pool got %q, want direct", c.ProxyURL)
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
