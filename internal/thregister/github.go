package thregister

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const OAuthUserAgent = "account-pool-balancer/tokenharbor-oauth"
const githubBase = "https://github.com"

// GitHubCredentials and Result contain secrets. Never project either to logs.
type GitHubCredentials struct {
	Login, Password, TOTPSecret, ProxyURL string
}

func (GitHubCredentials) String() string   { return "[REDACTED]" }
func (GitHubCredentials) GoString() string { return "[REDACTED]" }

var githubLoginRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)

func ValidateGitHub(login, password, secret string) error {
	if !githubLoginRE.MatchString(login) || password == "" {
		return errors.New("GitHub 账号或密码格式无效")
	}
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil || len(key) < 10 {
		return errors.New("GitHub 2FA 密钥必须为有效 Base32")
	}
	return nil
}

// oauthTransport uses the real product identity, not a fabricated browser.
type oauthTransport struct {
	*http.Transport
	userAgent string
}

func (t *oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", t.userAgent)
	return t.Transport.RoundTrip(r)
}

func githubTOTP(secret string, now time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", errors.New("GitHub 2FA 密钥无效")
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	m := hmac.New(sha1.New, key)
	_, _ = m.Write(counter[:])
	digest := m.Sum(nil)
	offset := digest[len(digest)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(digest[offset:offset+4])&0x7fffffff)%1000000), nil
}

// RunGitHub keeps the entire OAuth journey (including callback and finish) on
// the saved IPv4 route. Only after it completes does f use its independent
// TokenHarbor route. A proxy must itself provide IPv4 egress: tcp4 only controls
// the local connection to the proxy, not that proxy's remote address family.
func (f *Flow) RunGitHub(ctx context.Context, c GitHubCredentials, res Result) (Result, error) {
	if err := ValidateGitHub(c.Login, c.Password, c.TOTPSecret); err != nil {
		return res, err
	}
	// The OAuth product identity belongs to the authorization hops only. An
	// earlier version installed it on the flow's own transport, which then
	// stamped "account-pool-balancer/tokenharbor-oauth" on every later
	// tokenharbor.ai request this account made (key creation, dashboard reads,
	// plan checks) — a name no browser sends, on the site that issued the
	// session. The site's own requests keep the browser identity the flow
	// already presents; only the authorization transport is relabelled.
	f.client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 6 || !sameOrigin(req.URL, mustURL(f.cfg.base())) {
			return http.ErrUseLastResponse
		}
		return nil
	}
	if res.OAuthComplete && HasSession(res.Cookies) {
		return f.Resume(ctx, res)
	}
	authCfg := f.cfg
	authCfg.ProxyURL, authCfg.UserAgent = c.ProxyURL, OAuthUserAgent
	authCfg.Checkpoint = nil
	auth, err := New(authCfg)
	if err != nil {
		return res, err
	}
	defer auth.Close()
	tr := auth.client.Transport.(*oauthTransport).Transport
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	tr.DialContext = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp4", addr)
	}
	if f.cfg.oauthRoundTripper != nil {
		auth.client.Transport = f.cfg.oauthRoundTripper
	}
	// Manual redirects are essential: no credential-bearing cross-origin POST
	// replay, no arbitrary callback host and no automatic retries on mutation.
	auth.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	auth.SeedCookies(res.Cookies)
	seedGitHubCookies(auth, res.GitHubCookies)
	checkpoint := func() error {
		res.Cookies = auth.Session()
		res.GitHubCookies = snapshotGitHub(auth)
		email, login, providerToken, refreshToken := oauthProfile(res.Cookies)
		if login != "" && !strings.EqualFold(login, c.Login) {
			return errors.New("GitHub 会话账号与导入账号不一致，未关联该会话")
		}
		if email != "" {
			res.Email = email
		}
		// Supabase refreshes can omit provider tokens. Keep the original grant
		// separately rather than losing it on the next TH cookie rotation.
		if providerToken != "" {
			res.GitHubToken = providerToken
		}
		if refreshToken != "" {
			res.GitHubRefreshToken = refreshToken
		}
		if f.cfg.Checkpoint != nil {
			return f.cfg.Checkpoint(res)
		}
		return nil
	}
	var page oauthPage
	if HasSession(res.Cookies) {
		page, err = auth.oauthRequest(ctx, "GET", authCfg.base()+"/oauth/finish?next=%2Fdashboard", nil, "")
	} else {
		f.cfg.step("GitHub 授权", "固定 IPv4 出口，正常授权；遇到设备验证或风控即停止")
		page, err = auth.githubAuthorize(ctx, c, checkpoint)
	}
	if saveErr := checkpoint(); saveErr != nil {
		return res, saveErr
	}
	if err != nil {
		return res, err
	}
	// Once a session has been issued, the finish page itself may redirect.
	for i := 0; page.redirect != "" && i < 6; i++ {
		target := page.url.ResolveReference(mustURL(page.redirect))
		if !sameOrigin(target, mustURL(authCfg.base())) {
			return res, errors.New("OAuth 回调离开 TokenHarbor，已停止")
		}
		page, err = auth.oauthRequest(ctx, "GET", target.String(), nil, "")
		if saveErr := checkpoint(); saveErr != nil {
			return res, saveErr
		}
		if err != nil {
			return res, err
		}
	}
	if page.redirect != "" {
		return res, errors.New("OAuth 回调跳转次数过多")
	}
	if page.url.Path == "/oauth/finish" {
		f.cfg.step("完成 OAuth", "提交官网 finish 表单，不开启免费模型数据授权")
		if err = auth.finishOAuth(ctx, page); err != nil {
			if saveErr := checkpoint(); saveErr != nil {
				return res, saveErr
			}
			return res, err
		}
	} else if !strings.HasPrefix(page.url.Path, "/dashboard") {
		return res, errors.New("OAuth 未进入 finish 或 dashboard；请人工核对登录/验证状态")
	}
	if !HasSession(auth.Session()) || !strings.Contains(res.Email, "@") {
		return res, ErrSessionRejected
	}
	res.OAuthComplete = true
	if err = checkpoint(); err != nil {
		return res, err
	}
	f.cfg.step("TokenHarbor 会话", "OAuth 已完成；后续使用独立绑定出口")
	return f.Resume(ctx, res)
}

