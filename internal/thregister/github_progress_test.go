package thregister

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"strings"
	"testing"
)

func TestGitHubNetworkTraceReportsLastObservedPhase(t *testing.T) {
	trace, phase := oauthRequestTrace(true)
	trace.DNSStart(httptrace.DNSStartInfo{Host: "private-host"})
	if phase() != "代理 DNS 解析" {
		t.Fatal("missing DNS phase")
	}
	trace.ConnectStart("tcp4", "private-address")
	if phase() != "代理 TCP 连接" {
		t.Fatal("missing proxy connection phase")
	}
	trace.ConnectDone("tcp4", "private-address", nil)
	if !strings.Contains(phase(), "代理协商") {
		t.Fatal("missing SOCKS/CONNECT phase")
	}
	trace.TLSHandshakeStart()
	if phase() != "TLS 握手" {
		t.Fatal("missing TLS phase")
	}
	trace.GotConn(httptrace.GotConnInfo{})
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	if phase() != "等待官网响应头" {
		t.Fatal("missing response wait phase")
	}
	trace.GotFirstResponseByte()
	if phase() != "读取官网响应头" {
		t.Fatal("missing header read phase")
	}
}

func TestGitHubEOFDiagnosticsRetainSourceAndDoNotReplay(t *testing.T) {
	calls := 0
	flow, _ := New(Config{})
	defer flow.Close()
	flow.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		trace := httptrace.ContextClientTrace(r.Context())
		trace.GotConn(httptrace.GotConnInfo{})
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		return nil, io.EOF
	})
	_, err := flow.oauthRequest(context.Background(), "POST", DefaultBase+"/api/auth/oauth-url?state=private-state", strings.NewReader("private-body"), "application/json")
	if err == nil || calls != 1 {
		t.Fatal("missing error or POST replay")
	}
	for _, want := range []string{"TokenHarbor OAuth入口 POST", "连接提前关闭（EOF）", "等待官网响应头", "未重试"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing phase/source: %v", err)
		}
	}
	for _, secret := range []string{"private-state", "private-body"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal("network diagnostic leaked a secret")
		}
	}
}

func TestGitHubHTTPRejectionIdentifiesActualUpstream(t *testing.T) {
	for _, tc := range []struct{ target, stage string }{{DefaultBase + "/api/auth/oauth-url", "TokenHarbor OAuth入口"}, {"https://github.com/login/oauth/authorize?state=private-state", "GitHub OAuth授权页"}, {"https://auth.tokenharbor.ai/auth/v1/authorize", "Supabase OAuth"}} {
		t.Run(tc.stage, func(t *testing.T) {
			flow, _ := New(Config{})
			defer flow.Close()
			calls := 0
			flow.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				return githubMockResponse(r, 403, "private-body"), nil
			})
			_, err := flow.oauthRequest(context.Background(), "GET", tc.target, nil, "")
			if err == nil || calls != 1 || !strings.Contains(err.Error(), tc.stage) || !strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), "private-") {
				t.Fatalf("unsafe or ambiguous upstream error: %v", err)
			}
		})
	}
}

func TestGitHubRepeatedLoginPageIsLocalParsingFailureNotPasswordVerdict(t *testing.T) {
	posts := 0
	transport := githubMockTransport(func(r *http.Request) (*http.Response, error) {
		resp := githubMockResponse(r, 200, "")
		switch r.URL.Host {
		case "tokenharbor.ai":
			resp.Body = io.NopCloser(strings.NewReader(`{"url":"https://auth.tokenharbor.ai/auth/v1/authorize?provider=github"}`))
		case "auth.tokenharbor.ai":
			resp.StatusCode = 302
			resp.Header.Set("Location", "https://github.com/login")
		case "github.com":
			if r.Method == "POST" {
				posts++
				resp.StatusCode = 302
				resp.Header.Set("Location", "/login/oauth/authorize?state=private-state")
			} else {
				resp.Header.Set("Content-Type", "text/html; reflected=private-header")
				resp.Body = io.NopCloser(strings.NewReader(`<form method="post" action="/session"><input name="authenticity_token" value="private-csrf"><input name="login"><input name="password"></form>`))
			}
		default:
			t.Fatal("unexpected authority")
		}
		return resp, nil
	})
	flow, _ := New(Config{oauthRoundTripper: transport})
	defer flow.Close()
	_, err := flow.RunGitHub(context.Background(), GitHubCredentials{Login: "mock-user", Password: "private-password", TOTPSecret: "GEZDGNBVGY3TQOJQ"}, Result{})
	if err == nil || posts != 1 {
		t.Fatal("unexpected authorization success or repeated password submission")
	}
	for _, want := range []string{"GitHub OAuth授权页", "本地表单解析", "来源：GitHub", "HTTP 200", "HTML", "表单数 1", "已提交密码=true", "2FA=false", "授权=false"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing parsing evidence %s: %v", want, err)
		}
	}
	for _, secret := range []string{"private-state", "private-csrf", "private-header", "private-password"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatal(fmt.Sprint("unsafe parsing diagnostic: ", secret))
		}
	}
}
