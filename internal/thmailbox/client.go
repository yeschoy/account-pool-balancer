// Package thmailbox reads forwarding mailboxes of the iCloud HME manager
// (icloud.mida.vip). One line of an operator's account list has the shape
//
//	alias@icloud.com----https://icloud.mida.vip/messages/alias%40icloud.com?token=…
//
// The page is a single-page app; the mail it shows comes from one JSON
// endpoint that takes the alias and the share token from the page's own URL.
// Only the message fields the registration needs are read.
package thmailbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"account-pool-balancer/internal/egressid"
	"net/url"
	"strings"
	"time"

	"account-pool-balancer/internal/thregister"
)

// DefaultTimeout bounds one mailbox read; nil client means this deadline.
const DefaultTimeout = 25 * time.Second

// ParseLine splits "email----mailboxURL". It reports "" for a line without a
// separator, which the caller treats as "no mailbox".
func ParseLine(line string) (email, mailbox string) {
	line = strings.TrimSpace(line)
	i := strings.Index(line, "----")
	if i < 0 {
		return line, ""
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+4:])
}

// Validate checks a mailbox URL shape without contacting it.
func Validate(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("接码页地址无效")
	}
	if sharedMailbox(u) {
		if u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("共享接码页必须为 HTTPS 且不含额外凭据/查询参数")
		}
		return nil
	}
	if u.Hostname() == "" || !strings.Contains(u.Path, "/messages/") {
		return errors.New("接码页地址应是 …/messages/<邮箱>?token=… 的形式")
	}
	if u.Query().Get("token") == "" {
		return errors.New("接码页地址缺少 token")
	}
	return nil
}

// Client reads one mailbox share page.
type Client struct {
	URL        string
	HTTPClient *http.Client
	// Seed selects the identity this reader presents to the mail host. Like
	// the registration it serves, it must look like the same browser, or the
	// mail host sees a different client at exactly the moment the account is
	// being created.
	Seed string
}

// New builds a reader for a mailbox share page.
func New(pageURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{URL: strings.TrimSpace(pageURL), HTTPClient: hc}
}

type messagesResponse struct {
	OK    bool `json:"ok"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Data *struct {
		AliasEmail string `json:"aliasEmail"`
		ShareState string `json:"shareStatus"`
		Emails     []struct {
			ID         any    `json:"id"`
			ReceivedAt string `json:"receivedAt"`
			Subject    string `json:"subject"`
			FromAddr   string `json:"fromAddress"`
			Text       string `json:"textContent"`
		} `json:"emails"`
	} `json:"data"`
}

// Messages returns the mailbox's messages, newest data as the service sends it.
func (c *Client) Messages(ctx context.Context) ([]thregister.Message, error) {
	if c.URL == "" {
		return nil, errors.New("未配置接码页")
	}
	if u, err := url.Parse(c.URL); err == nil && sharedMailbox(u) {
		return c.sharedMessages(ctx)
	}
	api, err := apiURL(c.URL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/json")
	egressid.Apply(req.Header, egressid.For(c.Seed))
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("接码页请求失败: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var out messagesResponse
	if json.Unmarshal(raw, &out) != nil {
		return nil, errors.New("接码页返回无法解析")
	}
	if !out.OK {
		if out.Error != nil && out.Error.Message != "" {
			return nil, errors.New("接码页: " + out.Error.Message)
		}
		return nil, fmt.Errorf("接码页返回失败: HTTP %d", resp.StatusCode)
	}
	if out.Data == nil {
		return nil, errors.New("接码页没有返回数据")
	}
	msgs := make([]thregister.Message, 0, len(out.Data.Emails))
	for _, m := range out.Data.Emails {
		msgs = append(msgs, thregister.Message{
			ID:         fmt.Sprint(m.ID),
			ReceivedAt: parseTime(m.ReceivedAt),
			Subject:    m.Subject,
			From:       m.FromAddr,
			Text:       m.Text,
		})
	}
	return msgs, nil
}

// apiURL turns a share page into its JSON endpoint:
// /messages/<encoded alias>?token=… -> /api/messages?alias_email=…&token=…&range=latest
func apiURL(pageURL string) (string, error) {
	u, err := url.Parse(pageURL)
	if err != nil || u.Host == "" {
		return "", errors.New("接码页地址无效")
	}
	tok := u.Query().Get("token")
	if tok == "" {
		return "", errors.New("接码页地址缺少 token")
	}
	alias := u.Path
	if i := strings.LastIndex(alias, "/messages/"); i >= 0 {
		alias = alias[i+len("/messages/"):]
	} else {
		return "", errors.New("接码页地址没有 /messages/ 段")
	}
	alias, err = url.PathUnescape(alias)
	if err != nil {
		return "", errors.New("接码页地址中的邮箱无法解析")
	}
	q := url.Values{}
	q.Set("alias_email", alias)
	q.Set("token", tok)
	q.Set("range", "latest")
	return u.Scheme + "://" + u.Host + "/api/messages?" + q.Encode(), nil
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
