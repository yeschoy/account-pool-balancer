package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/proxy"
	"golang.org/x/net/websocket"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factory"
	"account-pool-balancer/internal/pool"
)

// handleFactoryResponsesWS serves the downstream OpenAI Responses WebSocket.
// The first client frame is response.create and carries the model; only then
// can an account be leased. After the upstream upgrade the connection is a
// transparent frame relay: a WebSocket cannot be retried once a frame was
// delivered.
func (s *Server) handleFactoryResponsesWS(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	ck, ok := s.authClient(cfg, r)
	if !ok {
		writeLocalError(w, "openai", http.StatusUnauthorized, "authentication_error", "invalid_api_key", "invalid API key", 0)
		return
	}
	if keyPool(cfg, ck) != config.ProviderFactory {
		http.NotFound(w, r)
		return
	}
	cfg = cfg.ForProvider(config.ProviderFactory)
	if !cfg.PoolEnabled(config.ProviderFactory) {
		writeLocalError(w, "openai", 503, "api_error", "pool_disabled", "this account pool is disabled", 0)
		return
	}
	websocket.Handler(func(ws *websocket.Conn) {
		s.proxyFactoryWS(ws, r, cfg, ck)
	}).ServeHTTP(w, r)
}

func (s *Server) proxyFactoryWS(ws *websocket.Conn, r *http.Request, cfg *config.Config, ck config.ClientKey) {
	ws.MaxPayloadBytes = 32 << 20
	_ = ws.SetDeadline(time.Now().Add(2 * time.Minute))
	var first string
	if err := websocket.Message.Receive(ws, &first); err != nil {
		_ = ws.Close()
		return
	}
	var frame struct {
		Type    string `json:"type"`
		Model   string `json:"model"`
		Factory struct {
			AssistantMessageID string `json:"assistantMessageId"`
			TraceContext       struct {
				Traceparent string `json:"traceparent"`
			} `json:"traceContext"`
		} `json:"_factory"`
	}
	if json.Unmarshal([]byte(first), &frame) != nil || frame.Type != "response.create" || frame.Model == "" {
		_ = websocket.Message.Send(ws, `{"type":"error","error":{"message":"first frame must be response.create with a model"}}`)
		_ = ws.Close()
		return
	}
	model := frame.Model
	resolved, ok := resolveFactoryAuto(cfg, model, "responses")
	if !ok {
		_ = websocket.Message.Send(ws, `{"type":"error","error":{"message":"Factory has no configured Responses model"}}`)
		_ = ws.Close()
		return
	}
	if resolved != model {
		if b, err := replaceFactoryModel([]byte(first), resolved); err == nil {
			first, model = string(b), resolved
		}
	}
	route, ok := cfg.Factory.Model(model)
	if !ok || route.Protocol != "responses" || (route.Upstream != "openai" && route.Upstream != "xai") {
		_ = websocket.Message.Send(ws, `{"type":"error","error":{"message":"Factory Responses WebSocket needs a configured responses route"}}`)
		_ = ws.Close()
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	affinity := frame.Factory.AssistantMessageID
	lease, err := s.pool.Acquire(ctx, cfg, pool.Request{Group: ck.Group, Model: model, RequestID: randHex(8), Affinity: affinity, Wait: 2 * time.Second})
	if err != nil {
		_ = websocket.Message.Send(ws, `{"type":"error","error":{"message":"no Factory account available"}}`)
		_ = ws.Close()
		return
	}
	defer lease.Release()
	acct := lease.Account
	if acct.Provider == config.ProviderFactory && acct.FactoryRefreshToken != "" {
		next, terr := s.factoryAccountToken(ctx, cfg, acct)
		if terr != nil {
			_ = websocket.Message.Send(ws, `{"type":"error","error":{"message":"Factory OAuth token refresh failed"}}`)
			_ = ws.Close()
			return
		}
		acct = next
	}
	session := r.Header.Get("X-Session-Id")
	if session == "" {
		session = uuidFromDigest(randHex(32))
	}
	if frame.Factory.AssistantMessageID == "" {
		frame.Factory.AssistantMessageID = uuidFromDigest(randHex(32))
	}
	trace := frame.Factory.TraceContext.Traceparent
	if trace == "" {
		trace = factory.RandomTraceparent()
	}
	h := http.Header{}
	factory.ApplyClientHeaders(h)
	h.Set("User-Agent", cfg.Factory.EffectiveUserAgent(acct))
	h.Set("X-Client-Version", config.FactoryClientVersion(cfg.Factory.EffectiveUserAgent(acct)))
	// The captured Responses WebSocket handshake carries no traceparent; the
	// trace context lives in the first frame instead.
	factory.ApplyProtocolForwardHeaders(h, "responses", route.Upstream, session, frame.Factory.AssistantMessageID, cfg.Factory.EffectiveRequestSurface(), "")
	h.Set("Authorization", "Bearer "+acct.RuntimeKey)
	if cfg.Factory.OpenAIPlatform != "" {
		h.Set("OpenAI-Platform", cfg.Factory.OpenAIPlatform)
	}
	if acct.FactoryWorkOSOrgID != "" {
		h.Set("X-Factory-Org-Id", acct.FactoryWorkOSOrgID)
	} else if acct.FactoryOrgID != "" {
		h.Set("X-Factory-Org-Id", acct.FactoryOrgID)
	}
	up, err := s.dialFactoryWS(ctx, cfg, acct, h)
	if err != nil {
		_ = websocket.Message.Send(ws, `{"type":"error","error":{"message":"Factory Responses WebSocket upstream failed"}}`)
		_ = ws.Close()
		return
	}
	defer up.Close()
	up.MaxPayloadBytes = 32 << 20
	_ = ws.SetDeadline(time.Time{})
	if err := websocket.Message.Send(up, first); err != nil {
		_ = websocket.Message.Send(ws, `{"type":"error","error":{"message":"Factory Responses WebSocket write failed"}}`)
		_ = ws.Close()
		return
	}
	lease.MarkSent()
	errs := make(chan error, 2)
	relay := func(dst, src *websocket.Conn) {
		for {
			var msg string
			if err := websocket.Message.Receive(src, &msg); err != nil {
				errs <- err
				return
			}
			if err := websocket.Message.Send(dst, msg); err != nil {
				errs <- err
				return
			}
		}
	}
	go relay(up, ws)
	go relay(ws, up)
	<-errs
	_ = ws.Close()
	_ = up.Close()
}

// dialFactoryWS upgrades to Factory's Responses WebSocket through the account's
// proxy. HTTP CONNECT and SOCKS5 are supported; no direct fallback is made for
// a configured proxy.
func (s *Server) dialFactoryWS(ctx context.Context, cfg *config.Config, a config.Account, h http.Header) (*websocket.Conn, error) {
	base := cfg.InferenceBase(a, "openai")
	u, err := url.Parse(base + "/responses/ws")
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil, fmt.Errorf("unsupported Factory inference scheme")
	}
	conn, err := dialWSConn(ctx, a.ProxyURL, u.Host, u.Scheme == "wss")
	if err != nil {
		return nil, err
	}
	// The capture's upstream handshake carries no Origin and asks for no
	// permessage-deflate, so the client presents the same minimal frame:
	// x/net/websocket always sends Origin from this config field, and reporting
	// a page origin here would be a browser fingerprint droid never showed.
	wc := &websocket.Config{Location: u, Origin: &url.URL{Scheme: "https", Host: u.Host}, Version: websocket.ProtocolVersionHybi13, Header: h}
	ws, err := websocket.NewClient(wc, conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return ws, nil
}

func dialWSConn(ctx context.Context, proxyURL, host string, secure bool) (net.Conn, error) {
	addr := host
	if _, _, err := net.SplitHostPort(addr); err != nil {
		if secure {
			addr = net.JoinHostPort(addr, "443")
		} else {
			addr = net.JoinHostPort(addr, "80")
		}
	}
	var conn net.Conn
	var err error
	if proxyURL == "" {
		d := &net.Dialer{Timeout: 15 * time.Second}
		conn, err = d.DialContext(ctx, "tcp", addr)
	} else {
		u, perr := url.Parse(proxyURL)
		if perr != nil {
			return nil, perr
		}
		switch strings.ToLower(u.Scheme) {
		case "socks5", "socks5h":
			d, derr := proxy.FromURL(u, proxy.Direct)
			if derr != nil {
				return nil, derr
			}
			conn, err = d.Dial("tcp", addr)
		case "http", "https":
			conn, err = dialHTTPProxy(ctx, u, addr)
		default:
			return nil, fmt.Errorf("unsupported proxy scheme for WebSocket")
		}
	}
	if err != nil {
		return nil, err
	}
	if secure {
		hostOnly, _, _ := net.SplitHostPort(addr)
		tc := tls.Client(conn, &tls.Config{ServerName: hostOnly, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	return conn, nil
}

func dialHTTPProxy(ctx context.Context, u *url.URL, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 15 * time.Second}
	proxyAddr := u.Host
	if _, _, err := net.SplitHostPort(proxyAddr); err != nil {
		if u.Scheme == "https" {
			proxyAddr = net.JoinHostPort(proxyAddr, "443")
		} else {
			proxyAddr = net.JoinHostPort(proxyAddr, "80")
		}
	}
	conn, err := d.DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "https" {
		hostOnly, _, _ := net.SplitHostPort(proxyAddr)
		tc := tls.Client(conn, &tls.Config{ServerName: hostOnly, MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tc
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", addr, addr)
	if u.User != nil {
		p, _ := u.User.Password()
		auth := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + p))
		fmt.Fprintf(&b, "Proxy-Authorization: Basic %s\r\n", auth)
	}
	b.WriteString("\r\n")
	if _, err := conn.Write([]byte(b.String())); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT HTTP %d", resp.StatusCode)
	}
	// A 200 CONNECT response's Body aliases the tunnel; closing it would close
	// the connection we are about to hand back.
	if br.Buffered() > 0 {
		// CONNECT responses have no body; bytes here belong to the tunnel.
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// bufferedConn keeps bytes read past the CONNECT response headers.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