type oauthPage struct {
	url      *url.URL
	body     []byte
	redirect string
	status   int
	media    string
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host) && a.User == nil
}

func (f *Flow) oauthRequest(ctx context.Context, method, target string, body io.Reader, contentType string) (oauthPage, error) {
	u, err := url.Parse(target)
	if err != nil || u.Host == "" || u.User != nil {
		return oauthPage{}, errors.New("OAuth 地址无效")
	}
	trace, networkPhase := oauthRequestTrace(f.cfg.ProxyURL != "")
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), method, target, body)
	if err != nil {
		return oauthPage{}, errors.New("OAuth 请求格式无效")
	}
	req.Header.Set("User-Agent", OAuthUserAgent)
	req.Header.Set("Accept", "text/html,application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		req.Header.Set("Referer", target)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return oauthPage{}, fmt.Errorf("%s %s 请求失败：%s（最近阶段：%s）；结果待核对，未重试或切换出口", oauthRequestStage(u), method, oauthNetworkReason(err, f.cfg.ProxyURL != ""), networkPhase())
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if limited := registrationRateLimit(resp, ""); limited != nil {
		return oauthPage{}, fmt.Errorf("%s %s: %w", oauthRequestStage(u), method, limited)
	}
	if err != nil {
		return oauthPage{}, fmt.Errorf("%s %s 响应读取失败：%s（HTTP %d；阶段：读取响应体）；结果待核对，未重试", oauthRequestStage(u), method, oauthNetworkReason(err, f.cfg.ProxyURL != ""), resp.StatusCode)
	}
	if len(raw) > maxBody {
		return oauthPage{}, fmt.Errorf("%s %s 响应过大；结果待核对，未重试", oauthRequestStage(u), method)
	}
	if resp.StatusCode >= 400 {
		return oauthPage{}, fmt.Errorf("%s %s 上游请求被拒绝：HTTP %d；未重试", oauthRequestStage(u), method, resp.StatusCode)
	}
	media := "未知内容类型"
	switch strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])) {
	case "text/html":
		media = "HTML"
	case "application/json":
		media = "JSON"
	case "text/plain":
		media = "纯文本"
	}
	page := oauthPage{url: u, body: raw, status: resp.StatusCode, media: media}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		page.redirect = resp.Header.Get("Location")
	}
	return page, nil
}

