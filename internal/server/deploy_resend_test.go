package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"account-pool-balancer/internal/deploysignupstore"
)

// deploy.com 的验证信可能晚一小时以上才到。重发验证只能用于"已注册、
// 还没有会话"的候选；已验证或已有会话的账号重发会把已激活的链接换掉。
func TestDeployResendVerificationGuards(t *testing.T) {
	// 假接码服务：任何别名都返回空收件箱，测试不发真实网络请求。
	mb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"data":{"emails":[]}}`))
	}))
	defer mb.Close()
	fakeMailboxURL := mb.URL + "/messages/pending%40icloud.com?token=test-secret"

	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.deployMgr()
	if err != nil {
		t.Fatal(err)
	}
	// 缩短取信超时，避免测试真的等满 240s
	st := m.store.Settings()
	st.VerifyTimeoutSec = 1
	if err := m.store.SaveSettings(st); err != nil {
		t.Fatal(err)
	}

	mail := "https://icloud.mida.vip/messages/a%40icloud.com?token=secret"
	cases := []struct {
		name    string
		in      deploysignupstore.Candidate
		want    int
		wantSub string
	}{
		{
			// 状态守卫放行：请求真的走到了注册机脚本。用本地假接码服务，
			// 不依赖外网；空收件箱会取信超时，结果作为 failed 返回 200。
			// 关键是**不是** 409/400 这类守卫拒绝。
			name: "pending account is allowed past the state guards",
			in: deploysignupstore.Candidate{
				Email: "pending@icloud.com", MailboxURL: fakeMailboxURL,
				Status: deploysignupstore.Registered,
			},
			want: http.StatusOK,
		},
		{
			name: "verified account is refused",
			in: deploysignupstore.Candidate{
				Email: "verified@icloud.com", MailboxURL: mail,
				Status: deploysignupstore.Verified, IsVerified: true,
			},
			want: http.StatusConflict, wantSub: "不需要重发",
		},
		{
			name: "account with a live session is refused",
			in: deploysignupstore.Candidate{
				Email: "sess@icloud.com", MailboxURL: mail,
				Status: deploysignupstore.Registered, SessionCookie: "hive=x",
			},
			want: http.StatusConflict, wantSub: "不需要重发",
		},
		{
			name: "running account is refused",
			in: deploysignupstore.Candidate{
				Email: "running@icloud.com", MailboxURL: mail,
				Status: deploysignupstore.Running,
			},
			want: http.StatusConflict, wantSub: "注册进行中",
		},
		{
			name: "candidate without a mailbox is refused",
			in: deploysignupstore.Candidate{
				Email: "nomail@icloud.com", Status: deploysignupstore.Registered,
			},
			want: http.StatusBadRequest, wantSub: "接码地址",
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
				"/api/deploy/candidates/"+added.ID+"/resend-verification", strings.NewReader("{}"))
			req.SetPathValue("id", added.ID)
			s.apiDeployResendVerification(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if tc.wantSub != "" && !strings.Contains(rec.Body.String(), tc.wantSub) {
				t.Fatalf("body %q does not mention %q", rec.Body.String(), tc.wantSub)
			}
			// 被拒绝的候选不能被改动
			if tc.want == http.StatusConflict || tc.want == http.StatusBadRequest {
				got, _ := m.store.Get(added.ID)
				if !got.LastResendAt.IsZero() || got.RegisterAttemptAt != added.RegisterAttemptAt {
					t.Fatalf("refused resend mutated the candidate: %+v", got)
				}
			}
		})
	}
}

// 校验要求：未知 id 是 404，不是 500，避免 UI 把"删掉了"报成服务器故障。
func TestDeployResendUnknownID(t *testing.T) {
	s, err := New(t.TempDir(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/deploy/candidates/nope/resend-verification", strings.NewReader("{}"))
	req.SetPathValue("id", "nope")
	s.apiDeployResendVerification(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}
