package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/egressid"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/pool"
	"account-pool-balancer/internal/quota"
	"account-pool-balancer/internal/rules"
	"account-pool-balancer/internal/stats"
)

// ---- log records: strict projections, never a request, account or error object ----

type attemptLog struct {
	Account        string `json:"account"`
	AccountName    string `json:"accountName"`
	Status         int    `json:"status"`
	UpstreamStatus *int   `json:"upstreamStatus"`
	Ms             int64  `json:"ms"`
	Action         string `json:"action,omitempty"`
	RuleID         string `json:"ruleId,omitempty"`
	Egress         string `json:"egress"`
	EgressIP       string `json:"egressIp,omitempty"`
	Terminal       string `json:"terminal"`
}

type workflowDecision struct {
	Account  string   `json:"account"`
	Reason   string   `json:"reason"`
	Binding  string   `json:"binding"`
	Overflow bool     `json:"overflow"`
	Order    []string `json:"order,omitempty"`
}

type requestLog struct {
	failureDetails      map[int]bool // bounded by attempts; not serialized
	TS                  time.Time    `json:"ts"`
	RequestID           string       `json:"requestId"`
	ClientKeyID         string       `json:"clientKeyId,omitempty"`
	ClientKeyName       string       `json:"clientKeyName,omitempty"`
	Group               string       `json:"group,omitempty"`
	Provider            string       `json:"provider,omitempty"`
	Path                string       `json:"path"`
	InboundProtocol     string       `json:"inboundProtocol"`
	Protocol            string       `json:"protocol"`
	Model               string       `json:"model"`
	Stream              bool         `json:"stream"`
	AccountID           string       `json:"accountId,omitempty"`
	AccountName         string       `json:"accountName,omitempty"`
	Proxy               string       `json:"proxy,omitempty"` // egress label: proxy host:port or "direct"
	SelectionReason     string       `json:"selectionReason,omitempty"`
	Selector            string       `json:"selector"`
	Overflow            bool         `json:"overflow"`
	Switched            bool         `json:"switched"`
	ClientFamily        string       `json:"clientFamily,omitempty"`
	CacheHintSource     string       `json:"cacheHintSource,omitempty"`
	CacheAffinityResult string       `json:"cacheAffinityResult,omitempty"`
	CacheControl        bool         `json:"cacheControl,omitempty"`
	EgressStatus        string       `json:"egressStatus,omitempty"`
	EgressIP            string       `json:"egressIp,omitempty"`
	SessionSource       string       `json:"sessionSource"`
	AffinityKeyType     string       `json:"affinityKeyType"`
	AffinityConfidence  string       `json:"affinityConfidence"`
	BindingResult       string       `json:"bindingResult"`
	Status              int          `json:"status"`
	Result              string       `json:"result"`
	UpstreamStatus      *int         `json:"upstreamStatus"`
	ErrorCategory       string       `json:"errorCategory,omitempty"`
	BlockedBy           string       `json:"blockedBy,omitempty"`
	RetryAfter          *int         `json:"retryAfter,omitempty"`
	DurationMs          int64        `json:"durationMs"`
	UseTimeSec          *int64       `json:"useTimeSec,omitempty"`
	AverageTPS          *float64     `json:"averageTps,omitempty"`
	TTFTMs              *int64       `json:"ttftMs"`
	UpstreamDurationMs  *int64       `json:"upstreamDurationMs,omitempty"`
	UpstreamOutputMs    *int64       `json:"upstreamOutputMs,omitempty"`
	UpstreamTPS         *float64     `json:"upstreamTps,omitempty"`
	UpstreamTPSBasis    string       `json:"upstreamTpsBasis,omitempty"`
	InputTokens         *int64       `json:"inputTokens"`
	OutputTokens        *int64       `json:"outputTokens"`
	CachedTokens        *int64       `json:"cachedTokens"`
	CacheCreationTokens *int64       `json:"cacheCreationTokens,omitempty"`
	ReasoningTokens     *int64       `json:"reasoningTokens,omitempty"`
	CacheHit            *bool        `json:"cacheHit"`
	CacheRate           *float64     `json:"cacheRate"`      // cached share of the prompt, 0..1
	Cost                *float64     `json:"cost,omitempty"` // in Currency, from the price table
	Currency            string       `json:"currency,omitempty"`
	UpstreamRequestID   string       `json:"upstreamRequestId,omitempty"` // x-request-id of the upstream response that was delivered
	// Conversations is how many distinct accounts this request tried while a
	// conversation fingerprint was present. It stays 1 while every attempt
	// remained on one account, and only exceeds 1 when a failover handed one
	// conversation's history to a second Perch identity.
	Conversations int `json:"conversations,omitempty"`
	// CrossAccount marks a request whose failover continued a conversation on a
	// different account than the one that started it. The upstream then sees one
	// chat split across two identities, which is the exact shape per-account
	// isolation exists to prevent, so it is recorded rather than left silent.
	CrossAccount     bool               `json:"crossAccount,omitempty"`
	CrossAccountFrom string             `json:"crossAccountFrom,omitempty"`
	Attempts         []attemptLog       `json:"attempts"`
	Workflow         []workflowDecision `json:"workflow,omitempty"`

	upstreamDetails    []upstreamWireLog // sanitized, attached only to detail logs
	upstreamIdentities []string          // ephemeral response secrets; never serialized
}

type errorLog struct {
	TS                    time.Time           `json:"ts"`
	RequestID             string              `json:"requestId"`
	AttemptIndex          int                 `json:"attemptIndex"`
	ClientKeyID           string              `json:"clientKeyId,omitempty"`
	ClientKeyName         string              `json:"clientKeyName,omitempty"`
	Provider              string              `json:"provider,omitempty"`
	AccountID             string              `json:"accountId"`
	AccountName           string              `json:"accountName"`
	Model                 string              `json:"model"`
	Status                int                 `json:"status"`
	UpstreamStatus        *int                `json:"upstreamStatus"`
	Origin                string              `json:"origin,omitempty"`
	Egress                string              `json:"egress,omitempty"`
	EgressIP              string              `json:"egressIp,omitempty"`
	Category              string              `json:"category"`
	Terminal              string              `json:"terminal"`
	Reason                string              `json:"reason"`
	ReasonTruncated       bool                `json:"reasonTruncated"`
	RuleID                string              `json:"ruleId,omitempty"`
	RuleScope             string              `json:"ruleScope,omitempty"`
	RuleAction            string              `json:"ruleAction,omitempty"`
	MatchedBy             string              `json:"matchedBy"`
	RetryRuleID           string              `json:"retryRuleId,omitempty"`
	RetryDecision         string              `json:"retryDecision,omitempty"`
	HealthAction          string              `json:"healthAction"`
	RetryAfterMs          *int64              `json:"retryAfterMs,omitempty"`
	Switched              bool                `json:"switched"`
	ResponseContentType   string              `json:"responseContentType,omitempty"`
	ResponseBytes         int                 `json:"responseBytes"`
	ResponseBodyTruncated bool                `json:"responseBodyTruncated,omitempty"`
	MistralValidation     []mistralValidation `json:"mistralValidation,omitempty"`
}

// cacheRate is the cached share of the prompt. Messages usage reports input
// without the cache reads; Chat and Responses usage includes them.
func cacheRate(in, cached *int64, anthropic bool) *float64 {
	if in == nil || cached == nil {
		return nil
	}
	total := *in
	if anthropic {
		total += *cached
	}
	if total <= 0 {
		return nil
	}
	r := min(float64(*cached)/float64(total), 1)
	return &r
}

// LogKeys are the logstore filter keys (logstore.Filters), as it would read
// them back from the JSON.
func (r *requestLog) LogKeys() map[string]string {
	keys := map[string]string{"requestId": r.RequestID, "accountId": r.AccountID, "status": strconv.Itoa(r.Status),
		"result": r.Result, "model": r.Model, "errorCategory": r.ErrorCategory, "bindingResult": r.BindingResult,
		"provider": r.Provider, "clientKeyId": r.ClientKeyID, "stream": strconv.FormatBool(r.Stream), "protocol": r.Protocol,
		"affinityKeyType": r.AffinityKeyType, "affinityConfidence": r.AffinityConfidence, "clientFamily": r.ClientFamily, "cacheAffinityResult": r.CacheAffinityResult}
	// Only present when true, so the filter does not gain a column that is
	// "false" for almost every request.
	if r.CrossAccount {
		keys["crossAccount"] = "true"
	}
	return keys
}

func (e errorLog) LogKeys() map[string]string {
	return map[string]string{"requestId": e.RequestID, "accountId": e.AccountID, "status": strconv.Itoa(e.Status),
		"category": e.Category, "ruleId": e.RuleID, "model": e.Model, "clientKeyId": e.ClientKeyID, "provider": e.Provider, "origin": e.Origin}
}

// ---- attempt results ----

type outcome int

const (
	outDelivered outcome = iota // a response went to the client
	outFailed                   // nothing written: may switch accounts
	outCancelled                // the client went away
)

type usage struct {
	in, out, cached       *int64
	cacheWrite, reasoning *int64
}

type attemptResult struct {
	kind              outcome
	result            string // success | failed, for delivered
	status            int
	upstreamStatus    int
	category          string // upstream | network | timeout | proxy
	terminal          string // success | upstream_http | upstream_envelope | timeout | proxy | network
	body              []byte
	bodyTruncated     bool // Mistral error prefix hit its bounded read cap
	header            http.Header
	contentType       string
	sent              atomic.Bool
	upstreamSession   string
	egressIP          string
	goRecord          *requestLog
	privateIdentities []string // request-local only; never serialized
	wrapped           bool
	accountScoped     bool
	evaluated         bool
	retryAfter        time.Duration
	// downstreamStatus / ruleID come from the matched error rule
	downstreamStatus int
	ruleID           string
	ttft             *int64
	upstreamStarted  time.Time
	upstreamFirst    time.Time
	upstreamLast     time.Time
	upstreamStream   bool
	usage            usage
	ms               int64
	// turn is what this attempt asked and answered, for the Perch pool's
	// cli-turn record. It is filled by the Perch path only and never logged
	// as text: the reporter redacts it to digests before writing.
	turn perchTurn
	// turnToken / turnAccount identify the per-account credential the
	// cli-turn record is posted with (Perch pool only).
	turnToken   string
	turnAccount config.Account
	turnStarted bool
}

// setUpstreamRequestID exposes the upstream's own request id so downstream gateways can quote it to the provider.
func setUpstreamRequestID(dst, upstream http.Header) {
	if id := upstream.Get("X-Request-Id"); id != "" {
		dst.Set("X-Upstream-Request-Id", id)
	}
}

func newRequestID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "req_" + hex.EncodeToString(b)
}

func clientKeyFrom(r *http.Request) string {
	if a := r.Header.Get("Authorization"); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	if k := r.Header.Get("X-Api-Key"); k != "" {
		return strings.TrimSpace(k)
	}
	return strings.TrimSpace(r.Header.Get("X-Goog-Api-Key"))
}

func (s *Server) authClient(cfg *config.Config, r *http.Request) (config.ClientKey, bool) {
	k := clientKeyFrom(r)
	if k == "" {
		return config.ClientKey{}, false
	}
	return cfg.ClientKeyByHash(config.HashKey(k))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeLocalError answers in the inbound protocol's error shape.
// riverPath: the inference endpoints River offers (OpenAI Chat Completions
// and Responses, including sub-paths such as /v1/responses/{id}).
func riverPath(p string) bool {
	for _, base := range []string{"/v1/chat/completions", "/v1/responses"} {
		if p == base || strings.HasPrefix(p, base+"/") {
			return true
		}
	}
	return false
}

// ollamaPath: what Ollama serves under /v1 — OpenAI Chat Completions and
// (stateless) Responses, Anthropic Messages, plus completions and embeddings.
// Its cloud answers /v1/messages as well as the OpenAI shapes, so all three
// protocols this proxy speaks pass through unchanged.
func ollamaPath(p string) bool {
	for _, base := range []string{"/v1/chat/completions", "/v1/completions", "/v1/responses", "/v1/messages", "/v1/embeddings"} {
		if p == base || strings.HasPrefix(p, base+"/") {
			return true
		}
	}
	return false
}

// modalPath: what a Modal Shared Endpoint serves — OpenAI Chat Completions,
// OpenAI Responses and Anthropic Messages all at one host
// (inference.<region>.modal.direct). Embeddings exist only on Endpoint types
// this pool does not schedule, and the path answers 404 without one, so it is
// left out and refused locally like River's.
func modalPath(p string) bool {
	for _, base := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		if p == base || strings.HasPrefix(p, base+"/") {
			return true
		}
	}
	return false
}

