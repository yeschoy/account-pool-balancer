package searchgateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"account-pool-balancer/internal/websearch"
)

type Gateway struct {
	cfg     Config
	routes  map[string]Route
	client  *http.Client
	search  websearch.Backend
	history *historyStore
	sem     chan struct{}
	log     *slog.Logger
}
type Options struct {
	Client *http.Client
	Search websearch.Backend
	Logger *slog.Logger
}

// New accepts a Config returned by ReadConfig. Credentials have been resolved
// into private fields, and are never serialized into diagnostics or examples.
func New(c Config, opt Options) (*Gateway, error) {
	if c.MaxConcurrent < 1 || c.RequestTimeoutSec < 1 || len(c.Routes) == 0 {
		return nil, errors.New("resolve gateway configuration before New")
	}
	client := http.Client{Transport: http.DefaultTransport}
	if opt.Client != nil {
		client = *opt.Client
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	g := &Gateway{cfg: c, client: &client, search: opt.Search, history: newHistory(c), routes: map[string]Route{}, sem: make(chan struct{}, c.MaxConcurrent), log: opt.Logger}
	for _, r := range c.Routes {
		g.routes[r.ID] = r
	}
	if g.search == nil {
		g.search = &websearch.Tavily{Endpoint: c.Search.Endpoint, APIKey: c.Search.key, Timeout: time.Duration(c.Search.TimeoutSec) * time.Second, MaxResults: c.Search.MaxResults, Client: &client}
	}
	return g, nil
}
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/healthz" && r.Method == "GET" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/r/"), "/", 2)
	if !strings.HasPrefix(r.URL.Path, "/r/") || len(parts) != 2 {
		g.writeError(w, &gatewayError{404, "route_not_found", "Use a configured /r/<route>/v1 endpoint"})
		return
	}
	route, ok := g.routes[parts[0]]
	if !ok {
		g.writeError(w, &gatewayError{404, "route_not_found", "Route not found"})
		return
	}
	h := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	want := sha256.Sum256([]byte("Bearer " + route.token))
	if subtle.ConstantTimeCompare(h[:], want[:]) != 1 {
		g.writeError(w, &gatewayError{401, "invalid_api_key", "Invalid gateway route credential"})
		return
	}
	path := "/" + parts[1]
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		g.writeError(w, invalid("unsupported_query", "Query parameters are not supported"))
		return
	}
	if !(r.Method == "GET" && path == "/v1/models" || r.Method == "POST" && (path == "/v1/responses" || path == "/v1/chat/completions")) {
		g.writeError(w, &gatewayError{404, "unsupported_endpoint", "Endpoint is not supported"})
		return
	}
	select {
	case g.sem <- struct{}{}:
		defer func() { <-g.sem }()
	default:
		g.writeError(w, &gatewayError{429, "gateway_capacity", "Gateway concurrency limit reached"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(g.cfg.RequestTimeoutSec)*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	start := time.Now()
	defer func() {
		if g.log != nil {
			g.log.Info("gateway request finished", "route", route.ID, "mode", route.Mode, "path", path, "durationMs", time.Since(start).Milliseconds())
		}
	}()
	var body []byte
	if r.Method == "POST" {
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, g.cfg.MaxBodyBytes+1))
		if err != nil || int64(len(body)) > g.cfg.MaxBodyBytes {
			g.writeError(w, &gatewayError{413, "request_limit", "Request exceeds gateway body limit"})
			return
		}
	}
	var o object
	has, history := false, false
	if r.Method == "POST" {
		var err error
		o, err = decodeObject(body)
		if err != nil {
			g.writeError(w, invalid("invalid_request", "Request must be unambiguous JSON object"))
			return
		}
		if len(route.AllowedModels) > 0 {
			model := str(o["model"])
			allowed := false
			for _, m := range route.AllowedModels {
				allowed = allowed || m == model
			}
			if !allowed {
				g.writeError(w, invalid("model_not_allowed", "Model is not permitted on this route"))
				return
			}
		}
		has, err = hasSearch(o)
		if err != nil {
			g.writeError(w, err)
			return
		}
		history = containsHistory(o)
	}
	if route.Mode == "disabled" && (has || history) {
		g.writeError(w, invalid("web_search_disabled", "Hosted web search is disabled on this route"))
		return
	}
	if route.Mode != "compat" || !has && !history {
		g.passthrough(w, r, route, path, body)
		return
	}
	if path != "/v1/responses" {
		g.writeError(w, invalid("unsupported_search_protocol", "Search compatibility currently supports Responses only"))
		return
	}
	p, err := g.prepare(o, g.history.scope(route, r, o))
	if err != nil {
		g.writeError(w, err)
		return
	}
	if route.RequirePoolOwner {
		if err := g.checkPoolOwner(r, route); err != nil {
			g.writeError(w, err)
			return
		}
		p.owner = &ownerTokenState{token: "start"}
	}
	emit := newEmitter(w, p.stream, str(o["model"]))
	emit.publicFields = o
	err = g.run(r, route, p, emit)
	if g.log != nil {
		code := "success"
		if err != nil {
			code = safeError(err).Code
		}
		g.log.Info("search compatibility finished", "route", route.ID, "code", code,
			"modelCalls", emit.modelCalls, "searchCalls", emit.searchCalls, "usageComplete", emit.usage != nil,
			"searchCredits", emit.searchCredits, "searchCreditsComplete", emit.creditsComplete)
	}
	if err != nil {
		if emit.started {
			emit.fail(err)
		} else {
			g.writeError(w, err)
		}
	}
}
func (g *Gateway) request(r *http.Request, route Route, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, route.UpstreamBaseURL+strings.TrimPrefix(path, "/v1"), bytes.NewReader(body))
	if err != nil {
		return nil, upstream("upstream_request_error")
	}
	// Only known harmless/session headers cross this boundary. Caller auth and
	// any proxy/host/forwarding override can never replace configured credentials.
	for _, name := range []string{"User-Agent", "X-Session-Id", "Session-Id", "Thread-Id", "X-Codex-Session-Id", "X-Pi-Session-Id", "X-Prompt-Cache-Key", "Prompt-Cache-Key", "X-Affinity"} {
		if v := r.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	req.Header.Set("Authorization", "Bearer "+route.upstreamKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if route.RequirePoolOwner && path == "/v1/responses" {
		if state, ok := r.Context().Value(ownerContextKey{}).(*ownerTokenState); ok {
			req.Header.Set(ownerHeader, state.token)
		}
	}
	resp, err := g.client.Do(req)
	if err != nil {
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		return nil, upstream("upstream_transport_error")
	}
	return resp, nil
}
func (g *Gateway) passthrough(w http.ResponseWriter, r *http.Request, route Route, path string, body []byte) {
	resp, e := g.request(r, route, path, body)
	if e != nil {
		g.writeError(w, e)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		g.writeError(w, upstream("upstream_redirect_blocked"))
		return
	}
	for _, k := range []string{"Content-Type", "Content-Encoding", "Retry-After", "X-Request-Id"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// No buffering or protocol rewriting on the native path. A hard transfer cap
	// closes oversized streams; it never fabricates a protocol terminal.
	n, e := io.Copy(flushingWriter{w}, io.LimitReader(resp.Body, g.cfg.MaxResponseBytes))
	if e != nil || n > g.cfg.MaxResponseBytes {
		return
	}
}

type flushingWriter struct{ w http.ResponseWriter }

func (f flushingWriter) Write(p []byte) (int, error) {
	n, e := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, e
}
func (g *Gateway) writeError(w http.ResponseWriter, err error) {
	e := safeError(err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": "gateway_error", "code": e.Code, "message": e.Message}})
}
func safeError(err error) *gatewayError {
	var e *gatewayError
	if errors.As(err, &e) {
		return e
	}
	var s *websearch.Error
	if errors.As(err, &s) {
		return &gatewayError{502, s.Code, "Search backend failed; no search result was fabricated"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &gatewayError{504, "gateway_timeout", "Gateway request deadline exceeded"}
	}
	return &gatewayError{502, "gateway_execution_failed", "Gateway execution failed; the request was not replayed"}
}
