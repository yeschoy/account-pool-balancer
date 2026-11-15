package telnyxreg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CaptchaRun solves Turnstile through captcha.run's /v2/tasks protocol.
//
//	POST https://api.captcha.run/v2/tasks
//	     Authorization: Bearer <token>
//	     {"captchaType":"Turnstile","siteReferer":"...","siteKey":"...",
//	      "data":{"action":"signup"}}
//	     → {"taskId":"..."}            (201)
//	GET  https://api.captcha.run/v2/tasks/<taskId>
//	     → {"status":"Working"|"Success"|"Fail","response":{"token":"1...."}}
//
// The action is part of the token signature. A token minted without the
// widget's data-action is rejected by Telnyx with
// "your browser could not be authenticated", so SolveTurnstile always sends it.
type CaptchaRun struct {
	BaseURL string        // default https://api.captcha.run
	Token   string        // captcha.run API token
	HTTP    *http.Client  // optional
	Poll    time.Duration // poll cadence, default 5s
	Timeout time.Duration // overall budget, default 180s
	// ProxyURL routes the captcha API itself. Usually direct.
	ProxyURL string
}

const defaultCaptchaRunBase = "https://api.captcha.run"

// NewCaptchaRun builds a solver with sane timing.
func NewCaptchaRun(token string) *CaptchaRun {
	return &CaptchaRun{
		BaseURL: defaultCaptchaRunBase,
		Token:   token,
		Poll:    5 * time.Second,
		Timeout: 180 * time.Second,
	}
}

type captchaTask struct {
	CaptchaType string         `json:"captchaType"`
	OrderID     string         `json:"orderId,omitempty"`
	SiteReferer string         `json:"siteReferer,omitempty"`
	SiteKey     string         `json:"siteKey,omitempty"`
	Data        map[string]any `json:"data,omitempty"`
}

type captchaResult struct {
	TaskID   string `json:"taskId"`
	ID       string `json:"id"`
	Status   string `json:"status"`
	Response struct {
		Token     string `json:"token"`
		UserAgent string `json:"userAgent"`
	} `json:"response"`
	ErrorID   string `json:"errorId"`
	ErrorDesc string `json:"errorDescription"`
}

// SolveTurnstile mints one token for siteKey/action.
func (c *CaptchaRun) SolveTurnstile(ctx context.Context, siteKey, siteReferer, action string) (string, error) {
	if c.Token == "" {
		return "", fmt.Errorf("captcharun: empty token")
	}
	base := c.BaseURL
	if base == "" {
		base = defaultCaptchaRunBase
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 70 * time.Second}
	}
	poll := c.Poll
	if poll <= 0 {
		poll = 5 * time.Second
	}
	budget := c.Timeout
	if budget <= 0 {
		budget = 180 * time.Second
	}

	task := captchaTask{
		CaptchaType: "Turnstile",
		SiteReferer: siteReferer,
		SiteKey:     siteKey,
	}
	if action != "" {
		task.Data = map[string]any{"action": action}
	}
	payload, err := json.Marshal(task)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v2/tasks", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("captcharun create: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("captcharun create → HTTP %d: %s", resp.StatusCode, string(body))
	}
	var created captchaResult
	if err := json.Unmarshal(body, &created); err != nil {
		return "", fmt.Errorf("captcharun create decode: %w (%s)", err, string(body))
	}

	// Some captcha types answer synchronously with the token.
	if created.Response.Token != "" && created.TaskID == "" {
		return created.Response.Token, nil
	}
	taskID := created.TaskID
	if taskID == "" {
		taskID = created.ID
	}
	if taskID == "" {
		return "", fmt.Errorf("captcharun: no taskId (%s)", string(body))
	}

	deadline := time.Now().Add(budget)
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf("captcharun: task %s timed out after %s", taskID, budget)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(poll):
		}
		greq, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/tasks/"+taskID, nil)
		if err != nil {
			return "", err
		}
		greq.Header.Set("Authorization", "Bearer "+c.Token)
		gresp, err := hc.Do(greq)
		if err != nil {
			continue // transient; keep polling until the deadline
		}
		gbuf, _ := io.ReadAll(io.LimitReader(gresp.Body, 1<<20))
		gresp.Body.Close()
		if gresp.StatusCode < 200 || gresp.StatusCode > 299 {
			continue
		}
		var res captchaResult
		if err := json.Unmarshal(gbuf, &res); err != nil {
			continue
		}
		switch res.Status {
		case "Success":
			if res.Response.Token == "" {
				return "", fmt.Errorf("captcharun: success without token")
			}
			return res.Response.Token, nil
		case "Fail":
			return "", fmt.Errorf("captcharun: task failed: %s %s", res.ErrorID, res.ErrorDesc)
		default:
			// Working / Processing — keep waiting.
		}
	}
}