// perchPath: what the Perch relay can translate. Chat Completions and
// Responses are translated; /v1/models is answered by this service from the
// catalogue it synced (Perch itself has no OpenAI-compatible listing).
func perchPath(p string) bool {
	for _, base := range []string{"/v1/chat/completions", "/v1/responses", "/v1/models"} {
		if p == base || strings.HasPrefix(p, base+"/") {
			return true
		}
	}
	return false
}

// poolPathOK says whether an account's pool serves the path.
//
// The Perch pool serves OpenAI Chat Completions and Responses: its model-call
// proxy takes Perch's own envelope and answers with Perch's event stream, and
// attemptPerch translates in both directions. Completions/embeddings have no
// Perch equivalent, so they are refused locally rather than sent upstream.
func poolPathOK(provider, path string) bool {
	switch provider {
	case config.ProviderOpenCodeZen, config.ProviderOpenCodeGo:
		return opencode.FromPath(path) != "" || path == "/v1/models"
	case config.ProviderMistral:
		return path == "/v1/chat/completions" || path == "/v1/responses" || path == "/v1/responses/input_tokens" || path == "/v1/fim/completions" || path == "/v1/embeddings"
	case config.ProviderTokenHarbor:
		return path == "/v1/chat/completions" || path == "/v1/responses" || path == "/v1/messages"
	case config.ProviderPerch:
		return perchPath(path)
	case config.ProviderRiver:
		return riverPath(path)
	case config.ProviderOllama:
		return ollamaPath(path)
	case config.ProviderModal:
		return modalPath(path)
	}
	return true
}

func writeLocalError(w http.ResponseWriter, proto string, status int, typ, code, msg string, retryAfter int) {
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	if proto == "anthropic" {
		writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": msg}})
		return
	}
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": typ, "code": code}})
}

func (s *Server) handleV1(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/models" && r.Method == http.MethodGet {
		s.handleModels(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeLocalError(w, "openai", http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed", "method not allowed", 0)
		return
	}
	s.handleProxy(w, r)
}

func ceilSeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}