// Neither the full request URL (OAuth code/state) nor the raw network error
// (proxy credentials/reflected values) is safe to log. Report allowlisted facts.
func oauthRequestStage(u *url.URL) string {
	if u.Hostname() == "github.com" {
		switch u.Path {
		case "/login":
			return "GitHub 登录页"
		case "/session":
			return "GitHub 密码登录"
		case "/login/oauth/authorize":
			return "GitHub OAuth授权页"
		case "/sessions/two-factor", "/sessions/two-factor/app":
			return "GitHub 2FA 页面"
		case "/sessions/verified-device":
			return "GitHub 设备验证页"
		default:
			return "GitHub OAuth页面"
		}
	}
	if u.Hostname() == "auth.tokenharbor.ai" || strings.HasSuffix(u.Hostname(), ".supabase.co") {
		return "Supabase OAuth"
	}
	switch u.Path {
	case "/api/auth/oauth-url":
		return "TokenHarbor OAuth入口"
	case "/oauth/finish":
		return "TokenHarbor finish"
	case "/auth/callback", "/api/auth/callback":
		return "TokenHarbor OAuth回调"
	default:
		return "TokenHarbor OAuth页面"
	}
}

func oauthNetworkReason(err error, usingProxy bool) string {
	for {
		requestError, ok := err.(*url.Error)
		if !ok {
			break
		}
		err = requestError.Err
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "DNS 解析失败"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "网络请求超时（连接、TLS 或等待响应）"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "请求已取消"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "响应流提前结束（unexpected EOF）"
	case errors.Is(err, io.EOF):
		return "连接提前关闭（EOF）"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "连接被拒绝"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return "网络不可达"
	case errors.Is(err, syscall.ECONNRESET):
		return "连接被重置"
	}
	// Inspect known library diagnostics only to select a fixed, nonsecret
	// label. In particular version72 ('H') was a proxy HTTP403, not a GH ban.
	message := strings.ToLower(err.Error())
	switch {
	case usingProxy && strings.Contains(message, "unexpected protocol version 72"):
		return "SOCKS5 握手收到 HTTP 响应，代理协议或入口准入异常（尚未建立官网连接）"
	case usingProxy && (strings.Contains(message, "no acceptable authentication methods") || strings.Contains(message, "authentication failed")):
		return "代理认证被拒绝（尚未建立官网连接）"
	case usingProxy && strings.Contains(message, "forbidden"):
		return "代理 CONNECT 请求被拒绝（Forbidden，尚未建立官网连接）"
	case strings.Contains(message, "http2:"):
		return "HTTP/2 连接或流异常"
	case strings.Contains(message, "x509:"), strings.Contains(message, "tls:"):
		return "TLS 握手或证书校验失败"
	default:
		return "网络传输失败"
	}
}

