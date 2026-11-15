package x50reg

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ProxyPool hands out proxy URLs round-robin. Sticky per-account rotation keeps
// one registration on one egress so the signup IP and the mail poll agree.
type ProxyPool struct {
	mu      sync.Mutex
	list    []string
	next    int
	healthy map[string]bool
}

// LoadProxyFile reads one proxy URL per line, ignoring blanks and comments.
func LoadProxyFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, err := url.Parse(line); err != nil {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// NewProxyPool builds a pool; an empty list means direct egress.
func NewProxyPool(list []string) *ProxyPool {
	p := &ProxyPool{list: append([]string(nil), list...), healthy: map[string]bool{}}
	for _, u := range p.list {
		p.healthy[u] = true
	}
	return p
}

// Len reports the number of proxies.
func (p *ProxyPool) Len() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.list)
}

// Pick returns the next usable proxy, or "" for direct egress. When every
// proxy has been marked bad it resets and starts over rather than stalling.
func (p *ProxyPool) Pick() string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.list) == 0 {
		return ""
	}
	for i := 0; i < len(p.list); i++ {
		u := p.list[(p.next+i)%len(p.list)]
		if p.healthy[u] {
			p.next = (p.next + i + 1) % len(p.list)
			return u
		}
	}
	for k := range p.healthy {
		p.healthy[k] = true
	}
	u := p.list[p.next]
	p.next = (p.next + 1) % len(p.list)
	return u
}

// MarkBad quarantines a proxy that failed at the connection level.
func (p *ProxyPool) MarkBad(u string) {
	if p == nil || u == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.healthy[u] = false
}

// ErrProxyRejected marks a proxy the gateway itself refused (auth, IP allowlist,
// plan), as opposed to a target-side failure. Retrying with another proxy is
// the only useful response, and the failure must not be reported as a signup
// rejection.
type ErrProxyRejected struct {
	Proxy  string
	Detail string
}

func (e *ErrProxyRejected) Error() string {
	return "proxy rejected by gateway: " + e.Detail
}

// ClassifyProxyResponse inspects a gateway reply for an HTTP/SOCKS status that
// means the proxy refused to carry the request.
func ClassifyProxyResponse(statusLine string) error {
	l := strings.ToLower(statusLine)
	switch {
	case strings.Contains(l, "forbidden"), strings.Contains(l, "407"),
		strings.Contains(l, "402"), strings.Contains(l, "proxy authentication"),
		strings.Contains(l, "not supported"), strings.Contains(l, "blocked"):
		return &ErrProxyRejected{Detail: strings.TrimSpace(statusLine)}
	}
	return nil
}

// WithStickySID rewrites a zooproxy-style "sid-XXXX" segment so each account
// gets its own sticky session on the same gateway. Proxies without a sid
// parameter are returned unchanged.
func WithStickySID(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	user := u.User.Username()
	if strings.Contains(user, "-sid-") {
		parts := strings.Split(user, "-sid-")
		sid := randomHex(4) // 8 hex chars, matches observed sid length
		user = parts[0] + "-sid-" + sid
		if rest := strings.Index(parts[1], "-t-"); rest >= 0 {
			user += parts[1][rest:]
		}
		u.User = url.UserPassword(user, passwordOf(u.User))
	}
	return u.String()
}

func passwordOf(u *url.Userinfo) string {
	if p, ok := u.Password(); ok {
		return p
	}
	return ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%08x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// RandomPassword builds a password that satisfies typical signup rules.
func RandomPassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("x50x-%d", time.Now().UnixNano())
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:6]) + "@" + string(b[6:12]) + "!" + string(b[12:])
}
