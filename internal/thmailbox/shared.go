package thmailbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"account-pool-balancer/internal/thregister"
)

func sharedMailbox(u *url.URL) bool {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return u.Hostname() != "" && len(parts) == 2 && parts[0] == "mailbox" && len(parts[1]) >= 8
}

var sharedSubjectRE = regexp.MustCompile(`(?is)<div\b[^>]*class=["']subject["'][^>]*>(.*?)</div>`)
var sharedRowsRE = regexp.MustCompile(`(?is)<div\b[^>]*class=["']label["'][^>]*>(.*?)</div>\s*<div\b[^>]*class=["']value["'][^>]*>(.*?)</div>`)
var sharedContentRE = regexp.MustCompile(`(?is)<div\b[^>]*class=["']content["'][^>]*>(.*?)</div>\s*</section>`)
var sharedTimeRE = regexp.MustCompile(`(?is)<time\b[^>]*datetime=["']([^"']+)["']`)
var sharedTagsRE = regexp.MustCompile(`(?s)<[^>]+>`)
var sharedProtectedRE = regexp.MustCompile(`data-cfemail=["']([a-fA-F0-9]+)["']`)
var sharedLinkRE = regexp.MustCompile(`https://tokenharbor\.ai/verify-email\?token=[A-Za-z0-9_-]+`)

func sharedText(s string) string {
	return strings.TrimSpace(html.UnescapeString(sharedTagsRE.ReplaceAllString(s, " ")))
}
func sharedAddress(s string) string {
	if m := sharedProtectedRE.FindStringSubmatch(s); m != nil {
		raw, err := hex.DecodeString(m[1])
		if err == nil && len(raw) > 1 {
			for i := 1; i < len(raw); i++ {
				raw[i] ^= raw[0]
			}
			return string(raw[1:])
		}
	}
	return sharedText(s)
}

func parseSharedMessages(raw []byte) []thregister.Message {
	page := string(raw)
	content := sharedContentRE.FindStringSubmatch(page)
	if content == nil {
		return nil
	}
	rows := map[string]string{}
	for _, m := range sharedRowsRE.FindAllStringSubmatch(page, -1) {
		rows[strings.ToLower(sharedText(m[1]))] = m[2]
	}
	first := func(names ...string) string {
		for _, name := range names {
			if s := rows[name]; s != "" {
				return s
			}
		}
		return ""
	}
	subject := ""
	if m := sharedSubjectRE.FindStringSubmatch(page); m != nil {
		subject = sharedText(m[1])
	}
	date := sharedText(first("时间", "日期", "date", "received", "received at"))
	if m := sharedTimeRE.FindStringSubmatch(page); m != nil {
		date = html.UnescapeString(m[1])
	}
	var received time.Time
	for _, layout := range []string{time.RFC3339Nano, time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700", "2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05 MST"} {
		if parsed, err := time.Parse(layout, date); err == nil {
			received = parsed
			break
		}
	}
	text := sharedText(content[1])
	for _, link := range sharedLinkRE.FindAllString(html.UnescapeString(content[1]), -1) {
		text += "\n" + link
	}
	id := sha256.Sum256([]byte(subject + "\n" + date + "\n" + text))
	return []thregister.Message{{ID: hex.EncodeToString(id[:]), Subject: subject,
		From: sharedAddress(first("发件人", "from", "sender")), ReceivedAt: received, Text: text}}
}

func (c *Client) sharedMessages(ctx context.Context) ([]thregister.Message, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.URL, nil)
	if err != nil {
		return nil, fmt.Errorf("接码页地址无效")
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("共享接码页网络读取失败")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	if err != nil || len(raw) > 4<<20 {
		return nil, fmt.Errorf("共享接码页响应不完整或过大")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("共享接码页返回 HTTP %d", resp.StatusCode)
	}
	return parseSharedMessages(raw), nil
}