func (s *Server) handleProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	cfg := s.cfg.Get() // one snapshot for the whole request
	rid := newRequestID()
	proto := "openai"
	if strings.HasPrefix(r.URL.Path, "/v1/messages") {
		proto = "anthropic"
	}
	var cw *captureWriter
	var reqHeader http.Header
	var reqBody []byte
	var reqBodyIncomplete bool
	var detailIdentities []string
	detailEnabled := cfg.Logs.Detail
	if detailEnabled {
		cw = &captureWriter{ResponseWriter: w, max: cfg.Logs.DetailBodyKB << 10}
		w, reqHeader = cw, r.Header.Clone()
	}
	w.Header().Set("X-Request-Id", rid)
	rec := &requestLog{TS: start.UTC(), RequestID: rid, Path: r.URL.Path, InboundProtocol: proto, Protocol: protocolOf(r.URL.Path), Selector: cfg.Routing.Strategy,
		SessionSource: "none", AffinityKeyType: "none", AffinityConfidence: "none", BindingResult: "not-applicable", Attempts: []attemptLog{}}
	var res *attemptResult
	// turnReported makes the cli-turn record fire at most once per request: a
	// request that switched accounts left several attempts, and only the
	// attempt whose outcome was delivered describes the turn the caller saw.
	var turnReported bool
	finish := func(status int, result string) {
		rec.Status, rec.Result = status, result
		ended := time.Now()
		rec.DurationMs = ended.Sub(start).Milliseconds()
		if res != nil {
			rec.InputTokens, rec.OutputTokens, rec.CachedTokens = res.usage.in, res.usage.out, res.usage.cached
			rec.CacheCreationTokens, rec.ReasoningTokens = res.usage.cacheWrite, res.usage.reasoning
			if res.usage.cached != nil {
				hit := *res.usage.cached > 0
				rec.CacheHit = &hit
			}
			rec.TTFTMs = res.ttft
			if rec.Provider == config.ProviderMistral {
				applyUpstreamTiming(rec, res)
			}
			if res.upstreamStatus != 0 {
				us := res.upstreamStatus
				rec.UpstreamStatus = &us
			}
		}
		applyAverageTiming(rec, ended)
		// The Perch pool reports its turn to the same places the CLI does.
		// It runs after the log line is written so the record can cite the
		// request id, and it is synchronous by design: the reporter's budget
		// is short (12 s streaming, 3×45 s final) and with egress off it only
		// writes a log line.
		if !turnReported && rec.Provider == config.ProviderPerch && res != nil {
			turnReported = true
			s.reportPerchTurn(cfg, res.turnAccount, rec, res, start)
		}
		anthropic := proto == "anthropic"
		rec.CacheRate = cacheRate(rec.InputTokens, rec.CachedTokens, anthropic)
		if config.StrictResponses(rec.Provider) && anthropic && rec.InputTokens != nil && rec.CacheCreationTokens != nil {
			inputWithWrites := *rec.InputTokens + *rec.CacheCreationTokens
			rec.CacheRate = cacheRate(&inputWithWrites, rec.CachedTokens, true)
		}
		var cost float64
		// The shared price table has no cache-write price. Do not report an
		// understated Token Harbor bill when the upstream measured writes.
		unpricedWrites := rec.Provider == config.ProviderTokenHarbor && rec.CacheCreationTokens != nil && *rec.CacheCreationTokens > 0
		if c, cur, ok := cfg.Pricing.Cost(rec.Model, rec.TS, rec.InputTokens, rec.CachedTokens, rec.OutputTokens, anthropic); ok && !unpricedWrites {
			cost, rec.Cost, rec.Currency = c, &c, cur
		}
		if (rec.Provider == config.ProviderOpenCodeGo || rec.Provider == config.ProviderTokenHarbor) && result == "failed" && len(rec.Attempts) == 0 {
			s.logs.Append("errors", rec.TS, errorLog{TS: rec.TS, RequestID: rid, Provider: rec.Provider, ClientKeyID: rec.ClientKeyID, ClientKeyName: rec.ClientKeyName, AccountID: rec.AccountID, AccountName: rec.AccountName, Model: rec.Model, Status: status, Category: rec.ErrorCategory, Terminal: "local_block", Origin: "local", Egress: rec.Proxy, Reason: rec.Provider + " gateway blocked " + rec.ErrorCategory, MatchedBy: "gateway", HealthAction: "none", RetryDecision: "stop"})
		}
		s.logs.Append("requests", rec.TS, rec)
		if cw != nil && detailEnabled && (rec.Provider != config.ProviderTokenHarbor || result != "failed" || len(rec.failureDetails) == 0) {
			max := cw.max
			d := &detailLog{TS: rec.TS, RequestID: rid, Protocol: rec.Protocol, Method: r.Method, Path: r.URL.Path, Model: rec.Model,
				ClientKeyID: rec.ClientKeyID, ClientKeyName: rec.ClientKeyName, Provider: rec.Provider, AccountID: rec.AccountID, Status: status, Result: result, RequestHeaders: maskHeaders(cfg, reqHeader), RequestBodyBytes: len(reqBody),
				ResponseHeaders: maskHeaders(cfg, cw.Header()), ResponseBodyBytes: cw.n}
			if rec.Provider == config.ProviderMistral {
				d.WireVersion, d.UpstreamAttempts = 1, rec.upstreamDetails
				detailIdentities = append(detailIdentities, rec.upstreamIdentities...)
			}
			d.RequestBodyTruncated = reqBodyIncomplete
			capturedBody := reqBody
			if rec.Provider == config.ProviderOpenCodeGo {
				d.RequestHeaders = maskHeaders(cfg, goPrivateHeaders(reqHeader))
				for _, name := range cfg.Routing.SessionHeaders {
					for key := range d.RequestHeaders {
						if strings.EqualFold(key, name) {
							d.RequestHeaders[key] = []string{"[REDACTED]"}
						}
					}
				}
				d.ResponseHeaders = maskHeaders(cfg, goPrivateHeaders(cw.Header()))
				capturedBody = goPrivateBody(reqBody)
			}
			if rec.Provider == config.ProviderTokenHarbor || rec.Provider == config.ProviderMistral {
				d.RequestHeaders = maskHeaders(cfg, thPrivateHeaders(reqHeader))
				for _, name := range cfg.Routing.SessionHeaders {
					for key := range d.RequestHeaders {
						if strings.EqualFold(key, name) {
							d.RequestHeaders[key] = []string{"[REDACTED]"}
						}
					}
				}
				d.ResponseHeaders = maskHeaders(cfg, thPrivateHeaders(cw.Header()))
				var cut bool
				capturedBody, cut = thBodyPreview(reqBody, detailIdentities, max)
				d.RequestBodyTruncated = d.RequestBodyTruncated || cut
			}
			var reqCut bool
			d.RequestBody, reqCut = maskBody(cfg, capturedBody, max)
			d.RequestBodyTruncated = d.RequestBodyTruncated || reqCut
			respBody, cut := cw.body()
			if rec.Provider == config.ProviderOpenCodeGo {
				respBody = goPrivateBody(respBody)
			} else if rec.Provider == config.ProviderTokenHarbor || rec.Provider == config.ProviderMistral {
				respBody = thPrivateBody(respBody, detailIdentities)
			}
			d.ResponseBody, _ = maskBody(cfg, respBody, len(respBody))
			d.ResponseBodyTruncated = cut
			s.logs.Append("details", rec.TS, d)
		}
		var prompt int64
		if rec.InputTokens != nil {
			prompt = *rec.InputTokens
			if anthropic && rec.CachedTokens != nil {
				prompt += *rec.CachedTokens
			}
			if config.StrictResponses(rec.Provider) && anthropic && rec.CacheCreationTokens != nil {
				prompt += *rec.CacheCreationTokens
			}
		}
		s.stats.Record(stats.Sample{AccountID: rec.AccountID, Provider: rec.Provider, Proxy: rec.Proxy, Model: rec.Model, Result: result, Input: rec.InputTokens, Output: rec.OutputTokens,
			Cached: rec.CachedTokens, CacheWrite: rec.CacheCreationTokens, Prompt: prompt, Currency: rec.Currency, Cost: cost, BindingResult: rec.BindingResult, CacheAffinityResult: rec.CacheAffinityResult, DurationMs: rec.DurationMs, TTFTMs: rec.TTFTMs})
		s.statsDirty.Store(true)
	}

	ck, ok := s.authClient(cfg, r)
	if !ok {
		rec.ErrorCategory = "auth"
		writeLocalError(w, proto, http.StatusUnauthorized, "authentication_error", "invalid_api_key", "invalid API key", 0)
		finish(http.StatusUnauthorized, "failed")
		return
	}
	rec.ClientKeyID, rec.ClientKeyName, rec.Group = ck.ID, ck.Name, ck.Group
	rec.Provider = keyPool(cfg, ck)
	rootCfg := cfg
	cfg = rootCfg.ForProvider(rec.Provider)
	rec.Selector = cfg.Routing.Strategy
	if rec.Provider == config.ProviderTokenHarbor || rec.Provider == config.ProviderMistral {
		if rec.Provider == config.ProviderTokenHarbor {
			detailEnabled = cfg.TokenHarbor.DetailLog
		} else if cfg.Mistral.DetailLog != nil {
			detailEnabled = *cfg.Mistral.DetailLog
		}
		if !detailEnabled {
			// No bytes have been written yet. Do not capture this pool even
			// when the shared detail switch is on for other providers.
			if cw != nil {
				w, cw, reqHeader = cw.ResponseWriter, nil, nil
			}
		} else {
			if cw == nil {
				cw = &captureWriter{ResponseWriter: w, max: min(cfg.Logs.DetailBodyKB, 64) << 10}
				w, reqHeader = cw, r.Header.Clone()
			}
			cw.max = min(cw.max, 64<<10)
		}
	}
	if rec.Provider == config.ProviderOpenCodeGo || rec.Provider == config.ProviderTokenHarbor {
		rec.ClientFamily = goClientFamily(r.Header, nil)
	}
	if !poolPathOK(rec.Provider, r.URL.Path) {
		// River serves only OpenAI Chat Completions and Responses
		rec.ErrorCategory = "request"
		writeLocalError(w, proto, http.StatusNotFound, "invalid_request_error", "unsupported_endpoint",
			"this pool ("+rec.Provider+") does not serve "+r.URL.Path, 0)
		finish(http.StatusNotFound, "failed")
		return
	}
	var body []byte
	var err error
	releaseBody := func() {}
	if rec.Provider == config.ProviderTokenHarbor {
		body, releaseBody, err = s.readTHBody(w, r, int64(cfg.Routing.MaxBodyMB)<<20)
	} else {
		body, err = io.ReadAll(http.MaxBytesReader(w, r.Body, int64(cfg.Routing.MaxBodyMB)<<20))
	}
	defer releaseBody()
	reqBody, reqBodyIncomplete = body, err != nil
	if err != nil {
		if errors.Is(err, errTHBufferLimit) {
			rec.ErrorCategory = "gateway-buffer-limit"
			writeLocalError(w, proto, 429, "rate_limit_error", "gateway_buffer_limit", "gateway input buffer budget is full; upstream was not contacted", 1)
			finish(429, "failed")
			return
		}
		if errors.Is(err, errTHBodyLength) {
			rec.ErrorCategory = "request"
			writeLocalError(w, proto, 400, "invalid_request_error", "invalid_content_length", "request Content-Length mismatch", 0)
			finish(400, "failed")
			return
		}
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			rec.ErrorCategory = "request-timeout"
			writeLocalError(w, proto, 408, "invalid_request_error", "request_timeout", "request upload timed out", 0)
			finish(408, "failed")
			return
		}
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeLocalError(w, proto, http.StatusRequestEntityTooLarge, "invalid_request_error", "body_too_large", "request body too large", 0)
			rec.ErrorCategory = "request"
			finish(http.StatusRequestEntityTooLarge, "failed")
			return
		}
		finish(499, "client_cancelled")
		return
	}
	obj, err := parseRoutingBody(body, rec.Provider == config.ProviderTokenHarbor || rec.Provider == config.ProviderMistral)
	if err != nil {
		if rec.Provider == config.ProviderTokenHarbor || rec.Provider == config.ProviderMistral {
			redactionObject, _ := parseRequestBody(body) // identity removal only; never used for admission
			detailIdentities = thIdentityValues(r.Header, redactionObject, cfg.Routing.SessionHeaders)
		}
		rec.ErrorCategory = "request"
		message := "request body must be a JSON object"
		if rec.Provider == config.ProviderTokenHarbor || rec.Provider == config.ProviderMistral {
			message = "request body must be valid JSON with at most one lowercase model field"
		}
		writeLocalError(w, proto, http.StatusBadRequest, "invalid_request_error", "invalid_json", message, 0)
		finish(http.StatusBadRequest, "failed")
		return
	}
	if rec.Provider == config.ProviderTokenHarbor || rec.Provider == config.ProviderMistral {
		detailIdentities = thIdentityValues(r.Header, obj, cfg.Routing.SessionHeaders)
	}
	rec.Model, _ = obj["model"].(string)
	if rec.Provider == config.ProviderTokenHarbor && rec.Model == "" {
		rec.ErrorCategory = "request"
		writeLocalError(w, proto, 400, "invalid_request_error", "invalid_model", "TokenHarbor requires an explicit nonempty model for Pass enforcement", 0)
		finish(400, "failed")
		return
	}
	if rec.Provider == config.ProviderMistral {
		if rec.Model == "" {
			rec.ErrorCategory = "request"
			writeLocalError(w, proto, 400, "invalid_request_error", "invalid_model", "Mistral requires a nonempty model", 0)
			finish(400, "failed")
			return
		}
		// Mistral Responses is stateless: never silently drop server-side
		// continuation/storage fields or pick a different owner for them.
		if strings.HasPrefix(r.URL.Path, "/v1/responses") && (obj["previous_response_id"] != nil || obj["store"] != nil && obj["store"] != false) {
			rec.ErrorCategory = "request"
			writeLocalError(w, proto, 400, "invalid_request_error", "stateless_responses", "Mistral Responses is stateless; send full history in input, omit previous_response_id and use store=false", 0)
			finish(400, "failed")
			return
		}
	}
	rec.Stream, _ = obj["stream"].(bool)
	if !config.IsOpenCode(rec.Provider) && rec.Provider != config.ProviderTokenHarbor && rec.Provider != config.ProviderMistral {
		s.quota.NoteModel(rec.Model)
	} // OpenCode must not feed OpenDesign preflight coverage
	identityScope := ck.Group
	if config.StrictResponses(rec.Provider) || rec.Provider == config.ProviderMistral {
		identityScope += "\x00" + ck.ID
	}
	var ident Identity
	cacheAffinity := ""
	if rec.Provider == config.ProviderOpenCodeGo {
		ident, cacheAffinity, rec.CacheHintSource = s.goIdentity(cfg, identityScope, r.Header, obj)
		ident.UpstreamSession = s.goUpstreamSession(cfg, identityScope, r.Header, obj, ident)
		rec.ClientFamily = goClientFamily(r.Header, obj)
		rec.CacheControl = bytes.Contains(body, []byte(`"cache_control"`))
	} else if rec.Provider == config.ProviderTokenHarbor {
		rec.ClientFamily = goClientFamily(r.Header, obj)
		ident = s.tokenHarborIdentity(cfg, identityScope, r.Header, obj)
	} else if rec.Provider == config.ProviderMistral {
		ident = s.mistralIdentity(cfg, identityScope, r.Header, obj)
		rec.ClientFamily = goClientFamily(r.Header, obj)
	} else {
		ident = s.identity(cfg, identityScope, r.Header, obj)
	}
	stateful := config.StrictResponses(rec.Provider) && lookup(obj, "previous_response_id") != ""
	routeGroup := ck.Group
	fallbackGroup := cfg.OpenCode.FallbackGroup(rec.Provider)
	usedFallback := false
	if config.IsOpenCode(rec.Provider) && ident.Fingerprint != "" {
		if id, ok := s.pool.BoundAccount(ident.Fingerprint); ok {
			if a, ok := rootCfg.Account(id); ok && (a.Group == ck.Group || a.Group == fallbackGroup) {
				routeGroup = a.Group
				usedFallback = routeGroup != ck.Group
				cfg = rootCfg.ForProvider(a.Provider)
			}
		}
	}
	rec.SessionSource, rec.AffinityKeyType, rec.AffinityConfidence = ident.Source, ident.KeyType, ident.Confidence
	if rec.Provider == config.ProviderMistral {
		var generatedCacheKey string
		body, generatedCacheKey, err = prepareMistralRequest(body, r.URL.Path, ident.Fingerprint)
		if err != nil {
			rec.ErrorCategory = "request"
			writeLocalError(w, proto, 400, "invalid_request_error", "invalid_mistral_request", err.Error(), 0)
			finish(400, "failed")
			return
		}
		if generatedCacheKey != "" {
			detailIdentities = append(detailIdentities, generatedCacheKey)
			rec.CacheHintSource = "session_hmac"
		}
	}

	excluded := map[string]bool{}
	if config.IsOpenCode(rec.Provider) {
		for _, a := range cfg.Accounts {
			if (a.Group == ck.Group || a.Group == fallbackGroup) && s.openCodeProxyView(a.ProxyURL).Status == "unhealthy" {
				excluded[a.ID] = true
			}
		}
	}
	// triedAccounts and firstAccount track whether one conversation's history
	// was handed to a second account (see CrossAccount on requestLog).
	triedAccounts := map[string]bool{}
	firstAccount := ""
	var last *attemptResult
	strippedTools := 0
	maxAttempts := rootCfg.ForProvider(keyPool(rootCfg, ck)).Routing.MaxAccounts
	for i := 0; i < maxAttempts; i++ {
		wait := time.Duration(cfg.Routing.ConcurrencyWaitMs) * time.Millisecond
		if i > 0 {
			wait = 0 // a switch probes the others without waiting
		}
		lease, err := s.pool.Acquire(r.Context(), cfg, pool.Request{
			Group: routeGroup, Model: rec.Model, RequestID: rid,
			Affinity: ident.Fingerprint, CacheAffinity: cacheAffinity, Explicit: ident.Confidence == "explicit",
			Excluded: excluded, Wait: wait,
			RequireBinding: stateful, PreserveBinding: config.StrictResponses(rec.Provider),
		})
		if err != nil {
			if r.Context().Err() != nil {
				finish(499, "client_cancelled")
				return
			}
			var unavailable *pool.UnavailableError
			if !stateful && !usedFallback && fallbackGroup != "" && errors.As(err, &unavailable) {
				routeGroup = fallbackGroup
				usedFallback = true
				cfg = rootCfg.ForProvider(rootCfg.GroupPool(routeGroup))
				i-- // no account or RPM permit was acquired; still bounded by one fallback group
				continue
			}
			if last != nil {
				break // nobody else to switch to: answer with the last failure
			}
			var ce *pool.CapacityError
			var ue *pool.UnavailableError
			var be *pool.BindingError
			switch {
			case errors.As(err, &be):
				rec.ErrorCategory = "stateful_binding"
				writeLocalError(w, proto, http.StatusConflict, "invalid_request_error", "stateful_binding_unavailable", be.Error(), 0)
				finish(http.StatusConflict, "failed")
			case errors.As(err, &ce):
				ra := ceilSeconds(ce.RetryAfter)
				rec.ErrorCategory, rec.BlockedBy, rec.RetryAfter = "capacity", ce.BlockedBy, &ra
				writeLocalError(w, proto, http.StatusTooManyRequests, "rate_limit_error", "pool_capacity", "all upstream accounts are busy, retry later", ra)
				finish(http.StatusTooManyRequests, "failed")
			case errors.As(err, &ue) && ue.RetryAfter > 0:
				// every account is only cooling down: a retryable 429, not a 503
				ra := ceilSeconds(ue.RetryAfter)
				rec.ErrorCategory, rec.BlockedBy, rec.RetryAfter = "unavailable", "cooldown", &ra
				if rec.Provider == config.ProviderOpenCodeGo && (ue.Reason == "policy-paused" || ue.Details["policy-paused"] == sumCounts(ue.Details)) {
					rec.ErrorCategory, rec.BlockedBy = "policy", "policy"
				}
				writeLocalError(w, proto, http.StatusTooManyRequests, "rate_limit_error", "pool_cooling_down", "all upstream accounts are cooling down for this model, retry later", ra)
				finish(http.StatusTooManyRequests, "failed")
			case errors.As(err, &ue) && ue.Details["not-entitled"] > 0 && ue.Details["not-entitled"] == sumCounts(ue.Details):
				rec.ErrorCategory, rec.BlockedBy = "unavailable", "not-entitled"
				writeLocalError(w, proto, http.StatusServiceUnavailable, "api_error", "model_not_entitled", "model "+rec.Model+" is not included in any upstream account's plan", 0)
				finish(http.StatusServiceUnavailable, "failed")
			case errors.As(err, &ue):
				rec.ErrorCategory, rec.BlockedBy = "unavailable", "unavailable"
				writeLocalError(w, proto, http.StatusServiceUnavailable, "api_error", "pool_unavailable", "no upstream account is available ("+ue.Reason+")", 0)
				finish(http.StatusServiceUnavailable, "failed")
			default:
				writeLocalError(w, proto, http.StatusServiceUnavailable, "api_error", "pool_unavailable", "no upstream account is available", 0)
				finish(http.StatusServiceUnavailable, "failed")
			}
			return
		}
		d := lease.Decision
		acct := lease.Account
		if config.IsOpenCode(acct.Provider) {
			rec.Provider = acct.Provider
			rec.Selector = cfg.Routing.Strategy
		}
		if i == 0 {
			rec.BindingResult = d.BindingResult
			rec.CacheAffinityResult = d.CacheResult
		}
		// Conversation continuity: a request that names a conversation and then
		// switches accounts splits that one conversation across two Perch
		// identities. Track it per request so the log shows a pool that handed
		// a chat to a second account instead of only showing a switch count.
		if ident.Fingerprint != "" {
			if _, seenAccount := triedAccounts[acct.ID]; !seenAccount {
				triedAccounts[acct.ID] = true
				rec.Conversations = len(triedAccounts)
				if firstAccount == "" {
					firstAccount = acct.ID
				} else {
					rec.CrossAccount = true
					rec.CrossAccountFrom = firstAccount
				}
			}
		}
		if len(rec.Workflow) < 4 {
			order := d.Order
			if len(order) > 8 {
				order = order[:8]
			}
			rec.Workflow = append(rec.Workflow, workflowDecision{Account: acct.ID, Reason: d.Reason, Binding: d.BindingResult, Overflow: d.Overflow, Order: order})
		}
		rec.AccountID, rec.AccountName, rec.SelectionReason, rec.Overflow = acct.ID, acct.Name, d.Reason, d.Overflow
		rec.Proxy = egressLabel(acct.ProxyURL)

		res = s.attempt(w, r, cfg, lease, body, proto, rec, i+1, start, identityScope, ident, detailIdentities)
		res.privateIdentities = detailIdentities
		if rec.Provider == config.ProviderMistral && len(rec.upstreamIdentities) > 0 {
			res.privateIdentities = rec.upstreamIdentities
		}
		if rec.Provider == config.ProviderTokenHarbor && (res.kind == outFailed || res.result == "failed") {
			s.thFailureDetail(cfg, rec, r, body, res, i)
		}
		lease.Release()
		att := attemptLog{Account: acct.ID, AccountName: acct.Name, Status: res.status, Ms: res.ms, Egress: "direct", EgressIP: res.egressIP, Terminal: res.terminal}
		if acct.ProxyURL != "" {
			att.Egress = "proxy"
		}
		if res.upstreamStatus != 0 {
			us := res.upstreamStatus
			att.UpstreamStatus = &us
		}
		switch res.kind {
		case outDelivered:
			rec.UpstreamRequestID = res.header.Get("X-Request-Id")
			rec.Attempts = append(rec.Attempts, att)
			if res.result == "success" {
				if acct.Provider == config.ProviderOpenCodeGo {
					s.pool.RememberCache(cfg, cacheAffinity, acct.ID, rec.Model)
				}
				s.pool.Sample(acct.ID, true)
				s.pool.Succeeded(cfg, acct.ID, rec.Model)
			} else {
				if !res.evaluated {
					s.pool.Sample(acct.ID, false)
				}
				rec.ErrorCategory = res.category
			}
			var tok int64
			for _, p := range []*int64{res.usage.in, res.usage.out} {
				if p != nil {
					tok += *p
				}
			}
			s.pool.AddTokens(acct.ID, tok)
			finish(res.status, res.result)
			return
		case outCancelled:
			rec.Attempts = append(rec.Attempts, att)
			res.status = 499
			finish(499, "client_cancelled")
			return
		}
		ev := s.evaluate(cfg, acct, rec.Model, res, rid, i, rec.ClientKeyID, rec.ClientKeyName)
		att.Action, att.RuleID = ev.action, ev.ruleID
		rec.Attempts = append(rec.Attempts, att)
		last = res
		if !config.IsOpenCode(rec.Provider) && rec.Provider != config.ProviderTokenHarbor && rec.Provider != config.ProviderMistral && res.status == http.StatusBadRequest && strippedTools < 3 {
			// the upstream refuses a hosted tool (e.g. web_search) on this route:
			// drop that tool type and ask the same account again
			if nb, ok := stripUnsupportedTool(body, res.body); ok {
				body = nb
				strippedTools++
				i--
				continue
			}
		}
		if stateful || !ev.switchAccount || i+1 >= maxAttempts {
			break
		}
		excluded[acct.ID] = true
		rec.Switched = true
	}
	res = last
	status := s.writeFailure(w, cfg, proto, last, len(rec.Attempts))
	rec.ErrorCategory = last.category
	if status == http.StatusTooManyRequests {
		ra := ceilSeconds(last.retryAfter)
		rec.RetryAfter = &ra
	}
	finish(status, "failed")
}