func (f *Flow) githubAuthorize(ctx context.Context, c GitHubCredentials, checkpoint func() error) (oauthPage, error) {
	start, err := f.oauthRequest(ctx, "POST", f.cfg.base()+"/api/auth/oauth-url", strings.NewReader(`{"provider":"github","next":"/dashboard","invite":null}`), "application/json")
	if err != nil {
		return oauthPage{}, err
	}
	var out struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(start.body, &out) != nil {
		return oauthPage{}, errors.New("OAuth 入口响应无法解析")
	}
	u, err := url.Parse(out.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Hostname() != "auth.tokenharbor.ai" && !strings.HasSuffix(u.Hostname(), ".supabase.co")) || u.Port() != "" || u.Fragment != "" || u.Path != "/auth/v1/authorize" || u.Query().Get("provider") != "github" {
		return oauthPage{}, errors.New("OAuth 入口不是受信任的 GitHub/Supabase 地址")
	}
	supabaseOrigin := u.Scheme + "://" + u.Host
	allowed := func(next *url.URL) bool {
		if next.User != nil {
			return false
		}
		if sameOrigin(next, mustURL(f.cfg.base())) {
			return true
		}
		return next.Scheme == "https" && (next.Host == "github.com" || next.Scheme+"://"+next.Host == supabaseOrigin)
	}
	var page oauthPage
	passwordSent, totpSent, grantSent := false, false, false
	for hop := 0; hop < 20; hop++ {
		if !allowed(u) {
			return page, errors.New("OAuth 跳转到未授权站点，已停止")
		}
		if u.Host == "github.com" {
			for _, scope := range strings.FieldsFunc(u.Query().Get("scope"), func(r rune) bool { return r == ',' || r == ' ' }) {
				if scope != "read:user" && scope != "user:email" {
					return page, errors.New("GitHub 请求了额外权限，需人工授权")
				}
			}
		}
		page, err = f.oauthRequest(ctx, "GET", u.String(), nil, "")
		if saveErr := checkpoint(); saveErr != nil {
			return page, saveErr
		}
		if err != nil {
			return page, err
		}
		if page.redirect != "" {
			u = u.ResolveReference(mustURL(page.redirect))
			continue
		}
		if sameOrigin(u, mustURL(f.cfg.base())) {
			return page, nil
		}
		if u.Host != "github.com" {
			return page, errors.New("OAuth 身份服务未返回回调，需人工核对")
		}
		forms := parseOAuthForms(string(page.body))
		var form *oauthForm
		for i := range forms {
			candidate := &forms[i]
			action, e := u.Parse(candidate.action)
			if e != nil || !sameOrigin(action, mustURL(githubBase)) || candidate.method != "post" {
				continue
			}
			switch action.Path {
			case "/session":
				if candidate.fields.Has("password") && !passwordSent {
					form = candidate
				}
			case "/sessions/two-factor":
				if candidate.fields.Has("app_otp") && !totpSent {
					form = candidate
				}
			case "/login/oauth/authorize":
				if candidate.fields.Has("authorize") && !grantSent {
					form = candidate
				}
			}
			if form != nil {
				break
			}
		}
		if form == nil {
			return page, fmt.Errorf("%s 本地表单解析无法继续（来源：GitHub；HTTP %d；%s；表单数 %d；已提交密码=%t、2FA=%t、授权=%t）；不能据此判定密码错误或风控，会话已保存，未重复提交或绕过验证", oauthRequestStage(u), page.status, page.media, len(forms), passwordSent, totpSent, grantSent)
		}
		if form.fields.Get("authenticity_token") == "" {
			return page, errors.New("GitHub 表单缺少 CSRF 令牌")
		}
		action, _ := u.Parse(form.action)
		switch action.Path {
		case "/session":
			f.cfg.step("GitHub 密码登录", "提交一次，不自动重试")
			passwordSent = true
			form.fields.Set("login", c.Login)
			form.fields.Set("password", c.Password)
		case "/sessions/two-factor":
			f.cfg.step("GitHub 2FA", "提交一次，不自动重试")
			totpSent = true
			code, e := githubTOTP(c.TOTPSecret, time.Now())
			if e != nil {
				return page, e
			}
			form.fields.Set("app_otp", code)
		case "/login/oauth/authorize":
			f.cfg.step("GitHub OAuth授权", "提交一次，不自动重试")
			grantSent = true
		}
		page, err = f.oauthRequest(ctx, "POST", action.String(), strings.NewReader(form.fields.Encode()), "application/x-www-form-urlencoded")
		if saveErr := checkpoint(); saveErr != nil {
			return page, saveErr
		}
		if err != nil {
			return page, err
		}
		if page.redirect == "" {
			return page, errors.New("GitHub 未接受登录/2FA/授权表单；未自动重试")
		}
		u = action.ResolveReference(mustURL(page.redirect))
	}
	return page, errors.New("OAuth 跳转次数过多，已停止")
}

