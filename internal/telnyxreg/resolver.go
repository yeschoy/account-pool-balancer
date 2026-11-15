package telnyxreg

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Resolver turns a Microsoft OAuth mailbox credential into a share token.
//
//	POST /api/mailbox-tool/mailboxes/resolve
//	     {"mailboxes":[{"localId","email","clientId","refreshToken"}],
//	      "includeSharePath":true}
//	     → {"data":{"results":[{"localId","ok",
//	            "mailbox":{...,"accessToken","refreshToken","state"},
//	            "sharePath":"/inbox#s=<shareToken>"}]}}
//
// Two behaviours matter operationally:
//
//   - refreshToken rotates on every call. The response carries the next one,
//     so a caller that discards it breaks the mailbox after a single use.
//   - sharePath only appears when includeSharePath is true; without it the
//     response has no inbox address at all.
type Resolver struct {
	BaseURL string
	HTTP    *http.Client
}

const defaultResolverBase = "https://tool.yaoyiyaocoffee.cn"

// MailCredential is one Outlook mailbox expressed the way MsaArtifacts exports it.
type MailCredential struct {
	LocalID      string `json:"localId"`
	Email        string `json:"email"`
	ClientID     string `json:"clientId"`
	RefreshToken string `json:"refreshToken"`
}

// ResolvedMailbox is the resolver's answer for one credential.
type ResolvedMailbox struct {
	Email string
	// ShareToken is the "s" value of /inbox#s=<token>.
	ShareToken string
	// RefreshToken must be persisted: the old one is now dead.
	RefreshToken string
	// AccessToken is short-lived and only good for listing endpoints.
	AccessToken string
	State       string
	ResourceID  string
}

type resolveRequest struct {
	Mailboxes        []MailCredential `json:"mailboxes"`
	IncludeSharePath bool             `json:"includeSharePath"`
}

type resolveResponse struct {
	Data struct {
		Results []struct {
			LocalID string `json:"localId"`
			OK      bool   `json:"ok"`
			Mailbox struct {
				ResourceID   string `json:"resourceId"`
				Email        string `json:"email"`
				AccessToken  string `json:"accessToken"`
				RefreshToken string `json:"refreshToken"`
				State        string `json:"state"`
				ClientID     string `json:"clientId"`
			} `json:"mailbox"`
			SharePath string `json:"sharePath"`
			Error     *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"results"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

var shareTokenPattern = regexp.MustCompile(`[#?&]s=([A-Za-z0-9_-]+)`)

// ShareTokenFromPath pulls the token out of "/inbox#s=<token>".
func ShareTokenFromPath(p string) string {
	if m := shareTokenPattern.FindStringSubmatch(p); m != nil {
		return m[1]
	}
	return ""
}

func (r *Resolver) client() *http.Client {
	if r.HTTP != nil {
		return r.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (r *Resolver) base() string {
	if r.BaseURL == "" {
		return defaultResolverBase
	}
	return strings.TrimRight(r.BaseURL, "/")
}

// Resolve exchanges one mailbox credential for its inbox share token.
func (r *Resolver) Resolve(ctx context.Context, c MailCredential) (*ResolvedMailbox, error) {
	out, err := r.ResolveBatch(ctx, []MailCredential{c})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("resolve: empty result")
	}
	return out[0], nil
}

// ResolveBatch resolves many credentials in one call.
func (r *Resolver) ResolveBatch(ctx context.Context, creds []MailCredential) ([]*ResolvedMailbox, error) {
	if len(creds) == 0 {
		return nil, nil
	}
	for i := range creds {
		if creds[i].LocalID == "" {
			creds[i].LocalID = newLocalID()
		}
	}
	payload, err := json.Marshal(resolveRequest{Mailboxes: creds, IncludeSharePath: true})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.base()+"/api/mailbox-tool/mailboxes/resolve", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", r.base())
	req.Header.Set("Referer", r.base()+"/")
	req.Header.Set("User-Agent", DefaultUserAgent)
	resp, err := r.client().Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("resolve → HTTP %d: %s", resp.StatusCode, truncate(string(buf), 300))
	}
	var rr resolveResponse
	if err := json.Unmarshal(buf, &rr); err != nil {
		return nil, fmt.Errorf("resolve decode: %w", err)
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("resolve: %s %s", rr.Error.Code, rr.Error.Message)
	}
	out := make([]*ResolvedMailbox, 0, len(rr.Data.Results))
	for _, item := range rr.Data.Results {
		m := &ResolvedMailbox{
			Email:        item.Mailbox.Email,
			RefreshToken: item.Mailbox.RefreshToken,
			AccessToken:  item.Mailbox.AccessToken,
			State:        item.Mailbox.State,
			ResourceID:   item.Mailbox.ResourceID,
			ShareToken:   ShareTokenFromPath(item.SharePath),
		}
		if !item.OK {
			msg := "not ok"
			if item.Error != nil {
				msg = item.Error.Code + " " + item.Error.Message
			}
			return nil, fmt.Errorf("resolve %s: %s", m.Email, msg)
		}
		if m.ShareToken == "" {
			return nil, fmt.Errorf("resolve %s: no sharePath in response", m.Email)
		}
		out = append(out, m)
	}
	return out, nil
}

// newLocalID returns a v4 UUID. The resolver validates this field strictly,
// so an arbitrary unique string is rejected with VALIDATION_ERROR.
func newLocalID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fall back to time-seeded bytes rather than failing the whole batch.
		now := time.Now().UnixNano()
		for i := 0; i < 16 && i < 8; i++ {
			b[i] = byte(now >> (8 * i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewResolver builds a resolver with default timing.
func NewResolver() *Resolver {
	return &Resolver{BaseURL: defaultResolverBase, HTTP: &http.Client{Timeout: 60 * time.Second}}
}
