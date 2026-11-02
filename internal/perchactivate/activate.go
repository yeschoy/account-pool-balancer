// Package perchactivate activates a Perch Starter session using the web flow.
package perchactivate

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const (
	defaultApp      = "https://app.perchai.app"
	defaultChat     = "https://chat.perchai.app"
	defaultSupabase = "https://zlfuvsfjtgsdtqcaykia.supabase.co"
	defaultKey      = "sb_publishable_w4R_pNkUpygBFIljZCoOlA_67ACg0FO"
	defaultPolicy   = "2026-09-13"
	cookieChunkSize = 3180
	maxJSON         = 4 << 20
)

// Config selects endpoints and transport. HTTPClient may have a proxy transport.
// Its cookie jar and redirect policy are not used, so each call uses only the
// supplied session and credentials cannot be forwarded by a redirect.
type Config struct {
	HTTPClient                                               *http.Client
	AppURL, ChatURL, SupabaseURL, SupabaseKey, PolicyVersion string
}

// Result describes whether a plan was selected or a message was sent.
// Reply is omitted deliberately: server-controlled text may contain credentials.
type Result struct {
	Already, Sent, SelectedStarter bool
	PlanCode, ThreadID             string
}

// Activate uses one freshly persisted Supabase session for both the web cookie
// and RPC Bearer. It does not refresh or persist tokens; callers must serialize
// refresh/activation for an account and pass the latest complete session.
// An error after chat was sent can leave delivery uncertain; retry only after
// checking the conversation, and do not run activations concurrently.
func Activate(ctx context.Context, cfg Config, session json.RawMessage) (Result, error) {
	var data map[string]json.RawMessage
	if json.Unmarshal(session, &data) != nil || data == nil {
		return Result{}, errors.New("invalid session JSON")
	}
	var token string
	if json.Unmarshal(data["access_token"], &token) != nil || strings.TrimSpace(token) == "" {
		return Result{}, errors.New("session has no access token")
	}
	app, err := baseURL(cfg.AppURL, defaultApp)
	if err != nil {
		return Result{}, err
	}
	chat, err := baseURL(cfg.ChatURL, defaultChat)
	if err != nil {
		return Result{}, err
	}
	supabase, err := baseURL(cfg.SupabaseURL, defaultSupabase)
	if err != nil {
		return Result{}, err
	}
	project := strings.Split(supabase.Hostname(), ".")[0]
	if project == "" {
		return Result{}, errors.New("invalid Supabase URL")
	}
	name := "sb-" + project + "-auth-token"
	value := "base64-" + base64.RawURLEncoding.EncodeToString(session)
	cookies := make([]*http.Cookie, 0, 1+len(value)/cookieChunkSize)
	if len(value) <= cookieChunkSize {
		cookies = append(cookies, &http.Cookie{Name: name, Value: value})
	} else {
		for i := 0; len(value) > 0; i++ {
			n := min(cookieChunkSize, len(value))
			cookies = append(cookies, &http.Cookie{Name: name + "." + strconv.Itoa(i), Value: value[:n]})
			value = value[n:]
		}
	}
	client := http.Client{}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
	}
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c := runner{client: &client, cookies: cookies, cookieName: name}
	var contextReply struct {
		Context *struct {
			PlanCode     string `json:"planCode"`
			TierRequired *bool  `json:"tierSelectionRequired"`
		} `json:"context"`
	}
	if err := c.json(ctx, "read session context", http.MethodGet, app.String()+"/api/perch-terminal/session-context", nil, true, "", "", &contextReply); err != nil {
		return Result{}, err
	}
	if contextReply.Context == nil {
		return Result{}, errors.New("read session context: invalid response")
	}
	out := Result{PlanCode: contextReply.Context.PlanCode}
	if strings.TrimSpace(out.PlanCode) == "" && contextReply.Context.TierRequired == nil {
		return out, errors.New("read session context: missing plan state")
	}
	if (contextReply.Context.TierRequired != nil && *contextReply.Context.TierRequired) || strings.TrimSpace(out.PlanCode) == "" {
		policy, key := fallback(cfg.PolicyVersion, defaultPolicy), fallback(cfg.SupabaseKey, defaultKey)
		var selected struct {
			OK       bool   `json:"ok"`
			PlanCode string `json:"plan_code"`
		}
		if err := c.json(ctx, "select Starter", http.MethodPost, supabase.String()+"/rest/v1/rpc/perch_ai_select_starter_with_data_notice", map[string]any{"p_notice_version": policy}, false, key, token, &selected); err != nil {
			return out, err
		}
		if !selected.OK {
			return out, errors.New("select Starter: response was not ok")
		}
		out.SelectedStarter = true
		if selected.PlanCode != "" {
			out.PlanCode = selected.PlanCode
		}
	}
	threadsURL := chat.String() + "/api/perchai/threads"
	var listed struct {
		Threads *[]struct {
			ID string `json:"id"`
		} `json:"threads"`
	}
	if err := c.json(ctx, "list threads", http.MethodGet, threadsURL, nil, true, "", "", &listed); err != nil {
		return out, err
	}
	if listed.Threads == nil {
		return out, errors.New("list threads: invalid response")
	}
	for _, thread := range *listed.Threads {
		if strings.TrimSpace(thread.ID) == "" {
			return out, errors.New("list threads: missing thread ID")
		}
		u, _ := url.Parse(threadsURL)
		q := u.Query()
		q.Set("threadId", thread.ID)
		u.RawQuery = q.Encode()
		var messages struct {
			Messages *[]struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		if err := c.json(ctx, "list messages", http.MethodGet, u.String(), nil, true, "", "", &messages); err != nil {
			return out, err
		}
		if messages.Messages == nil {
			return out, errors.New("list messages: invalid response")
		}
		for _, m := range *messages.Messages {
			if m.Role == "user" {
				out.Already = true
				return out, nil
			}
			if m.Role == "" {
				return out, errors.New("list messages: missing role")
			}
		}
	}
	var created struct {
		Thread *struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := c.json(ctx, "create thread", http.MethodPost, threadsURL, struct{}{}, true, "", "", &created); err != nil {
		return out, err
	}
	if created.Thread == nil || strings.TrimSpace(created.Thread.ID) == "" {
		return out, errors.New("create thread: missing thread ID")
	}
	out.ThreadID = created.Thread.ID
	id := []rune(out.ThreadID)
	if len(id) > 8 {
		id = id[:8]
	}
	body := map[string]any{"message": "hi", "clientMessageId": "perch-activate-" + string(id), "threadId": out.ThreadID, "webSearch": false, "manualModelOptionId": nil, "promoOverflowAccepted": false}
	resp, err := c.request(ctx, "send message", http.MethodPost, chat.String()+"/api/perchai/chat", body, true, "", "", "text/event-stream")
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if err := readDone(resp.Body); err != nil {
		return out, err
	}
	out.Sent = true
	return out, nil
}

func fallback(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

func baseURL(raw, def string) (*url.URL, error) {
	u, err := url.Parse(fallback(raw, def))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid endpoint URL")
	}
	// Reject malformed IP literals, while permitting normal DNS host names.
	if strings.Contains(u.Hostname(), ":") {
		if _, err := netip.ParseAddr(u.Hostname()); err != nil {
			return nil, errors.New("invalid endpoint URL")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

type runner struct {
	client     *http.Client
	cookies    []*http.Cookie
	cookieName string
}

func (c runner) request(ctx context.Context, step, method, endpoint string, body any, cookie bool, key, token, accept string) (*http.Response, error) {
	var b io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%s: invalid request", step)
		}
		b = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, b)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid request", step)
	}
	req.Header.Set("Accept", accept)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie {
		for _, item := range c.cookies {
			req.AddCookie(item)
		}
	}
	if key != "" {
		req.Header.Set("apikey", key)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: request failed", step)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", step, resp.StatusCode)
	}
	// Server-side refresh can rotate the token; continuing would mix sessions.
	for _, item := range resp.Cookies() {
		if item.Name == c.cookieName || strings.HasPrefix(item.Name, c.cookieName+".") {
			resp.Body.Close()
			return nil, fmt.Errorf("%s: session cookie changed; refresh and retry", step)
		}
	}
	return resp, nil
}

func (c runner) json(ctx context.Context, step, method, endpoint string, body any, cookie bool, key, token string, dst any) error {
	resp, err := c.request(ctx, step, method, endpoint, body, cookie, key, token, "application/json")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil || len(raw) > maxJSON || json.Unmarshal(raw, dst) != nil {
		return fmt.Errorf("%s: invalid response", step)
	}
	return nil
}

func readDone(r io.Reader) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var data []string
	done, ok := false, false
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		var event struct {
			Type string `json:"type"`
			OK   bool   `json:"ok"`
		}
		err := json.Unmarshal([]byte(strings.Join(data, "\n")), &event)
		data = nil
		if err != nil {
			return errors.New("send message: invalid SSE event")
		}
		if event.Type == "done" {
			done, ok = true, event.OK
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if scanner.Err() != nil {
		return errors.New("send message: SSE read failed")
	}
	if err := consume(); err != nil {
		return err
	}
	if !done || !ok {
		return errors.New("send message: SSE did not complete successfully")
	}
	return nil
}
