package thmailbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"

	"account-pool-balancer/internal/thregister"
)

func codeMailbox(u *url.URL) bool {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "mailboxes" && parts[4] == "code"
}

// CodeMailboxEmail exposes only the mailbox identity, never the URL/key.
func CodeMailboxEmail(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !codeMailbox(u) {
		return ""
	}
	return strings.Split(strings.Trim(u.Path, "/"), "/")[3]
}

func validateCodeMailbox(u *url.URL) error {
	if u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("JSON 接码接口必须使用无用户信息和fragment的 HTTPS 地址")
	}
	address := CodeMailboxEmail(u.String())
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Name != "" || parsed.Address != address {
		return errors.New("JSON 接码接口中的邮箱无效")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["key"]) != 1 || strings.TrimSpace(query.Get("key")) == "" {
		return errors.New("JSON 接码接口必须包含唯一的 key 参数")
	}
	return nil
}

type codeResponse struct {
	Success         *bool  `json:"success"`
	Code            string `json:"code"`
	Retryable       bool   `json:"retryable"`
	Email           string `json:"email"`
	ReceivedAt      string `json:"received_at"`
	Subject         string `json:"subject"`
	From            string `json:"from"`
	Text            string `json:"text"`
	HTML            string `json:"html"`
	Body            string `json:"body"`
	Content         string `json:"content"`
	VerificationURL string `json:"verification_url"`
	Link            string `json:"link"`
}

func mailboxRejected(message string) error {
	return fmt.Errorf("%w: %s", thregister.ErrMailboxRejected, message)
}

func parseCodeMessages(raw []byte, email string) ([]thregister.Message, error) {
	var out codeResponse
	if json.Unmarshal(raw, &out) != nil || out.Success == nil {
		return nil, mailboxRejected("JSON 接码接口返回格式无法识别")
	}
	if !*out.Success {
		if out.Retryable && (out.Code == "no_code" || out.Code == "no_mail") {
			return nil, nil
		}
		if out.Retryable {
			return nil, errors.New("JSON 接码接口暂不可用，等待下一次检查")
		}
		return nil, mailboxRejected("JSON 接码接口拒绝请求，请核对接码地址权限或有效期")
	}
	if out.Email != "" && !strings.EqualFold(strings.TrimSpace(out.Email), email) {
		return nil, mailboxRejected("JSON 接码接口返回了其他邮箱的数据")
	}
	text := html.UnescapeString(strings.Join([]string{out.Code, out.VerificationURL, out.Link, out.Text, out.HTML, out.Body, out.Content}, "\n"))
	link := sharedLinkRE.FindString(text)
	if link == "" {
		// The same mailbox also receives GitHub login/OAuth notifications.
		// A successful extraction of one of those is not a mailbox failure.
		if strings.Contains(strings.ToLower(out.Subject+"\n"+out.From), "github") {
			return nil, nil
		}
		return nil, mailboxRejected("接码接口未提供 TokenHarbor 验证链接；数字验证码不能代替邮箱验证")
	}
	received := parseTime(out.ReceivedAt)
	if received.IsZero() {
		return nil, mailboxRejected("接码接口缺少有效收信时间，无法确认验证链接是否属于本次注册")
	}
	id := sha256.Sum256([]byte(email + "\n" + out.ReceivedAt + "\n" + link))
	return []thregister.Message{{ID: hex.EncodeToString(id[:]), ReceivedAt: received, Subject: out.Subject, From: out.From, Text: link}}, nil
}

func (c *Client) codeMessages(ctx context.Context) ([]thregister.Message, error) {
	if err := Validate(c.URL); err != nil {
		return nil, mailboxRejected("JSON 接码接口地址无效")
	}
	u, _ := url.Parse(c.URL) // Validate above has already checked the URL.
	query := u.Query()
	if !query.Has("keyword") {
		query.Set("keyword", "tokenharbor")
	}
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, mailboxRejected("JSON 接码接口请求格式无效")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("User-Agent", "account-pool-balancer/mailbox")
	client := *c.HTTPClient
	// The URL contains a write-only mailbox key. Never follow a redirect that
	// could forward it, or reveal it by returning a raw *url.Error/message.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("JSON 接码接口网络读取失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 410 {
			return nil, mailboxRejected(fmt.Sprintf("JSON 接码接口拒绝访问：HTTP %d", resp.StatusCode))
		}
		return nil, fmt.Errorf("JSON 接码接口返回 HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, errors.New("JSON 接码接口响应不完整或过大")
	}
	return parseCodeMessages(raw, CodeMailboxEmail(c.URL))
}
