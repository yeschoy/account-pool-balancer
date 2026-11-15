package x50reg

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
	"time"
)

// Mailbox is one line of the iCloud HME share list: address plus its share URL.
type Mailbox struct {
	Email string
	Token string
}

// ParseMailboxLine accepts "email----url" and extracts the share token from the
// URL's ?token= parameter.
func ParseMailboxLine(line string) (Mailbox, error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return Mailbox{}, errors.New("empty")
	}
	email, raw, ok := strings.Cut(line, "----")
	if !ok {
		// Also accept whitespace or a bare share URL carrying the address.
		if u, err := url.Parse(line); err == nil && u.Query().Get("token") != "" {
			addr := u.Query().Get("alias_email")
			if addr == "" {
				if i := strings.Index(u.Path, "/messages/"); i >= 0 {
					if dec, derr := url.PathUnescape(u.Path[i+len("/messages/"):]); derr == nil {
						addr = dec
					}
				}
			}
			if addr != "" {
				return Mailbox{Email: addr, Token: u.Query().Get("token")}, nil
			}
		}
		return Mailbox{}, fmt.Errorf("no ---- separator in %q", line)
	}
	email = strings.TrimSpace(email)
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return Mailbox{}, fmt.Errorf("parse share url: %w", err)
	}
	tok := u.Query().Get("token")
	if tok == "" {
		return Mailbox{}, fmt.Errorf("share url has no token: %q", raw)
	}
	if email == "" {
		if dec, derr := url.PathUnescape(u.Path[strings.Index(u.Path, "/messages/")+len("/messages/"):]); derr == nil {
			email = dec
		}
	}
	email, err = url.PathUnescape(email)
	if err != nil {
		email = strings.ReplaceAll(email, "%40", "@")
	}
	if !strings.Contains(email, "@") {
		return Mailbox{}, fmt.Errorf("no address in %q", line)
	}
	return Mailbox{Email: email, Token: tok}, nil
}

// ParseMailboxList parses a whole block of "email----url" lines.
func ParseMailboxList(block string) ([]Mailbox, error) {
	var out []Mailbox
	for _, line := range strings.Split(block, "\n") {
		mb, err := ParseMailboxLine(line)
		if err != nil {
			continue
		}
		out = append(out, mb)
	}
	if len(out) == 0 {
		return nil, errors.New("no usable mailbox lines")
	}
	return out, nil
}

// verifyLink matches the 50x confirm link: https://ai.50x.energy/?verify=<token>
var verifyLink = regexp.MustCompile(`https?://[^\s"'<>]*[?&]verify=([^\s"'<>&]+)`)

// extractVerifyToken pulls the confirm token from a mail body.
func extractVerifyToken(text string) (string, bool) {
	if m := verifyLink.FindStringSubmatch(text); len(m) == 2 {
		if dec, err := url.QueryUnescape(m[1]); err == nil {
			return dec, true
		}
		return m[1], true
	}
	// Plain token forms seen inside JSON bodies, where quotes are escaped.
	if m := regexp.MustCompile(`verify[=":\\ ]+([A-Za-z0-9_\-:.]+)`).FindStringSubmatch(text); len(m) == 2 {
		return m[1], true
	}
	return "", false
}

// mailMessage is one entry of the shared-mailbox API.
type mailMessage struct {
	ID         int64  `json:"id"`
	Subject    string `json:"subject"`
	FromName   string `json:"fromName"`
	FromAddr   string `json:"fromAddress"`
	ToAddress  string `json:"toAddress"`
	Text       string `json:"textContent"`
	HTML       string `json:"htmlContent"`
	ReceivedAt string `json:"receivedAt"`
}

type mailEnvelope struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Data  struct {
		AliasEmail string        `json:"aliasEmail"`
		Emails     []mailMessage `json:"emails"`
	} `json:"data"`
}

// FetchMessages reads the shared inbox for one mailbox.
func (r *Registrar) fetchMessages(ctx context.Context, mb Mailbox) ([]mailMessage, error) {
	q := url.Values{}
	q.Set("alias_email", mb.Email)
	q.Set("token", mb.Token)
	q.Set("range", "all")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.cfg.MailBase+"/api/messages?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", r.cfg.UserAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mailbox http %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var env mailEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("mailbox decode: %w", err)
	}
	if !env.OK {
		return nil, fmt.Errorf("mailbox: %s", env.Error)
	}
	return env.Data.Emails, nil
}

// waitMailToken polls the mailbox until a fresh confirm mail with a verify link
// arrives, then returns that token.
func (r *Registrar) waitMailToken(ctx context.Context, since time.Time) (string, error) {
	mb := r.mailbox
	if mb.Token == "" {
		return "", errors.New("no mailbox configured")
	}
	deadline := time.Now().Add(r.cfg.MailWait)
	var lastErr error
	for {
		mails, err := r.fetchMessages(ctx, mb)
		if err != nil {
			lastErr = err
		} else {
			for _, m := range mails { // newest first
				if m.ReceivedAt != "" {
					if ts, perr := time.Parse(time.RFC3339, m.ReceivedAt); perr == nil && ts.Before(since.Add(-5*time.Second)) {
						continue
					}
				}
				if !is50xMail(m) {
					continue
				}
				if tok, ok := extractVerifyToken(m.Text + "\n" + m.HTML); ok {
					return tok, nil
				}
			}
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return "", fmt.Errorf("confirm mail not found within %s (last error: %v)", r.cfg.MailWait, lastErr)
			}
			return "", fmt.Errorf("confirm mail not found within %s", r.cfg.MailWait)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(r.cfg.MailInterval):
		}
	}
}

func is50xMail(m mailMessage) bool {
	s := strings.ToLower(m.Subject + " " + m.FromAddr + " " + m.FromName)
	return strings.Contains(s, "50x") || strings.Contains(s, "50x_energy")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// resolveEmails validates the signup address matches the mailbox address.
func (r *Registrar) mailboxMatches() bool {
	return strings.EqualFold(strings.TrimSpace(r.cfg.Email), strings.TrimSpace(r.mailbox.Email))
}
