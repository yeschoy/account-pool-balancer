package telnyxreg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// YaoYiMailbox reads Outlook inboxes through the shared "取件" tool at
// tool.yaoyiyaocoffee.cn.
//
//	POST /api/mailbox-tool/inbox/info  {"shareToken":"..."} → {"data":{"email":"..."}}
//	POST /api/mailbox-tool/inbox/read  {"shareToken":"...","cursor":"..."}
//	     → {"data":{"email":"...","messages":[{"id","sender","subject",
//	                     "receivedAt","text","html",...}],"nextCursor":...}}
//
// shareToken is the "s" value from https://tool.yaoyiyaocoffee.cn/inbox#s=<token>.
type YaoYiMailbox struct {
	BaseURL    string // default https://tool.yaoyiyaocoffee.cn
	ShareToken string
	ListURL    string // optional | ?token=...&range=all form (token taken from it)
	HTTP       *http.Client
	// MatchSubject narrows candidate messages; default is Telnyx's subject.
	MatchSubject *regexp.Regexp
}

const defaultMailboxBase = "https://tool.yaoyiyaocoffee.cn"

// NewYaoYiMailbox builds a reader for one share token.
func NewYaoYiMailbox(shareToken string) *YaoYiMailbox {
	return &YaoYiMailbox{
		BaseURL:      defaultMailboxBase,
		ShareToken:   shareToken,
		MatchSubject: regexp.MustCompile(`(?i)telnyx`),
	}
}

type mailboxMessage struct {
	ID         string `json:"id"`
	Sender     string `json:"sender"`
	Subject    string `json:"subject"`
	ReceivedAt string `json:"receivedAt"`
	Text       string `json:"text"`
	HTML       string `json:"html"`
}

type mailboxEnvelope struct {
	Data struct {
		Email      string           `json:"email"`
		Messages   []mailboxMessage `json:"messages"`
		NextCursor string           `json:"nextCursor"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Address returns the inbox address behind the share token.
func (m *YaoYiMailbox) Address(ctx context.Context) (string, error) {
	env, err := m.call(ctx, "inbox/info", nil)
	if err != nil {
		return "", err
	}
	return env.Data.Email, nil
}

func (m *YaoYiMailbox) client() *http.Client {
	if m.HTTP != nil {
		return m.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (m *YaoYiMailbox) base() string {
	if m.BaseURL == "" {
		return defaultMailboxBase
	}
	return strings.TrimRight(m.BaseURL, "/")
}

func (m *YaoYiMailbox) call(ctx context.Context, path string, extra map[string]any) (*mailboxEnvelope, error) {
	body := map[string]any{"shareToken": m.token()}
	for k, v := range extra {
		body[k] = v
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.base()+"/api/mailbox-tool/"+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", m.base())
	req.Header.Set("Referer", m.base()+"/inbox")
	req.Header.Set("User-Agent", DefaultUserAgent)
	resp, err := m.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("mailbox %s → HTTP %d: %s", path, resp.StatusCode, truncate(string(buf), 200))
	}
	var env mailboxEnvelope
	if err := json.Unmarshal(buf, &env); err != nil {
		return nil, fmt.Errorf("mailbox %s decode: %w", path, err)
	}
	if env.Error != nil {
		return nil, fmt.Errorf("mailbox %s: %s %s", path, env.Error.Code, env.Error.Message)
	}
	return &env, nil
}

func (m *YaoYiMailbox) token() string {
	if m.ShareToken != "" {
		return m.ShareToken
	}
	// Fall back to a pasted inbox URL carrying ?token= or #s=.
	for _, sep := range []string{"#s=", "token="} {
		if i := strings.Index(m.ListURL, sep); i >= 0 {
			return strings.TrimSpace(m.ListURL[i+len(sep):])
		}
	}
	return ""
}

var portalLinkInText = regexp.MustCompile(`https://portal\.telnyx\.com/[^\s"'<>]*?[?&]token=[0-9a-fA-F-]{32,40}[^\s"'<>]*`)

// WaitPortalLink polls until a Telnyx portal sign-in link appears.
func (m *YaoYiMailbox) WaitPortalLink(ctx context.Context, email string) (string, error) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		if link, err := m.findLink(ctx, email); err == nil && link != "" {
			return link, nil
		} else if err != nil {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return "", fmt.Errorf("no sign-in link for %s: %w", email, lastErr)
			}
			return "", fmt.Errorf("no sign-in link for %s before deadline", email)
		case <-ticker.C:
		}
	}
}

// findLink reads one page of the inbox and returns the newest matching link.
func (m *YaoYiMailbox) findLink(ctx context.Context, email string) (string, error) {
	env, err := m.call(ctx, "inbox/read", nil)
	if err != nil {
		return "", err
	}
	if email != "" && env.Data.Email != "" && !strings.EqualFold(env.Data.Email, email) {
		return "", fmt.Errorf("share token belongs to %s, want %s", env.Data.Email, email)
	}
	// Newest first: a resend produces a second mail and only its token works.
	for _, msg := range env.Data.Messages {
		blob := msg.Subject + "\n" + msg.Text + "\n" + msg.HTML
		if m.MatchSubject != nil && !m.MatchSubject.MatchString(blob) {
			continue
		}
		blob = strings.ReplaceAll(blob, "&amp;", "&")
		// The same URL appears in the plain-text part, the html part and again
		// wrapped in brackets; take the last standalone occurrence.
		all := portalLinkInText.FindAllString(blob, -1)
		for i := len(all) - 1; i >= 0; i-- {
			if link := strings.TrimRight(all[i], "])},.;"); link != "" {
				return link, nil
			}
		}
	}
	return "", nil
}