var unsupportedToolRe = regexp.MustCompile(`unsupported_tool\][^"]*?unsupported \w+ tool type \\?"([A-Za-z0-9_.-]+)\\?"`)

// stripUnsupportedTool removes every tool of the type an upstream
// unsupported_tool 400 names from the request body. Clients such as Codex
// add hosted tools (web_search) that the chat route of non-OpenAI models
// rejects, while the rest of the request is fine.
func stripUnsupportedTool(body, errBody []byte) ([]byte, bool) {
	m := unsupportedToolRe.FindSubmatch(errBody)
	if m == nil {
		return nil, false
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(body, &obj) != nil {
		return nil, false
	}
	var tools []json.RawMessage
	if json.Unmarshal(obj["tools"], &tools) != nil {
		return nil, false
	}
	kept := make([]json.RawMessage, 0, len(tools))
	for _, t := range tools {
		var ty struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(t, &ty) == nil && ty.Type == string(m[1]) {
			continue
		}
		kept = append(kept, t)
	}
	if len(kept) == len(tools) {
		return nil, false
	}
	if len(kept) == 0 {
		delete(obj, "tools")
		delete(obj, "tool_choice")
		delete(obj, "parallel_tool_calls")
	} else {
		obj["tools"], _ = json.Marshal(kept)
	}
	nb, err := json.Marshal(obj)
	return nb, err == nil
}

// wrappedStatus finds a deterministic request error an upstream wrapped in
// a 5xx, so it is answered as the 400/422 it is and cools nothing down.
func wrappedStatus(lowerBody string) int {
	for _, m := range []string{"status code 422", `"status":422`, `"status": 422`, "unprocessable entity"} {
		if strings.Contains(lowerBody, m) {
			return 422
		}
	}
	for _, m := range []string{"invalid_request_error", "status code 400", `"status":400`, `"status": 400`, `"code":400`, "bad request", "invalid_argument"} {
		if strings.Contains(lowerBody, m) {
			return 400
		}
	}
	return 0
}

type evaluation struct {
	switchAccount bool
	action        string
	ruleID        string
}

// evaluate applies the error rules to a failed attempt that wrote nothing
// to the client, logs it, and says whether to switch accounts.
func (s *Server) evaluate(cfg *config.Config, acct config.Account, model string, res *attemptResult, rid string, idx int, keyID, keyName string) evaluation {
	now := time.Now()
	reasonBody := res.body
	if acct.Provider == config.ProviderTokenHarbor || acct.Provider == config.ProviderMistral {
		reasonBody, _ = thBodyPreview(reasonBody, res.privateIdentities, 16<<10)
	}
	reason, truncated := errorReason(cfg, reasonBody, res.contentType)
	redacted := redactSecrets(cfg, clip(string(res.body), 16<<10))
	matchText := strings.ToLower(redacted)
	el := errorLog{TS: now.UTC(), RequestID: rid, AttemptIndex: idx, ClientKeyID: keyID, ClientKeyName: keyName, Provider: acct.Provider, AccountID: acct.ID, AccountName: acct.Name, Model: model,
		Status: res.status, Category: res.category, Terminal: res.terminal, Reason: reason, ReasonTruncated: truncated,
		MatchedBy: "default", HealthAction: "degrade", ResponseContentType: strings.SplitN(res.contentType, ";", 2)[0], ResponseBytes: len(res.body)}
	if acct.Provider == config.ProviderMistral {
		el.ResponseBodyTruncated = res.bodyTruncated
		el.MistralValidation = mistralValidationDetails(res.body)
	}
	if res.upstreamStatus != 0 {
		us := res.upstreamStatus
		el.UpstreamStatus = &us
	}
	if res.terminal != "upstream_http" && res.terminal != "upstream_envelope" {
		el.Reason = res.terminal + " error"
		if res.category == "proxy" {
			el.Reason = "proxy connection failed"
		}
	}
	if acct.Provider == config.ProviderOpenCodeGo {
		el.Egress = egressLabel(acct.ProxyURL)
		el.EgressIP = res.egressIP
		el.Origin = "transport"
		if res.upstreamStatus != 0 {
			el.Origin = "upstream"
		}
		if res.terminal == "egress" || res.terminal == "policy_pause" || res.terminal == "compatibility" {
			el.Origin = "local"
		}
	}
	ev := evaluation{}
	sample := true
	switch res.terminal {
	case "upstream_http", "upstream_envelope":
		st := res.status
		if acct.Provider == config.ProviderTokenHarbor && rules.IsTokenHarborSpendConfirmation(st, redacted) {
			res.ruleID, res.category = "tokenharbor-spend-confirmation", "spend-confirmation"
			el.RuleID, el.RuleAction, el.HealthAction, el.MatchedBy, el.RetryDecision, el.Category = res.ruleID, rules.ActionIgnore, "none", "provider-policy", "stop", res.category
			s.logs.Append("errors", el.TS, el)
			return ev
		}
		if config.IsOpenCode(acct.Provider) && (rules.IsOpenCodePolicyRefusal(st, redacted) || acct.Provider == config.ProviderOpenCodeGo && goPolicyRefusal(st, redacted)) {
			res.downstreamStatus, res.ruleID, res.category = 403, acct.Provider+"-policy-refusal", "policy"
			el.RuleID, el.RuleAction, el.HealthAction, el.MatchedBy, el.RetryDecision, el.Category = res.ruleID, rules.ActionIgnore, "none", "provider-policy", "stop", "policy"
			if acct.Provider == config.ProviderOpenCodeGo && cfg.OpenCode.GoPolicyCooldownSec > 0 {
				d := s.pauseGoPolicy(cfg, acct, redacted, res.ruleID)
				el.RuleAction = rules.ActionCooldown
				ms := d.Milliseconds()
				el.RetryAfterMs = &ms
			}
			s.logs.Append("errors", el.TS, el)
			return ev
		}
		if st >= 500 {
			if ws := wrappedStatus(matchText); ws != 0 {
				res.status, res.wrapped, res.category = ws, true, "request"
				el.Status, el.MatchedBy, el.HealthAction, el.Category = ws, "wrapped-request-error", "none", "request"
				s.logs.Append("errors", el.TS, el)
				return ev
			}
		}
		errRules, retRules := cfg.RulesFor(acct.Provider)
		if acct.Provider == config.ProviderPerch {
			errRules = rules.PerchErrorRules(errRules)
		}
		retry := rules.MatchRetry(retRules, st, matchText, res.header)
		if retry != nil {
			el.RetryRuleID, el.RetryDecision = retry.ID, retry.Decision
		}
		rule := rules.MatchError(errRules, st, redacted, res.header)
		if c, r := membershipLimits(matchText); c > 0 || r > 0 {
			s.pool.Learn(acct.ID, c, r)
		}
		if rule == nil {
			if st >= 400 && st < 500 && st != 401 && st != 402 && st != 403 && st != 429 {
				sample, el.HealthAction = false, "none" // the request's fault, not the account's
				res.category = "request"
				el.Category = "request"
			}
		} else {
			el.MatchedBy, el.RuleID, el.RuleScope, el.RuleAction = "rule", rule.ID, rule.Scope, rule.Action
			ev.action, ev.ruleID = rule.Action, rule.ID
			res.downstreamStatus, res.ruleID = rule.DownstreamStatus, rule.ID
			switch rule.Action {
			case rules.ActionIgnore:
				sample, el.HealthAction = false, "none"
			case rules.ActionCooldown:
				// what the cooldown covers: the account, one model, or a funding class
				target, class := "account", ""
				switch {
				case rule.Scope == rules.ScopeFunding && model != "":
					class = s.pool.Class(cfg, model)
					switch class {
					case "plan":
						target = "account" // windows, 套餐模型额度 and wallet all empty
					case "package":
						target = "class"
					default:
						target = "model"
					}
				case rule.Scope == rules.ScopeModel && model != "":
					target = "model"
				}
				strikes := 1
				if rule.Reset != nil && rule.Reset.Escalate {
					key := "account"
					switch target {
					case "class":
						key = class
					case "model":
						key = "model:" + model
					default:
						if class == "plan" {
							key = "plan"
						}
					}
					strikes = s.pool.Strike(acct.ID, key)
				}
				d := rules.CooldownAfter(rule, res.header, redacted, now, strikes)
				ms := d.Milliseconds()
				el.RetryAfterMs = &ms
				el.RuleScope = rule.Scope + ":" + target
				res.retryAfter = d
				switch target {
				case "model":
					s.pool.Cooldown(acct.ID, model, now.Add(d), st, rule.ID, rule.ID)
				case "class":
					s.pool.ClassCooldown(acct.ID, class, now.Add(d), st, rule.ID, rule.ID)
				default:
					s.pool.Cooldown(acct.ID, "", now.Add(d), st, rule.ID, rule.ID)
					res.accountScoped = true
				}
				ev.switchAccount = true
			case rules.ActionHardQuarantine:
				s.pool.Quarantine(acct.ID, st, rule.ID, rule.ID)
				if acct.Provider == config.ProviderPerch && rule.ID == "perch-banned" {
					if err := s.saveMetadata(true); err != nil {
						s.logf("failed to persist banned quarantine for %s: %v", acct.ID, err)
					}
				}
				res.accountScoped = true
				ev.switchAccount = true
			}
			if rule.RefreshQuota {
				s.quota.Refresh(acct.ID)
			}
		}
		if st == 401 {
			ev.switchAccount = true
			res.accountScoped = true
		}
		// an error a rule pins on the account (cooldown, quarantine) is not the
		// request's fault, so a retry rule for its status doesn't stop the switch
		if retry != nil && !(rule != nil && (rule.Action == rules.ActionCooldown || rule.Action == rules.ActionHardQuarantine)) {
			ev.switchAccount = false
		} else if retry != nil {
			el.RetryDecision = "overridden-by-error-rule"
		}
	case "compatibility", "egress", "policy_pause", "incomplete_stream":
		sample, el.HealthAction = false, "none"
	case "timeout":
		if config.StrictResponses(acct.Provider) || acct.Provider == config.ProviderMistral {
			ev.switchAccount = !res.sent.Load()
			sample, el.HealthAction = false, "none"
		}
	case "proxy", "network":
		// never sent: another account can take it without double billing
		ev.switchAccount = !res.sent.Load()
		if (config.StrictResponses(acct.Provider) || acct.Provider == config.ProviderMistral) && res.category == "proxy" {
			sample, el.HealthAction = false, "none"
		}
	}
	if sample {
		s.pool.Sample(acct.ID, false)
	}
	if res.retryAfter == 0 && res.header != nil {
		if v := res.header.Get("Retry-After"); v != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				res.retryAfter = time.Duration(n) * time.Second
			}
		}
	}
	el.Switched = ev.switchAccount
	s.logs.Append("errors", el.TS, el)
	return ev
}