func (f *Flow) finishOAuth(ctx context.Context, page oauthPage) error {
	form, err := parseSignupForm(string(page.body))
	if err != nil {
		return errors.New("finish 页面缺少动态 Action ID/Key，未提交")
	}
	var buf bytes.Buffer
	mw, err := newBrowserForm(&buf)
	if err != nil {
		return errors.New("finish 请求格式无效")
	}
	fields := [][2]string{
		{"_1_$ACTION_REF_1", ""},
		{"_1_$ACTION_1:0", fmt.Sprintf(`{"id":%q,"bound":"$@1"}`, form.actionID)},
		{"_1_$ACTION_1:1", `["$undefined"]`},
		{"_1_$ACTION_KEY", form.actionKey},
		{"_1_timezone", f.cfg.timezone()},
		{"_1_next", "/dashboard"},
		{"_1_invite_code", ""},
		{"0", `["$undefined","$K1"]`},
	}
	for _, field := range fields {
		if err := mw.WriteField(field[0], field[1]); err != nil {
			return err
		}
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", page.url.String(), &buf)
	if err != nil {
		return errors.New("finish 请求格式无效")
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "text/x-component")
	req.Header.Set("Next-Action", form.actionID)
	req.Header.Set("User-Agent", OAuthUserAgent)
	req.Header.Set("Origin", f.cfg.base())
	req.Header.Set("Referer", page.url.String())
	resp, err := f.client.Do(req)
	if err != nil {
		return errors.New("finish 请求结果不确定，未自动重试；请核对官网")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if limited := registrationRateLimit(resp, ""); limited != nil {
		return limited
	}
	if err != nil || len(raw) > maxBody {
		return errors.New("finish 响应不完整，未重试")
	}
	message, captcha := authActionError(raw)
	if message != "" || captcha {
		return errors.New("finish 表单被拒绝或需要人工验证；未重试")
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("finish 被拒绝：HTTP %d", resp.StatusCode)
	}
	// The website must confirm the dashboard redirect; HTTP200 alone is not success.
	redirect := strings.Split(resp.Header.Get("X-Action-Redirect"), ";")[0]
	if redirect == "" {
		redirect = resp.Header.Get("Location")
	}
	if redirect != "" {
		u, err := page.url.Parse(redirect)
		if err == nil && sameOrigin(u, mustURL(f.cfg.base())) && strings.HasPrefix(u.Path, "/dashboard") {
			return nil
		}
	}
	if bytes.Contains(raw, []byte("NEXT_REDIRECT;")) && bytes.Contains(raw, []byte(";/dashboard;")) {
		return nil
	}
	return errors.New("finish 未确认完成，请人工核对；未自动重试")
}

type oauthForm struct {
	action, method string
	fields         url.Values
}

var oauthFormRE = regexp.MustCompile(`(?is)<form\b([^>]*)>(.*?)</form\s*>`)
var oauthInputRE = regexp.MustCompile(`(?is)<(input|button)\b([^>]*)>`)
var oauthAttrRE = regexp.MustCompile(`(?is)([\w$:-]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

func oauthAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range oauthAttrRE.FindAllStringSubmatch(s, -1) {
		value := m[2]
		if value == "" {
			value = m[3]
		}
		if value == "" {
			value = m[4]
		}
		out[strings.ToLower(m[1])] = html.UnescapeString(value)
	}
	return out
}
func parseOAuthForms(s string) []oauthForm {
	var out []oauthForm
	for _, match := range oauthFormRE.FindAllStringSubmatch(s, -1) {
		attrs := oauthAttrs(match[1])
		form := oauthForm{action: attrs["action"], method: strings.ToLower(attrs["method"]), fields: url.Values{}}
		for _, input := range oauthInputRE.FindAllStringSubmatch(match[2], -1) {
			a := oauthAttrs(input[2])
			name := a["name"]
			if name == "" || a["type"] == "checkbox" || a["type"] == "radio" || name == "deny" {
				continue
			}
			form.fields.Add(name, a["value"])
		}
		out = append(out, form)
	}
	return out
}

func seedGitHubCookies(f *Flow, cookies []NamedCookie) {
	u := mustURL(githubBase)
	for _, c := range cookies {
		if c.Domain != "" && strings.TrimPrefix(c.Domain, ".") != u.Hostname() {
			continue
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		f.jar.SetCookies(u, []*http.Cookie{{Name: c.Name, Value: c.Value, Path: path, Secure: true, HttpOnly: c.HTTPOnly, Expires: c.Expires}})
	}
}
func snapshotGitHub(f *Flow) []NamedCookie {
	var out []NamedCookie
	for _, c := range f.jar.Cookies(mustURL(githubBase)) {
		out = append(out, NamedCookie{Name: c.Name, Value: c.Value, Domain: "github.com", Path: "/", Secure: true, HTTPOnly: true})
	}
	return out
}
func oauthProfile(cookies []NamedCookie) (string, string, string, string) {
	var chunks []NamedCookie
	for _, c := range cookies {
		if IsSessionCookie(c.Name) {
			chunks = append(chunks, c)
		}
	}
	index := func(name string) int { _, n, _ := strings.Cut(name, "."); i, _ := strconv.Atoi(n); return i }
	sort.Slice(chunks, func(i, j int) bool { return index(chunks[i].Name) < index(chunks[j].Name) })
	var joined strings.Builder
	for _, c := range chunks {
		joined.WriteString(c.Value)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(strings.TrimPrefix(joined.String(), "base64-"), "="))
	if err != nil {
		return "", "", "", ""
	}
	var session struct {
		ProviderToken        string `json:"provider_token"`
		ProviderRefreshToken string `json:"provider_refresh_token"`
		User                 struct {
			Email    string `json:"email"`
			Metadata struct {
				Login string `json:"preferred_username"`
				Name  string `json:"user_name"`
			} `json:"user_metadata"`
		} `json:"user"`
	}
	if json.Unmarshal(raw, &session) != nil || !strings.Contains(session.User.Email, "@") {
		return "", "", "", ""
	}
	login := session.User.Metadata.Login
	if login == "" {
		login = session.User.Metadata.Name
	}
	return session.User.Email, login, session.ProviderToken, session.ProviderRefreshToken
}
