package x50reg

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
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
	// coerced maps a configured URL to the URL that corrected its scheme, so a
	// probed pool does not pay one TCP round trip per account.
	coerced map[string]string
}

// proxyCoerceWorkers bounds concurrent probes so a 500-entry pool does not
// serialize on TCP timeouts while staying gentle on the gateway.
const proxyCoerceWorkers = 16

// CoerceAll probes every proxy once and rewrites the ones whose declared scheme
// does not match the wire protocol the gateway answers.
//
// This is what makes a pasted `host:port` or `socks5://…` list usable against an
// HTTP-CONNECT gateway: the correction happens here, once, with a report of what
// changed, instead of surfacing later as an opaque per-account timeout.
func (p *ProxyPool) CoerceAll() (coerced map[string]string, failed map[string]string) {
	coerced = map[string]string{}
	failed = map[string]string{}
	if p == nil {
		return coerced, failed
	}
	p.mu.Lock()
	list := append([]string(nil), p.list...)
	if p.coerced == nil {
		p.coerced = map[string]string{}
	}
	p.mu.Unlock()

	type outcome struct {
		raw   string
		fixed string
		why   string
	}
	results := make([]outcome, len(list))
	sem := make(chan struct{}, proxyCoerceWorkers)
	var wg sync.WaitGroup
	for i, raw := range list {
		wg.Add(1)
		go func(i int, raw string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if fixed, changed := CoerceProxyScheme(raw); changed {
				results[i] = outcome{raw: raw, fixed: fixed}
				return
			}
			if _, err := DetectProxyScheme(hostOf(raw)); err != nil {
				results[i] = outcome{raw: raw, why: err.Error()}
			}
		}(i, raw)
	}
	wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range results {
		switch {
		case r.fixed != "":
			coerced[r.raw] = r.fixed
			p.coerced[r.raw] = r.fixed
			for i := range p.list {
				if p.list[i] == r.raw {
					p.list[i] = r.fixed
				}
			}
			if ok, seen := p.healthy[r.raw]; seen {
				delete(p.healthy, r.raw)
				p.healthy[r.fixed] = ok
			}
		case r.why != "":
			failed[r.raw] = r.why
		}
	}
	return coerced, failed
}

// Resolve returns the URL to use for raw, applying a scheme correction learned
// by CoerceAll or by an earlier Pick.
func (p *ProxyPool) Resolve(raw string) string {
	if p == nil || raw == "" {
		return raw
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if fixed, ok := p.coerced[raw]; ok {
		return fixed
	}
	return raw
}

func hostOf(raw string) string {
	u, err := url.Parse(normalizeProxyURL(raw))
	if err != nil {
		return ""
	}
	return u.Host
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

// ---------------------------------------------------------------------------
// Scheme detection
//
// Proxy list vendors hand out `host:port` lines with no scheme, and the config
// UI used to default every schemeless line to socks5. A gateway that only
// speaks HTTP CONNECT then fails in three confusing ways, depending on where
// the mismatch lands:
//
//	socks5 handshake stalls        → context deadline exceeded (Client.Timeout …)
//	plaintext 403 read as TLS      → server gave HTTP response to HTTPS client
//	connection torn down mid-flight→ context canceled
//
// None of those mention the proxy, which is why a whole 499-row batch can run
// and fail with no usable signal. DetectProxyScheme probes the port, and
// CoerceProxyScheme rewrites the stored URL to what the gateway actually speaks.
// ---------------------------------------------------------------------------

// ProxyScheme is the wire protocol one proxy endpoint speaks.
type ProxyScheme string

const (
	SchemeSOCKS5 ProxyScheme = "socks5"
	SchemeHTTP   ProxyScheme = "http"
)

// proxySchemeTimeout bounds one probe connection attempt.
const proxySchemeTimeout = 6 * time.Second

// proxyGreetingWait is how long a SOCKS5 gateway is given to answer the
// greeting. A real SOCKS server replies immediately; an HTTP proxy stays silent
// until it sees a request line, so a short wait here trades nothing for ~6s per
// proxy on an HTTP-only pool.
const proxyGreetingWait = 800 * time.Millisecond

// SchemeOf reports the scheme a proxy URL declares, defaulting to socks5 for a
// bare host:port because that is the historical CLI default.
func SchemeOf(raw string) ProxyScheme {
	u, err := url.Parse(normalizeProxyURL(raw))
	if err != nil {
		return SchemeSOCKS5
	}
	if strings.HasPrefix(u.Scheme, "socks") {
		return SchemeSOCKS5
	}
	if u.Scheme == "" {
		return SchemeSOCKS5
	}
	return SchemeHTTP
}

// normalizeProxyURL gives a schemeless "host:port" a scheme so url.Parse does
// not read "host" as a scheme.
func normalizeProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "://") {
		return raw
	}
	return "socks5://" + raw
}

