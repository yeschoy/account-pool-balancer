// Package thcaptcha is the Turnstile solver client for captcha-run.com.
//
// tokenharbor.ai renders Cloudflare Turnstile on a share of signups. The token
// cannot be produced locally, so it is bought from a solving service: create a
// task, poll it, take the token. The service's documented ladder is
//
//	POST {base}/v2/tasks                 {"taskId": "…"}  (or a synchronous result)
//	GET  {base}/v2/tasks/{taskId}        {"status":"Working|Success|Fail", …}
//
// and a token is only good for a short while (≈2 minutes), which is why the
// caller solves and submits back to back.
package thcaptcha

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBase is the global endpoint of the solver.
const DefaultBase = "https://api.captcha-run.com"

// ErrUnsolvable reports a task the service finished with a failure code; it is
// safe to retry with a fresh task.
var ErrUnsolvable = errors.New("验证码未能识别")

// Config selects the service. Zero values mean the live global endpoint.
type Config struct {
	BaseURL string
	APIKey  string
	// HTTPClient carries the egress proxy; nil means a plain client.
	HTTPClient *http.Client
	// PollEvery is the result polling interval; zero means 3s.
	PollEvery time.Duration
	// Timeout bounds one solve; zero means 3m.
	Timeout time.Duration
}

func (c Config) base() string {
	if c.BaseURL == "" {
		return DefaultBase
	}
	return strings.TrimSuffix(c.BaseURL, "/")
}

// Client solves Turnstile challenges.
type Client struct {
	cfg  Config
	http *http.Client
}

// New builds a solver. A missing API key is not an error here: the caller
// decides whether solving is available at all.
func New(cfg Config) *Client {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}
}

type task struct {
	TaskID string `json:"taskId"`
	Result *struct {
		Token string `json:"token"`
	} `json:"result"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	Response *struct {
		Token string `json:"token"`
	} `json:"response"`
}

// Turnstile solves one challenge and returns the token.
//
// The action is forwarded because Cloudflare checks it; the referer must be the
// page that renders the widget or the token is rejected by the site.
func (c *Client) Turnstile(ctx context.Context, siteKey, referer, action string) (string, error) {
	if strings.TrimSpace(c.cfg.APIKey) == "" {
		return "", errors.New("未配置 captcha-run API Key")
	}
	timeout := c.cfg.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload := map[string]any{"captchaType": "Turnstile", "siteKey": siteKey, "siteReferer": referer}
	if action != "" {
		payload["data"] = map[string]string{"action": action}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	var created task
	if err := c.do(ctx, http.MethodPost, "/v2/tasks", body, &created); err != nil {
		return "", err
	}
	if created.Result != nil && created.Result.Token != "" {
		return created.Result.Token, nil
	}
	if created.TaskID == "" {
		return "", errors.New("验证码服务未返回任务 ID")
	}

	poll := c.cfg.PollEvery
	if poll <= 0 {
		poll = 3 * time.Second
	}
	for {
		select {
		case <-ctx.Done():
			return "", errors.New("验证码识别超时")
		case <-time.After(poll):
		}
		var got task
		if err := c.do(ctx, http.MethodGet, "/v2/tasks/"+created.TaskID, nil, &got); err != nil {
			return "", err
		}
		switch got.Status {
		case "Success":
			if got.Response != nil && got.Response.Token != "" {
				return got.Response.Token, nil
			}
			return "", errors.New("验证码识别成功但没有 token")
		case "Fail":
			reason := got.Reason
			if reason == "" {
				reason = "未知原因"
			}
			return "", fmt.Errorf("%w: %s", ErrUnsolvable, reason)
		case "Working", "":
			continue
		default:
			return "", fmt.Errorf("验证码服务返回未知状态 %q", got.Status)
		}
	}
}

func (c *Client) do(ctx context.Context, method, path string, body []byte, out any) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.base()+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("验证码服务请求失败: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		// The service documents a {statusCode,message,error} error body; its
		// message is the only useful part and carries no credential.
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		msg := strings.TrimSpace(e.Message)
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("验证码服务错误: %s", msg)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return errors.New("验证码服务返回无法解析")
	}
	return nil
}
