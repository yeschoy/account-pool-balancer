package thregister

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGitHubNetworkFailuresIdentifyStageWithoutSecrets(t *testing.T) {
	for _, tc := range []struct {
		name, proxy, want string
		cause             error
	}{
		{"socks-http-rejection", "socks5://private-user:private-password@proxy.invalid:5000", "SOCKS5 握手收到 HTTP 响应", &net.OpError{Op: "socks connect", Net: "tcp", Err: errors.New("unexpected protocol version 72")}},
		{"connect-forbidden", "http://private-user:private-password@proxy.invalid:5000", "代理 CONNECT 请求被拒绝", errors.New("Forbidden ")},
		{"dns", "", "DNS 解析失败", &net.DNSError{Name: "private-host.invalid", Err: "no such host"}},
		{"query-is-not-cause", "http://proxy.invalid:5000", "网络传输失败", &url.Error{Op: "POST", URL: "https://example.invalid/?state=forbidden-private-state", Err: io.ErrUnexpectedEOF}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flow, err := New(Config{ProxyURL: tc.proxy})
			if err != nil {
				t.Fatal(err)
			}
			defer flow.Close()
			flow.client.Transport = githubMockTransport(func(r *http.Request) (*http.Response, error) { return nil, tc.cause })
			_, err = flow.oauthRequest(context.Background(), "POST", DefaultBase+"/api/auth/oauth-url?state=private-state", nil, "")
			if err == nil || !strings.Contains(err.Error(), "TokenHarbor OAuth入口 POST") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("missing sanitized phase/cause: %v", err)
			}
			for _, secret := range []string{"private-user", "private-password", "private-host", "private-state", "example.invalid"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("diagnostic leaked URL/proxy detail")
				}
			}
		})
	}
}