// writeFailure answers the client for a request whose last attempt failed
// before writing anything. Account problems never reach the client as
// 401/402/403: a relay such as new-api would disable the whole channel.
func (s *Server) writeFailure(w http.ResponseWriter, cfg *config.Config, proto string, res *attemptResult, attempts int) int {
	w.Header().Set("X-Attempts", strconv.Itoa(attempts))
	if res.ruleID == "mistral-service-unavailable" && res.header != nil {
		if hint := res.header.Get("Retry-After"); hint != "" {
			w.Header().Set("Retry-After", hint)
		}
	}
	st := res.status
	switch {
	case res.ruleID == "tokenharbor-spend-confirmation":
		// The caller must see the actual approval prompt. Do not hide it behind
		// a generic account-unavailable error or rotate keys to avoid consent.
	case res.terminal == "timeout":
		writeLocalError(w, proto, http.StatusGatewayTimeout, "api_error", "upstream_timeout", "upstream timed out", 0)
		return http.StatusGatewayTimeout
	case res.terminal == "network" || res.terminal == "proxy":
		writeLocalError(w, proto, http.StatusBadGateway, "api_error", "upstream_connection_failed", "upstream connection failed", 0)
		return http.StatusBadGateway
	case res.wrapped:
		// pass the deterministic request error through
	case res.downstreamStatus != 0:
		ds := res.downstreamStatus
		ra, typ, msg := 0, "api_error", "upstream account unavailable for this request"
		switch {
		case ds == http.StatusTooManyRequests:
			ra, typ, msg = ceilSeconds(res.retryAfter), "rate_limit_error", "upstream account limit reached, retry later"
		case ds == http.StatusBadGateway:
			msg = "upstream service unavailable"
		case ds == http.StatusForbidden && strings.Contains(res.ruleID, "policy-refusal"):
			typ, msg = "permission_error", "upstream policy refused this request; no account or egress replay was attempted"
		}
		writeLocalError(w, proto, ds, typ, strings.ReplaceAll(res.ruleID, "-", "_"), msg, ra)
		return ds
	case st == 401 || st == 403:
		writeLocalError(w, proto, http.StatusBadGateway, "api_error", "upstream_account_auth_failed", "upstream account authentication failed", 0)
		return http.StatusBadGateway
	case st == 429:
		ra := ceilSeconds(res.retryAfter)
		writeLocalError(w, proto, http.StatusTooManyRequests, "rate_limit_error", "upstream_rate_limited", "upstream is rate limited, retry later", ra)
		return http.StatusTooManyRequests
	case st == 402 || (res.accountScoped && st >= 400 && st < 500):
		writeLocalError(w, proto, http.StatusServiceUnavailable, "api_error", "upstream_account_unavailable", "upstream account temporarily unavailable", 0)
		return http.StatusServiceUnavailable
	}
	ct := res.contentType
	if ct == "" || res.terminal == "upstream_envelope" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(st)
	_, _ = w.Write([]byte(redactSecrets(cfg, string(res.body))))
	return st
}

// ---- upstream attempt ----

var skipRequestHeaders = map[string]bool{
	"connection": true, "keep-alive": true, "proxy-authenticate": true, "proxy-authorization": true, "proxy-connection": true,
	"te": true, "trailer": true, "transfer-encoding": true, "upgrade": true, "host": true, "content-length": true,
	"authorization": true, "x-api-key": true, "api-key": true, "x-goog-api-key": true, "cookie": true,
	"accept-encoding": true, "forwarded": true, "x-real-ip": true, "true-client-ip": true, "x-request-id": true,
	// Every other spelling a reverse proxy or CDN uses to describe the
	// downstream peer. The upstream must see our egress, never the client's
	// address, so these are dropped by name rather than by prefix.
	"x-client-ip": true, "client-ip": true, "x-originating-ip": true, "x-cluster-client-ip": true,
	"x-remote-ip": true, "x-remote-addr": true, "x-host": true, "via": true, "x-proxyuser-ip": true,
	"fastly-client-ip": true, "x-akamai-client-ip": true, "x-bluecoat-via": true, "x-hermes-client-ip": true,
}