// DetectProxyScheme probes host:port and reports the protocol that answered.
//
// The SOCKS5 greeting is the discriminator: a SOCKS5 server replies 0x05, an
// HTTP proxy replies with the ASCII "HTTP/" header line. Reading only the first
// byte is enough and keeps the probe cheap; auth is never needed for it.
//
// An HTTP proxy usually waits for a request line before answering at all, so a
// bare greeting can go unanswered. The probe therefore sends the SOCKS greeting
// and, when no byte arrives, retries with a CONNECT. Getting an answer of either
// shape is the whole point; it never completes a tunnel.
func DetectProxyScheme(hostport string) (ProxyScheme, error) {
	if hostport == "" {
		return "", errors.New("empty proxy address")
	}
	conn, err := (&net.Dialer{Timeout: proxySchemeTimeout}).Dial("tcp", hostport)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(proxyGreetingWait))

	if _, err := conn.Write([]byte("\x05\x01\x00")); err != nil {
		return "", err
	}
	first := make([]byte, 1)
	if _, err := io.ReadFull(conn, first); err == nil {
		if first[0] == 0x05 {
			return SchemeSOCKS5, nil
		}
		if first[0] == 'H' {
			return SchemeHTTP, nil
		}
	}
	// No greeting-parsable answer: an HTTP CONNECT proxy only replies once it has
	// a request, so confirm with one before deciding.
	return probeHTTPConnect(hostport)
}

// probeHTTPConnect sends one CONNECT and treats any HTTP status as proof that
// the endpoint speaks HTTP. Any authorization outcome is irrelevant here: only
// the protocol matters, and 407 is still an HTTP-speaking gateway.
func probeHTTPConnect(hostport string) (ProxyScheme, error) {
	conn, err := (&net.Dialer{Timeout: proxySchemeTimeout}).Dial("tcp", hostport)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(proxySchemeTimeout))
	req := "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		return "", err
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return "", err
	}
	if strings.HasPrefix(string(buf), "HTTP/") {
		return SchemeHTTP, nil
	}
	return "", fmt.Errorf("unrecognized proxy protocol on %s", hostport)
}

// CoerceProxyScheme rewrites raw to speak the scheme the gateway actually uses.
//
// It only changes the URL when a probe succeeds and disagrees with what the URL
// declares, so a working socks5 endpoint keeps its sticky-session form and an
// unreachable gateway is left exactly as configured. The boolean reports
// whether the returned string differs.
func CoerceProxyScheme(raw string) (string, bool) {
	if raw == "" {
		return raw, false
	}
	u, err := url.Parse(normalizeProxyURL(raw))
	if err != nil || u.Host == "" {
		return raw, false
	}
	actual, derr := DetectProxyScheme(u.Host)
	if derr != nil {
		return raw, false
	}
	declared := SchemeOf(raw)
	if actual == declared {
		return raw, false
	}
	u.Scheme = string(actual)
	return u.String(), true
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
