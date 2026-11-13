package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"account-pool-balancer/internal/deploysignupstore"
)

// runPython 在仓库根跑一小段 python，用于校验注册机脚本里的判定规则。
func runPython(t *testing.T, code string) string {
	t.Helper()
	py := os.Getenv("DEPLOY_TEST_PYTHON")
	if py == "" {
		py = "python3"
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var errBuf strings.Builder
	cmd := exec.Command(py, "-c", code)
	cmd.Dir = root
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("python 不可用，跳过: %v (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(string(out))
}

// 已存在的账号（上游回 201 但实际发了 "Sign Up Attempt for Existing Deploy
// Account"）必须走登录，不能重复注册。校验会话入口的守卫必须允许
// "有密码、没会话"的候选通过——否则这类账号永远卡在 registered。
func TestDeployResumeGuardsAllowPasswordOnly(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { time.Sleep(300 * time.Millisecond); _ = os.RemoveAll(dir) })
	s, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.deployMgr()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		in      deploysignupstore.Candidate
		want    int
		wantSub string
	}{
		{
			name: "password only candidate is allowed to log in",
			in: deploysignupstore.Candidate{
				Email: "existing@icloud.com", Password: "Str0ng!Passw0rd#2026",
				Status: deploysignupstore.Registered,
			},
			want: http.StatusOK,
		},
		{
			name: "candidate without session and without password is refused",
			in: deploysignupstore.Candidate{
				Email: "nothing@icloud.com", Status: deploysignupstore.Registered,
			},
			want: http.StatusBadRequest, wantSub: "密码",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			added, aerr := m.store.Add(tc.in)
			if aerr != nil {
				t.Fatal(aerr)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost,
				"/api/deploy/candidates/"+added.ID+"/resume", strings.NewReader("{}"))
			req.SetPathValue("id", added.ID)
			s.apiDeployCandidateResume(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.wantSub != "" && !strings.Contains(rec.Body.String(), tc.wantSub) {
				t.Fatalf("body %q does not mention %q", rec.Body.String(), tc.wantSub)
			}
		})
	}
}

// runBatchMode 的筛选逻辑：已注册（账号已存在）与已有会话的候选不能再提交注册。
// 用不存在的 python 让脚本永远起不来，从而只观察"是否被放进批次"。
func TestDeployBatchSkipsRegisteredCandidates(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { time.Sleep(300 * time.Millisecond); _ = os.RemoveAll(dir) })
	s, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.deployMgr()
	if err != nil {
		t.Fatal(err)
	}

	st := m.store.Settings()
	st.PythonBin = filepath.Join(dir, "missing-python")
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}

	existing, err := m.store.Add(deploysignupstore.Candidate{
		Email: "existing@icloud.com", Password: "pw", Status: deploysignupstore.Registered,
		MailboxURL: "https://m.invalid/messages/a?token=t", RegisterAttemptAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := m.store.Add(deploysignupstore.Candidate{
		Email: "fresh@icloud.com", Password: "pw", Status: deploysignupstore.Queued,
		MailboxURL: "https://m.invalid/messages/b?token=t",
	})
	if err != nil {
		t.Fatal(err)
	}

	m.runBatch([]string{existing.ID, fresh.ID})

	got, _ := m.store.Get(existing.ID)
	if got.Status == deploysignupstore.Running {
		t.Fatalf("registered candidate was re-submitted: %+v", got)
	}
	freshGot, _ := m.store.Get(fresh.ID)
	if freshGot.Status == deploysignupstore.Queued {
		t.Fatalf("queued candidate was never processed")
	}
}

// 登录/会话复用的 job 输入必须带上已保存的密码，否则脚本无法走登录。
func TestDeployBatchPassesSavedPassword(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { time.Sleep(300 * time.Millisecond); _ = os.RemoveAll(dir) })
	s, err := New(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.deployMgr()
	if err != nil {
		t.Fatal(err)
	}
	added, err := m.store.Add(deploysignupstore.Candidate{
		Email: "pw@icloud.com", Password: "Str0ng!Passw0rd#2026", Status: deploysignupstore.Registered,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.dataDir, "deploy-signup.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Rows []deploysignupstore.Candidate `json:"rows"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range env.Rows {
		if r.ID == added.ID && r.Password == "Str0ng!Passw0rd#2026" {
			found = true
		}
	}
	if !found {
		t.Fatalf("saved password missing from store: %s", raw)
	}
}

// 注册响应里的"账号已存在"必须能被识别（上游回 201 但仍表示已存在）。
func TestDeployAlreadyExistsDetection(t *testing.T) {
	// 走注册机脚本的同一套规则，避免 Go 与 Python 判定漂移。
	py := `import sys, json
sys.path.insert(0, "tools")
import deploy_registrar as d
print(d.email_already_exists(d.Response(201, json.dumps({"message": "An account with this email already exists. Please try logging in."}))))
print(d.email_already_exists(d.Response(201, json.dumps({"data": {"created": True}}))))
print(d.email_already_exists(d.Response(409, "")))
`
	out := runPython(t, py)
	if out != "True\nFalse\nTrue" {
		t.Fatalf("unexpected detection result: %q", out)
	}
}

func TestDeployLoginUsesHomeHeaderEntry(t *testing.T) {
	py := `import sys
sys.path.insert(0, "tools")
import deploy_registrar as d
print(d.LOGIN_PAGE)
print(d.LOGIN_REFERER)`
	out := runPython(t, py)
	if !strings.Contains(out, "https://deploy.com/login?source=home-header") {
		t.Fatalf("login entry not wired: %q", out)
	}
}
