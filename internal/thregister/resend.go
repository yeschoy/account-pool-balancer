package thregister

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ResendVerification uses the signed-in dashboard's current server action.
// Action IDs change on deployment; never guess an API or replay signup/login.
func (f *Flow) ResendVerification(ctx context.Context, beforeSend func() error) error {
	if !HasSession(f.Session()) {
		return errors.New("没有已保存的登录会话，无法重发验证码")
	}
	action, err := f.verificationAction(ctx)
	if err != nil {
		return err
	}
	if beforeSend != nil {
		if err := beforeSend(); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.cfg.base()+"/dashboard", strings.NewReader("[]"))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Accept", "text/x-component")
	req.Header.Set("Next-Action", action)
	req.Header.Set("Origin", f.cfg.base())
	req.Header.Set("Referer", f.cfg.base()+"/dashboard")
	f.applyIdentity(req)
	raw, err := f.verificationRequest(req)
	if err != nil {
		return err
	}
	// A successful HTTP response alone does not establish that mail was sent.
	if message, _ := authActionError(raw); message != "" {
		return errors.New("官网拒绝重发验证码，请在官网核对；未自动重试")
	}
	for _, line := range strings.Split(string(raw), "\n") {
		id, payload, ok := strings.Cut(line, ":")
		if !ok || id != "1" {
			continue
		}
		var result struct {
			OK      bool `json:"ok"`
			Success bool `json:"success"`
		}
		if json.Unmarshal([]byte(payload), &result) == nil && (result.OK || result.Success) {
			return nil
		}
	}
	return errors.New("官网未确认验证码已发送，请核对收件箱；未自动重试")
}

var verificationScript = regexp.MustCompile(`<script\b[^>]*\bsrc="([^"<>]+\.js(?:\?[^"<>]*)?)"`)
var verificationServerAction = regexp.MustCompile(`createServerReference\)\("([0-9a-f]{40,44})",[^;\n]{0,300}?"(?:resendVerificationEmail|resendEmailVerification|resendVerification|sendVerificationEmail)(?:Action)?"\)`)

func (f *Flow) verificationAction(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.cfg.base()+"/dashboard", nil)
	req.Header.Set("Accept", "text/html")
	f.applyIdentity(req)
	page, err := f.verificationRequest(req)
	if err != nil {
		return "", err
	}
	base := mustURL(f.cfg.base())
	seen := map[string]bool{}
	// Only same-origin scripts explicitly shipped with this dashboard are read.
	// Refuse schema drift rather than inventing a mutation endpoint/action ID.
	for _, match := range verificationScript.FindAllSubmatch(page, 32) {
		u, err := url.Parse(string(match[1]))
		if err != nil {
			continue
		}
		u = base.ResolveReference(u)
		if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || !strings.HasPrefix(u.Path, "/_next/static/") || seen[u.String()] {
			continue
		}
		seen[u.String()] = true
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		f.applyIdentity(req)
		script, err := f.verificationRequest(req)
		if err != nil {
			return "", err
		}
		if action := verificationServerAction.FindSubmatch(script); action != nil {
			return string(action[1]), nil
		}
	}
	return "", errors.New("未识别官网当前的重发验证动作，请在官网手动重发；未重新注册")
}

func (f *Flow) verificationRequest(req *http.Request) ([]byte, error) {
	client := *f.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("验证码请求网络失败，结果待核对；未自动重试或切换出口")
	}
	defer resp.Body.Close()
	if err := registrationRateLimit(resp, ""); err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("验证码请求返回 HTTP %d，请核对登录状态；未自动重试", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(raw) > maxBody {
		return nil, errors.New("验证码响应不完整或过大，结果待核对；未自动重试")
	}
	return raw, nil
}