func copyRequestHeaders(dst, src http.Header) {
	for k, vs := range src {
		lk := strings.ToLower(k)
		if skipRequestHeaders[lk] || strings.HasPrefix(lk, "x-forwarded-") || strings.HasPrefix(lk, "cf-") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// completeBrowserHints fills in Sec-CH-UA, Accept-Language and the Sec-Fetch
// set when, and only when, the request already carries a browser User-Agent.
//
// Such a client contradicts itself otherwise: a Chrome User-Agent with no
// brand list, no language and no fetch metadata is not a browser sending an
// unusual header set, it is a program wearing a browser's name. Clients that
// identify themselves (Codex, Claude Code, OpenCode, the OpenAI and Anthropic
// SDKs) are left completely alone.
func completeBrowserHints(h http.Header) {
	ua := h.Get("User-Agent")
	if ua == "" || !strings.HasPrefix(ua, "Mozilla/") {
		return
	}
	if h.Get("Sec-Ch-Ua") != "" {
		// The caller already sent a coherent hint set; do not second-guess it.
		return
	}
	if egressid.ChromeMajor(ua) == "" {
		// A non-Chromium browser (Firefox, Safari) sends no client hints at
		// all, so adding them would be the contradiction. Only the fetch
		// metadata below applies to every browser.
		if h.Get("Sec-Fetch-Mode") == "" {
			h.Set("Sec-Fetch-Mode", "cors")
			h.Set("Sec-Fetch-Dest", "empty")
			h.Set("Sec-Fetch-Site", "cross-site")
		}
		return
	}
	if h.Get("Accept-Language") == "" {
		h.Set("Accept-Language", "en-US,en;q=0.9")
	}
	// An inference call is a fetch() from an application, never a navigation.
	if h.Get("Sec-Fetch-Mode") == "" {
		h.Set("Sec-Fetch-Mode", "cors")
		h.Set("Sec-Fetch-Dest", "empty")
		h.Set("Sec-Fetch-Site", "cross-site")
	}
	major := egressid.ChromeMajor(ua)
	brand := "Google Chrome"
	if strings.Contains(ua, "Edg/") {
		brand = "Microsoft Edge"
	}
	h.Set("Sec-Ch-Ua", egressid.BuildBrandList(ua, brand, major))
	if h.Get("Sec-Ch-Ua-Mobile") == "" {
		h.Set("Sec-Ch-Ua-Mobile", "?0")
	}
	if h.Get("Sec-Ch-Ua-Platform") == "" {
		switch {
		case strings.Contains(ua, "Windows NT"):
			h.Set("Sec-Ch-Ua-Platform", `"Windows"`)
		case strings.Contains(ua, "Macintosh"):
			h.Set("Sec-Ch-Ua-Platform", `"macOS"`)
		case strings.Contains(ua, "Linux"):
			h.Set("Sec-Ch-Ua-Platform", `"Linux"`)
		}
	}
}

func isProxyError(err error, proxyURL string) bool {
	if proxyURL == "" {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "proxyconnect" {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "proxyconnect") || strings.Contains(msg, "socks")
}

func (s *Server) attempt(w http.ResponseWriter, r *http.Request, cfg *config.Config, lease *pool.Lease, body []byte, proto string, rec *requestLog, attemptNo int, start time.Time, group string, ident Identity, privateIdentities []string) *attemptResult {
	acct := lease.Account
	res := &attemptResult{goRecord: rec}
	t0 := time.Now()
	defer func() { res.ms = time.Since(t0).Milliseconds() }()

	actx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var timedOut atomic.Bool
	limit := time.Duration(cfg.Routing.RequestSec) * time.Second
	if rec.Stream {
		limit = time.Duration(cfg.Routing.FirstEventSec) * time.Second
	}
	timer := time.AfterFunc(limit, func() { timedOut.Store(true); cancel() })
	defer timer.Stop()

	// Perch has no OpenAI-compatible endpoint: its one model-call proxy takes
	// Perch's own envelope and answers with Perch's own event stream. It is
	// served here, inside the same lease/retry/logging path every other pool
	// uses, so selection, cooldowns and the error rules all apply unchanged.
	if acct.Provider == config.ProviderPerch {
		return s.attemptPerch(w, r, cfg, lease, body, proto, rec, actx, res, &timedOut)
	}

	var ocFrom, ocTo opencode.Protocol
	if acct.Provider == config.ProviderOpenCodeGo {
		res.upstreamSession = ident.UpstreamSession
		err := s.ensureGoEgress(actx, cfg, acct, false)
		eg := s.goEgressView(cfg, acct)
		rec.EgressStatus, rec.EgressIP, res.egressIP = eg.Status, eg.ObservedIP, eg.ObservedIP
		if err != nil {
			res.kind, res.status, res.terminal, res.category = outFailed, 503, "egress", "egress"
			if errors.Is(err, errGoPolicyPause) {
				res.status, res.terminal, res.category = 429, "policy_pause", "policy"
				res.retryAfter = time.Until(s.pool.PauseUntil(acct.ID, "opencode-go-policy-refusal"))
			}
			res.contentType = "application/json"
			res.body = []byte(`{"error":{"message":"fixed egress could not be verified; no direct fallback"}}`)
			if res.category == "policy" {
				res.body = []byte(`{"error":{"message":"Go policy pause active; no upstream was contacted"}}`)
			}
			return res
		}
	}
	if config.IsOpenCode(acct.Provider) {
		var err error
		ocFrom, ocTo, body, err = s.prepareOpenCode(cfg, acct, r.URL.Path, rec.Model, body)
		if err != nil {
			res.kind, res.status, res.terminal, res.category = outFailed, http.StatusBadRequest, "compatibility", "request"
			res.contentType = "application/json"
			res.body, _ = json.Marshal(map[string]any{"error": map[string]string{"type": "invalid_request_error", "code": "unsupported_compatibility", "message": err.Error()}})
			return res
		}
	}
	var sentOnce sync.Once
	markSent := func() { sentOnce.Do(func() { res.sent.Store(true); lease.MarkSent() }) }
	trace := &httptrace.ClientTrace{WroteHeaders: func() {
		// Partial body writes are uncertain too: once headers left the socket,
		// OpenCode must not replay a network failure on another account.
		if config.StrictResponses(acct.Provider) || acct.Provider == config.ProviderMistral {
			markSent()
		}
	}, WroteRequest: func(i httptrace.WroteRequestInfo) {
		if i.Err == nil {
			markSent()
		}
	}}
	targetPath := strings.TrimPrefix(r.URL.Path, "/v1")
	if ocTo != "" {
		targetPath = opencode.Path(ocTo)
	}
	target := cfg.InferenceBase(acct, string(ocTo)) + targetPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	if acct.Provider == config.ProviderOpenCodeGo && ocTo == opencode.Messages {
		u, _ := url.Parse(target)
		q := u.Query()
		q.Set("beta", "true")
		u.RawQuery = q.Encode()
		target = u.String()
	}
	// the caller names a model by the pool's public name (a base model when
	// several accounts serve it under their own ids); this account may only
	// know it by one of its own, so translate before sending
	if acct.Provider != config.ProviderTokenHarbor && acct.Provider != config.ProviderMistral {
		body = s.upstreamBody(cfg, acct.ID, rec.Model, body)
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(actx, trace), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusBadGateway, "network", "network"
		return res
	}
	copyRequestHeaders(req.Header, r.Header)
	// A downstream client that sends a browser User-Agent but no Sec-CH-UA
	// hints describes a browser to one header reader and a script to the next,
	// which is a stronger signal than an honest library name. Only a browser
	// User-Agent is completed this way: Codex, Claude Code and OpenCode send
	// their own product strings and must keep them untouched.
	completeBrowserHints(req.Header)
	if acct.Provider == config.ProviderTokenHarbor {
		req.Header.Set("User-Agent", gatewayUserAgent(req.Header.Get("User-Agent"), "account-pool-balancer/tokenharbor-gateway"))
	}
	if acct.Provider == config.ProviderOpenCodeGo {
		req.Header.Set("User-Agent", goUserAgent(req.Header.Get("User-Agent")))
		if ident.UpstreamSession != "" {
			req.Header.Set("X-Opencode-Session", ident.UpstreamSession)
		}
	}
	req.Header.Set("Authorization", "Bearer "+acct.RuntimeKey)
	switch acct.Provider {
	case config.ProviderOpenCodeZen, config.ProviderOpenCodeGo:
		if ocTo == opencode.Messages {
			req.Header.Del("Authorization")
			req.Header.Set("X-Api-Key", acct.RuntimeKey)
			if req.Header.Get("Anthropic-Version") == "" {
				req.Header.Set("Anthropic-Version", "2023-06-01")
			}
		}
	case config.ProviderOllama, config.ProviderModal, config.ProviderTokenHarbor, config.ProviderMistral:
		// Token Harbor accepts Bearer on all native protocols.
		// Ollama's /v1/messages explicitly refuses x-api-key on its own, and
		// Modal authenticates a proxy token in Authorization only: neither
		// wants the Anthropic header, so never send it
	default:
		if proto == "anthropic" {
			req.Header.Set("X-Api-Key", acct.RuntimeKey)
		}
	}
	if (acct.Provider == config.ProviderOllama || acct.Provider == config.ProviderModal || acct.Provider == config.ProviderTokenHarbor) && proto == "anthropic" {
		// both Messages endpoints require the version header; clients behind
		// a relay sometimes drop it
		if req.Header.Get("Anthropic-Version") == "" {
			req.Header.Set("Anthropic-Version", "2023-06-01")
		}
	}
	if acct.Provider == config.ProviderModal && ident.Fingerprint != "" {
		// Modal routes a Shared Endpoint's request to the container most
		// likely to hold its prompt prefix in cache: the routing key is what
		// keeps one conversation's requests together. Our own HMAC of the
		// session is exactly that key, and it never leaves as the raw value.
		req.Header.Set("Modal-Routing-Affinity-Key", ident.Fingerprint)
	}
	if acct.Provider == config.ProviderMistral {
		ensureMistralXAffinity(req.Header, body)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.ContentLength = int64(len(body))
	wireCapture := newMistralUpstreamDetail(cfg, req, rec, body, attemptNo, privateIdentities)
	if wireCapture != nil {
		trace.WroteHeaderField = wireCapture.recordHeader
		defer func() {
			wire, secrets := wireCapture.finish(res)
			rec.upstreamDetails = append(rec.upstreamDetails, wire)
			rec.upstreamIdentities = append(rec.upstreamIdentities, secrets...)
		}()
	}

	client := s.httpClient(acct.ProxyURL)
	if config.StrictResponses(acct.Provider) || acct.Provider == config.ProviderMistral {
		client = opencode.NoRedirect(client)
	}
	res.upstreamStarted = time.Now()
	resp, err := client.Do(req)
	if config.IsOpenCode(acct.Provider) && r.Context().Err() == nil {
		if err == nil {
			s.observeOpenCodeProxy(acct.ProxyURL, resp.StatusCode == http.StatusProxyAuthRequired)
		} else if isProxyError(err, acct.ProxyURL) {
			s.observeOpenCodeProxy(acct.ProxyURL, true)
		}
	}
	if err != nil {
		res.kind = outFailed
		switch {
		case r.Context().Err() != nil:
			res.kind = outCancelled
		case timedOut.Load():
			res.status, res.terminal, res.category = http.StatusGatewayTimeout, "timeout", "timeout"
		case isProxyError(err, acct.ProxyURL):
			res.status, res.terminal, res.category = http.StatusBadGateway, "proxy", "proxy"
		default:
			res.status, res.terminal, res.category = http.StatusBadGateway, "network", "network"
		}
		return res
	}
	defer resp.Body.Close()
	markSent()
	res.upstreamStatus = resp.StatusCode
	res.header = resp.Header
	if wireCapture != nil {
		wireCapture.attach(resp)
	}
	ct := resp.Header.Get("Content-Type")
	res.contentType = ct
	if config.IsOpenCode(acct.Provider) && acct.ProxyURL != "" && resp.StatusCode == http.StatusProxyAuthRequired {
		res.kind, res.status, res.terminal, res.category = outFailed, 502, "proxy", "proxy"
		return res
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		limit := int64(64 << 10)
		if acct.Provider == config.ProviderMistral {
			limit++ // distinguish a complete64KiB error from a truncated prefix
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
		if acct.Provider == config.ProviderMistral && len(b) > 64<<10 {
			res.bodyTruncated = true
			b = b[:64<<10]
		}
		if r.Context().Err() != nil {
			res.kind = outCancelled
			return res
		}
		res.kind, res.status, res.body, res.terminal, res.category = outFailed, resp.StatusCode, b, "upstream_http", "upstream"
		return res
	}
	if ocTo != "" && ocFrom != ocTo {
		timer.Reset(time.Duration(cfg.Routing.RequestSec) * time.Second)
		return s.openCodeCompatibility(w, r, cfg, resp, res, ocTo, ocFrom, rec.Stream, attemptNo, start)
	}
	isResponses := strings.HasPrefix(r.URL.Path, "/v1/responses") && acct.Provider != config.ProviderMistral
	if strings.HasPrefix(strings.ToLower(ct), "text/event-stream") {
		return s.relayStream(w, r, cfg, lease, resp, res, cancel, timer, &timedOut, attemptNo, start, isResponses, group, body, privateIdentities)
	}

	b, err := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	res.upstreamLast = time.Now()
	if err != nil {
		res.kind = outFailed
		switch {
		case r.Context().Err() != nil:
			res.kind = outCancelled
		case timedOut.Load():
			res.status, res.terminal, res.category = http.StatusGatewayTimeout, "timeout", "timeout"
		default:
			res.status, res.terminal, res.category = http.StatusBadGateway, "network", "network"
		}
		return res
	}
	var m map[string]any
	if json.Unmarshal(b, &m) == nil && m != nil {
		if isEnvelopeError(m) {
			res.kind, res.status, res.body, res.terminal, res.category = outFailed, errorStatus(m, b), b, "upstream_envelope", "upstream"
			return res
		}
		scanUsage(m, &res.usage)
		if isResponses {
			if id, _ := m["id"].(string); id != "" {
				s.pool.BindWithSession(cfg, s.responseFingerprint(group, id), acct.ID, res.upstreamSession)
			}
		}
	}
	if acct.Provider == config.ProviderMistral && r.URL.Path == "/v1/chat/completions" {
		b, err = mistralChatResponse(b, false)
		if err != nil {
			return mistralResponseFailure(res)
		}
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Attempts", strconv.Itoa(attemptNo))
	setUpstreamRequestID(h, resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(b)
	res.kind, res.result, res.status, res.terminal = outDelivered, "success", resp.StatusCode, "success"
	return res
}

func isEnvelopeError(m map[string]any) bool {
	if m["error"] == nil {
		return m["type"] == "error"
	}
	return m["choices"] == nil && m["output"] == nil && m["content"] == nil
}

// errorStatus reads a status out of an error envelope or SSE error event.
func errorStatus(m map[string]any, raw []byte) int {
	num := func(v any) int {
		switch x := v.(type) {
		case float64:
			if x >= 400 && x <= 599 {
				return int(x)
			}
		case string:
			if n, err := strconv.Atoi(x); err == nil && n >= 400 && n <= 599 {
				return n
			}
		}
		return 0
	}
	if n := num(m["status"]); n != 0 {
		return n
	}
	if n := num(m["status_code"]); n != 0 {
		return n
	}
	for _, k := range []string{"error", "response"} {
		if e, ok := m[k].(map[string]any); ok {
			for _, f := range []string{"status", "status_code", "code"} {
				if n := num(e[f]); n != 0 {
					return n
				}
			}
			if inner, ok := e["error"].(map[string]any); ok {
				if n := num(inner["code"]); n != 0 {
					return n
				}
			}
		}
	}
	lower := strings.ToLower(string(raw))
	switch {
	case strings.Contains(lower, "insufficient_balance"):
		return 402
	case strings.Contains(lower, "invalid credentials"), strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "invalid proxy auth"):
		// Ollama answers {"error":"invalid credentials"}; Modal's proxy
		// answers {"error":"invalid proxy auth credentials"}
		return 401
	case strings.Contains(lower, "account suspended"), strings.Contains(lower, "suspended"):
		return 403
	case strings.Contains(lower, "model_limit_exceeded"), strings.Contains(lower, "rate_limit"), strings.Contains(lower, "too many requests"):
		return 429
	case strings.Contains(lower, "invalid_api_key"), strings.Contains(lower, "authentication_error"):
		return 401
	case strings.Contains(lower, "tier_model_not_entitled"), strings.Contains(lower, "permission"):
		return 403
	case strings.Contains(lower, "unknown inference model"):
		// Modal: the model field names no Endpoint this token can see
		return 404
	case strings.Contains(lower, "invalid_request"), strings.Contains(lower, "invalid_model"):
		return 400
	}
	return 502
}

func intField(m map[string]any, k string) *int64 {
	if f, ok := m[k].(float64); ok {
		v := int64(f)
		return &v
	}
	return nil
}

// scanUsage reads OpenAI Chat, Responses and Anthropic usage shapes; a
// later event overrides only the fields it carries.
func scanUsage(m map[string]any, u *usage) {
	var us map[string]any
	if x, ok := m["usage"].(map[string]any); ok {
		us = x
	} else if r, ok := m["response"].(map[string]any); ok {
		us, _ = r["usage"].(map[string]any)
	} else if msg, ok := m["message"].(map[string]any); ok {
		us, _ = msg["usage"].(map[string]any)
	}
	if us == nil {
		return
	}
	set := func(dst **int64, v *int64) {
		if v != nil {
			*dst = v
		}
	}
	set(&u.in, intField(us, "prompt_tokens"))
	set(&u.in, intField(us, "input_tokens"))
	set(&u.out, intField(us, "completion_tokens"))
	set(&u.out, intField(us, "output_tokens"))
	set(&u.cached, intField(us, "cache_read_input_tokens"))
	set(&u.cacheWrite, intField(us, "cache_creation_input_tokens"))
	for _, k := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if d, ok := us[k].(map[string]any); ok {
			set(&u.cached, intField(d, "cached_tokens"))
			set(&u.cacheWrite, intField(d, "cache_creation_tokens"))
		}
	}
	for _, k := range []string{"completion_tokens_details", "output_tokens_details"} {
		if d, ok := us[k].(map[string]any); ok {
			set(&u.reasoning, intField(d, "reasoning_tokens"))
		}
	}
}

// ---- SSE ----

const maxEventBytes = 32 << 20

var errEventTooLarge = errors.New("SSE event too large")

// readEvent reads one SSE event: lines up to a blank line, raw.
func readEvent(rd *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		line, err := rd.ReadSlice('\n')
		buf = append(buf, line...)
		if len(buf) > maxEventBytes {
			return nil, errEventTooLarge
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return buf, err
		}
		if len(line) == 1 || (len(line) == 2 && line[0] == '\r') {
			if len(buf) == len(line) {
				buf = buf[:0] // a stray blank line
				continue
			}
			return buf, nil
		}
	}
}

func sseFields(ev []byte) (event string, data []byte, real bool) {
	for _, line := range bytes.Split(ev, []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			event = strings.TrimSpace(string(line[6:]))
			real = true
		case bytes.HasPrefix(line, []byte("data:")):
			d := line[5:]
			if len(d) > 0 && d[0] == ' ' {
				d = d[1:]
			}
			if len(data) > 0 {
				data = append(data, '\n')
			}
			data = append(data, d...)
			real = true
		case len(line) > 0 && line[0] != ':':
			real = true
		}
	}
	return
}

// sseError says whether an event (its sseFields) is an error event, with
// its status and JSON payload. Only an event that names an error is decoded.
func sseError(event string, data []byte) (bool, int, []byte) {
	isErr := event == "error" || event == "response.failed"
	if !isErr && !bytes.Contains(data, []byte(`"error"`)) && !bytes.Contains(data, []byte("response.failed")) {
		return false, 0, nil
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	if m != nil {
		if m["error"] != nil && m["choices"] == nil {
			isErr = true
		}
		if t, _ := m["type"].(string); t == "error" || t == "response.failed" {
			isErr = true
		}
	}
	if !isErr {
		return false, 0, nil
	}
	return true, errorStatus(m, data), data
}

// hasUsage says whether data has a "usage" member that isn't null: OpenAI
// chunks streamed with include_usage carry "usage":null until the last one.
func hasUsage(data []byte) bool {
	key := []byte(`"usage"`)
	for rest := data; ; {
		i := bytes.Index(rest, key)
		if i < 0 {
			return false
		}
		rest = rest[i+len(key):]
		v := bytes.TrimLeft(rest, " \t\r\n")
		if len(v) == 0 || v[0] != ':' {
			continue
		}
		if !bytes.HasPrefix(bytes.TrimLeft(v[1:], " \t\r\n"), []byte("null")) {
			return true
		}
	}
}

// isTerminalEvent says whether an SSE event ends a stream: OpenAI's
// [DONE], Anthropic's message_stop, the Responses API's response.completed.
func isTerminalEvent(event string, data []byte) bool {
	if bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
		return true
	}
	return event == "message_stop" || event == "response.completed" || event == "response.incomplete"
}

type evMsg struct {
	ev  []byte
	err error
	at  time.Time // upstream read completion, before downstream parsing/flush
}

func (s *Server) relayStream(w http.ResponseWriter, r *http.Request, cfg *config.Config, lease *pool.Lease, resp *http.Response, res *attemptResult,
	cancel context.CancelFunc, timer *time.Timer, timedOut *atomic.Bool, attemptNo int, start time.Time, isResponses bool, group string, body []byte, privateIdentities []string) *attemptResult {
	mistralResponses := lease.Account.Provider == config.ProviderMistral && r.URL.Path == "/v1/responses"
	rd := bufio.NewReaderSize(resp.Body, 64<<10)
	events := make(chan evMsg, 16)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			ev, err := readEvent(rd)
			select {
			case events <- evMsg{ev: ev, err: err, at: time.Now()}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	readFailure := func(err error) *attemptResult {
		if mistralResponses && err == io.EOF && r.Context().Err() == nil && !timedOut.Load() {
			return mistralResponsesTruncated(res)
		}
		res.kind = outFailed
		switch {
		case r.Context().Err() != nil:
			res.kind = outCancelled
		case timedOut.Load():
			res.status, res.terminal, res.category = http.StatusGatewayTimeout, "timeout", "timeout"
		default:
			res.status, res.terminal, res.category = http.StatusBadGateway, "network", "network"
		}
		return res
	}

	// hold everything until the first complete event proves not to be an error
	var first, firstData []byte
	var firstEvent string
	var firstErr error
	for first == nil {
		m := <-events
		if event, data, real := sseFields(m.ev); real && (m.err == nil || m.err == io.EOF) {
			first, firstEvent, firstData, firstErr = m.ev, event, data, m.err
			res.upstreamStream, res.upstreamFirst, res.upstreamLast = true, m.at, m.at
			break
		}
		if m.err != nil {
			return readFailure(m.err)
		}
	}
	timer.Stop()
	if isErr, st, payload := sseError(firstEvent, firstData); isErr {
		res.kind, res.status, res.body, res.terminal, res.category = outFailed, st, payload, "upstream_envelope", "upstream"
		res.contentType = "application/json"
		return res
	}
	mistralChat := lease.Account.Provider == config.ProviderMistral && r.URL.Path == "/v1/chat/completions"
	if mistralChat {
		var err error
		first, err = mistralChatEvent(first)
		if err != nil {
			return mistralResponseFailure(res)
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	h.Set("X-Attempts", strconv.Itoa(attemptNo))
	setUpstreamRequestID(h, resp.Header)
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	ttft := time.Since(start).Milliseconds()
	res.ttft = &ttft
	res.kind, res.status, res.result, res.terminal = outDelivered, http.StatusOK, "success", "success"

	clientGone := func() *attemptResult {
		cancel()
		res.kind, res.status, res.result = outCancelled, 499, "client_cancelled"
		return res
	}
	scan := func(event string, data []byte) {
		if !hasUsage(data) && !(isResponses && event == "response.created") && !bytes.Contains(data, []byte(`"response.created"`)) {
			return
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			return
		}
		scanUsage(m, &res.usage)
		if isResponses {
			if t, _ := m["type"].(string); t == "response.created" || event == "response.created" {
				if rr, ok := m["response"].(map[string]any); ok {
					if id, _ := rr["id"].(string); id != "" {
						s.pool.BindWithSession(cfg, s.responseFingerprint(group, id), lease.Account.ID, res.upstreamSession)
					}
				}
			}
		}
	}
	write := func(b []byte) bool {
		if _, err := w.Write(b); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	// Keep HTTP200 after commitment, but never turn a missing Responses
	// terminal into success or synthesize completed/DONE. No account replay.
	truncatedResponses := func() *attemptResult {
		if r.Context().Err() != nil {
			return clientGone()
		}
		failure := mistralResponsesTruncated(&attemptResult{})
		if !write(append(append([]byte("\n\nevent: error\ndata: "), failure.body...), '\n', '\n')) {
			return clientGone()
		}
		res.result, res.terminal, res.category = "failed", failure.terminal, failure.category
		if timedOut.Load() {
			res.terminal, res.category = "timeout", "timeout"
		}
		res.evaluated = true // transport incompleteness is not a bad credential
		return res
	}
	firstDone := mistralResponses && mistralResponsesTerminal(firstEvent, firstData)
	if !write(first) {
		if firstDone {
			return res
		}
		return clientGone()
	}
	scan(firstEvent, firstData)
	if firstDone {
		return res
	}
	if firstErr == io.EOF {
		if mistralResponses {
			return truncatedResponses()
		}
		return res // legacy native streams may finish with one event
	}

	idleDur := time.Duration(cfg.Routing.IdleSec) * time.Second
	idle := time.NewTimer(idleDur)
	defer idle.Stop()
	hb := time.NewTicker(time.Duration(cfg.Routing.HeartbeatSec) * time.Second)
	defer hb.Stop()
	for {
		select {
		case m := <-events:
			if len(m.ev) > 0 {
				res.upstreamLast = m.at
				event, data, _ := sseFields(m.ev)
				done := isTerminalEvent(event, data)
				if mistralResponses {
					done = mistralResponsesTerminal(event, data)
				}
				outEvent := m.ev
				if mistralChat {
					var err error
					outEvent, err = mistralChatEvent(outEvent)
					if err != nil {
						failure := mistralResponseFailure(&attemptResult{})
						if !write(append(append([]byte("data: "), failure.body...), '\n', '\n')) {
							return clientGone()
						}
						res.result, res.terminal, res.category = "failed", "compatibility", "compatibility"
						res.evaluated = true // incompatible output is not a bad account
						return res           // already committed: never replay or fabricate [DONE]
					}
				}
				if isErr, _, _ := sseError(event, data); isErr {
					outEvent = []byte(redactSecrets(cfg, string(m.ev)))
				}
				if !write(outEvent) {
					if done {
						return res // the client had what it needs and hung up first
					}
					return clientGone()
				}
				scan(event, data)
				if isErr, st, payload := sseError(event, data); isErr {
					res.result, res.category = "failed", "upstream"
					if mistralResponses {
						res.terminal, res.evaluated = "upstream_envelope", true
						return res // a failed terminal must not wait for EOF either
					}
					if lease.Account.Provider == config.ProviderOpenCodeGo && !res.evaluated && res.goRecord != nil {
						failure := &attemptResult{privateIdentities: privateIdentities, status: st, upstreamStatus: resp.StatusCode, egressIP: res.egressIP, body: payload, contentType: "application/json", header: resp.Header, terminal: "upstream_envelope", category: "upstream"}
						rc := res.goRecord
						if lease.Account.Provider == config.ProviderTokenHarbor {
							s.thFailureDetail(cfg, rc, r, body, failure, attemptNo-1)
						}
						s.evaluate(cfg, lease.Account, rc.Model, failure, rc.RequestID, attemptNo-1, rc.ClientKeyID, rc.ClientKeyName)
						res.evaluated = true
						res.category = failure.category
					}
				}
				if done {
					// Clients close as soon as they see the last event; don't wait
					// for the upstream EOF, or that close reads as a cancellation.
					return res
				}
				idle.Reset(idleDur)
			}
			if m.err == io.EOF {
				if mistralResponses {
					return truncatedResponses()
				}
				return res
			}
			if m.err != nil {
				if r.Context().Err() != nil {
					return clientGone()
				}
				if mistralResponses {
					return truncatedResponses()
				}
				res.result, res.terminal = "failed", "network"
				res.category = "network"
				if timedOut.Load() {
					res.terminal, res.category = "timeout", "timeout"
				}
				return res
			}
		case <-hb.C:
			if !write([]byte(": PING\n\n")) {
				return clientGone()
			}
		case <-idle.C:
			timedOut.Store(true)
			cancel()
		case <-r.Context().Done():
			return clientGone()
		}
	}
}

// ---- /v1/models ----

func sumCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// handleModels lists the upstream catalogue, narrowed to the models some
// account of the caller's group may call (each key's own catalogue), so a
// relay such as new-api only offers what this pool can serve.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	ck, ok := s.authClient(cfg, r)
	if !ok {
		writeLocalError(w, "openai", http.StatusUnauthorized, "authentication_error", "invalid_api_key", "invalid API key", 0)
		return
	}
	body := s.upstreamModels(r.Context(), cfg, ck.Group)
	if body == nil {
		writeLocalError(w, "openai", http.StatusBadGateway, "api_error", "models_unavailable", "could not list upstream models", 0)
		return
	}
	// An Ollama key's /v1/models is the public cloud catalogue (it answers
	// 200 even for a refused key), so it can't narrow what this pool serves.
	allowed, known := map[string]bool{}, false
	if provider := keyPool(cfg, ck); !cfg.Routing.IgnoreCatalog && provider != config.ProviderOllama && !config.IsOpenCode(provider) {
		now := time.Now()
		for _, a := range cfg.Accounts {
			if a.Group != ck.Group || !a.Enabled {
				continue
			}
			// PublicModels, not Models: an id that is an alias of another
			// (a Modal Shared Endpoint hostname) is published under the base
			// model, so accounts running the same model share one name
			if ms := s.quota.View(a.ID).PublicModels(now); ms != nil {
				known = true
				for _, m := range ms {
					allowed[m] = true
				}
			}
		}
	}
	if known {
		var doc map[string]any
		if json.Unmarshal(body, &doc) == nil {
			if data, ok := doc["data"].([]any); ok {
				kept := make([]any, 0, len(data))
				for _, it := range data {
					if m, ok := it.(map[string]any); ok {
						if id, _ := m["id"].(string); allowed[id] {
							kept = append(kept, it)
						}
					}
				}
				doc["data"] = kept
				if b, err := json.Marshal(doc); err == nil {
					body = b
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

type modelsEntry struct {
	body []byte
	at   time.Time
}

// modelsCall is one in-flight upstream read that concurrent misses share.
type modelsCall struct {
	done chan struct{}
	body []byte
}

// resetModels drops the cached catalogues (the upstream settings changed);
// a read already in flight then isn't cached.
func (s *Server) resetModels() {
	s.models.Lock()
	s.models.byGroup = nil
	s.models.gen++
	s.models.Unlock()
}

// upstreamModels is the raw /v1/models of an account of group, cached per
// group; concurrent misses wait for one upstream read.
func (s *Server) upstreamModels(ctx context.Context, cfg *config.Config, group string) []byte {
	if cfg.GroupPool(group) == config.ProviderTokenHarbor {
		return s.thModels(cfg, group)
	}
	s.models.Lock()
	if e, ok := s.models.byGroup[group]; ok && time.Since(e.at) < time.Duration(cfg.Routing.ModelsCacheSec)*time.Second {
		s.models.Unlock()
		return e.body
	}
	if c := s.models.inflight[group]; c != nil {
		s.models.Unlock()
		select {
		case <-c.done:
			return c.body
		case <-ctx.Done():
			return nil
		}
	}
	c := &modelsCall{done: make(chan struct{})}
	if s.models.inflight == nil {
		s.models.inflight = map[string]*modelsCall{}
	}
	s.models.inflight[group] = c
	gen := s.models.gen
	s.models.Unlock()

	// the read outlives a caller that leaves: others may be waiting on it
	c.body = s.fetchModels(context.WithoutCancel(ctx), cfg, group)
	s.models.Lock()
	delete(s.models.inflight, group)
	if c.body != nil && gen == s.models.gen {
		if s.models.byGroup == nil {
			s.models.byGroup = map[string]modelsEntry{}
		}
		s.models.byGroup[group] = modelsEntry{body: c.body, at: time.Now()}
	}
	s.models.Unlock()
	close(c.done)
	return c.body
}

// fetchModels is the pool's model list: the union of what every enabled
// account of group can serve. Accounts differ (each Modal proxy token
// reaches its own set of Endpoints, each OpenDesign key its own plan), so
// asking a single account would publish a fraction of the pool and hide
// models another account can take — a relay such as new-api never offers
// what it can't see. Which account actually serves a model is decided per
// request by the pool's own per-account catalogue, not here.
func (s *Server) fetchModels(ctx context.Context, cfg *config.Config, group string) []byte {
	if cfg.GroupPool(group) == config.ProviderTokenHarbor {
		return s.thModels(cfg, group)
	}
	if config.IsOpenCode(cfg.GroupPool(group)) {
		if !cfg.OpenCode.Enabled {
			return nil
		}
		data := s.openCodeModels(cfg, group)
		if len(data) == 0 {
			return nil
		}
		b, _ := json.Marshal(map[string]any{"object": "list", "data": data})
		return b
	}
	seen := map[string]bool{}
	var data []any
	var object any = "list"
	failed, read := false, 0
	// every enabled account, servable this second or not: a cooling-down
	// account's models are still models this pool offers once it is back
	for _, a := range cfg.Accounts {
		if a.Group != group || !a.Enabled {
			continue
		}
		// Perch has no OpenAI-compatible /models: RuntimeBase is the app host,
		// where /models is a 307 to the web app. Its models come from the
		// catalogue the quota poller already read (/api/perchai/models), so
		// publish those instead of asking the host.
		if a.Provider == config.ProviderPerch {
			if ms := s.quota.View(a.ID).PublicModels(time.Now()); ms != nil {
				read++
				for _, id := range ms {
					if id == "" || seen[id] {
						continue
					}
					seen[id] = true
					data = append(data, map[string]any{"id": id, "object": "model", "owned_by": "perch"})
				}
			} else {
				failed = true
			}
			continue
		}
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		req, _ := http.NewRequestWithContext(c, http.MethodGet, cfg.RuntimeBase(a)+"/models", nil)
		egressid.Apply(req.Header, egressid.For(a.ID))
		req.Header.Set("Authorization", "Bearer "+a.RuntimeKey)
		req.Header.Set("Accept", "application/json")
		client := s.httpClient(a.ProxyURL)
		if a.Provider == config.ProviderTokenHarbor || a.Provider == config.ProviderMistral {
			client = opencode.NoRedirect(client)
		}
		resp, err := client.Do(req)
		if err != nil {
			cancel()
			failed = true
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		cancel()
		if err != nil || resp.StatusCode != http.StatusOK {
			failed = true
			continue
		}
		var doc struct {
			Object string `json:"object"`
			Data   []any  `json:"data"`
		}
		if json.Unmarshal(b, &doc) != nil {
			failed = true
			continue
		}
		read++
		if doc.Object != "" {
			object = doc.Object
		}
		for _, it := range doc.Data {
			m, ok := it.(map[string]any)
			if !ok {
				continue
			}
			mid, _ := m["id"].(string)
			if mid == "" {
				continue
			}
			// an id this account disables still counts if another enables it
			if enabled, ok := m["enabled"].(bool); ok && !enabled {
				continue
			}
			// publish the base model an alias stands for: each Modal proxy
			// token has its own hostname for the same model, so without this
			// three accounts running DeepSeek offer three different names
			base, _ := m["base_model_id"].(string)
			id := quota.PublicModelID(mid, base)
			if a.Provider == config.ProviderMistral {
				id = mid // base_model_id is ancestry, not an alias (e.g. fine-tunes).
			}
			if seen[id] {
				continue
			}
			seen[id] = true
			entry := map[string]any{"id": id, "object": "model"}
			for _, k := range []string{"created", "owned_by", "context_length", "input_modalities",
				"max_output_length", "output_modalities", "supported_features", "supported_sampling_parameters"} {
				if v, ok := m[k]; ok {
					entry[k] = v
				}
			}
			if name, _ := m["name"].(string); name != "" {
				entry["name"] = name
			}
			// keep the upstream id visible without making it a second model:
			// a caller can still name it to target one account
			if id != mid {
				entry["upstream_ids"] = appendUpstream(entry["upstream_ids"], mid)
			}
			data = append(data, entry)
		}
	}
	if read == 0 || (failed && len(data) == 0) {
		// nobody could be read: fail, don't answer an empty catalogue
		return nil
	}
	out, err := json.Marshal(map[string]any{"object": object, "data": data})
	if err != nil {
		return nil
	}
	return out
}

var (
	concLimitRe = regexp.MustCompile(`concurrency limit exceeded\s*(\d+)\s*/\s*(\d+)`)
	rpmLimitRe  = regexp.MustCompile(`\brpm(?: limit exceeded)?\s*:?\s*(\d+)\s*/\s*(\d+)`)
)

// membershipLimits reads the limits a tier_limit_exceeded refusal names:
// "membership concurrency limit exceeded 6/5" → concurrency 5, "rpm 61/60"
// → 60. lower is the lowercased body.
func membershipLimits(lower string) (conc, rpm int) {
	if !strings.Contains(lower, "tier_limit_exceeded") && !strings.Contains(lower, "limit exceeded") {
		return 0, 0
	}
	if m := concLimitRe.FindStringSubmatch(lower); m != nil {
		conc, _ = strconv.Atoi(m[2])
	}
	if m := rpmLimitRe.FindStringSubmatch(lower); m != nil {
		rpm, _ = strconv.Atoi(m[2])
	}
	return conc, rpm
}

// egressLabel names an account's outbound path without its credentials.
func egressLabel(proxyURL string) string {
	if proxyURL == "" {
		return "direct"
	}
	if u, err := url.Parse(proxyURL); err == nil && u.Host != "" {
		return u.Host
	}
	return "proxy"
}

// appendUpstream adds an upstream id to a model entry's upstream_ids list.
func appendUpstream(v any, id string) []string {
	cur, _ := v.([]string)
	for _, x := range cur {
		if x == id {
			return cur
		}
	}
	return append(cur, id)
}

// upstreamBody rewrites a request's "model" to the id this account serves it
// by, leaving the bytes untouched when no translation is needed (the
// account's own catalogue already names the model).
func (s *Server) upstreamBody(cfg *config.Config, accountID, model string, body []byte) []byte {
	if model == "" || len(body) == 0 {
		return body
	}
	up := s.quota.View(accountID).UpstreamModel(model)
	if up == model {
		return body
	}
	var obj map[string]any
	if json.Unmarshal(body, &obj) != nil {
		return body
	}
	obj["model"] = up
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}
