package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/factory"
	"account-pool-balancer/internal/quota"
)

const factoryTransportPrefix = "\x00factory:"

func (s *Server) factoryHTTPClient(cfg *config.Config, proxy string) *http.Client {
	r := cfg.Factory.EffectiveRouting()
	key := factoryTransportPrefix + strconv.Itoa(r.DirectMaxConns) + ":" + strconv.Itoa(r.ProxyMaxConns) + ":" + proxy
	return &http.Client{Transport: s.factoryTransportFor(proxy, r, key), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// factoryTransportFor keeps Factory's model plane on HTTP/1.1, matching the
// captured droid request (Connection: keep-alive). Go's automatic HTTP/2 is a
// different SETTINGS/priority fingerprint and would also hide the connection
// header Factory saw. The proxy is explicit, so there is no direct fallback.
func (s *Server) factoryTransportFor(proxyURL string, cfg config.Routing, key string) *http.Transport {
	if t, ok := s.transports.Load(key); ok {
		return t.(*http.Transport)
	}
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if t, ok := s.transports.Load(key); ok {
		return t.(*http.Transport)
	}
	t := &http.Transport{
		ForceAttemptHTTP2:     false,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   cfg.DirectMaxConns,
		MaxConnsPerHost:       cfg.DirectMaxConns,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: time.Second,
		// The gateway sends droid's own Accept-Encoding itself (see
		// factoryAcceptEncoding). Go's automatic gzip would add a header only
		// it knows about and would not match the CLI identity.
		DisableCompression: true,
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			t.Proxy = func(*http.Request) (*url.URL, error) { return nil, errors.New("invalid proxy URL") }
		} else {
			t.Proxy = http.ProxyURL(u)
		}
		t.MaxIdleConnsPerHost, t.MaxConnsPerHost = cfg.ProxyMaxConns, cfg.ProxyMaxConns
	}
	s.transports.Store(key, t)
	return t
}

func factoryResponsesTruncated(res *attemptResult) *attemptResult {
	res.kind, res.result, res.status = outFailed, "failed", 502
	res.terminal, res.category, res.contentType = "incomplete_stream", "network", "application/json"
	res.body = []byte(`{"type":"error","error":{"type":"api_error","code":"factory_response_incomplete","message":"Factory Responses stream ended before a terminal event; the request was not replayed"}}`)
	return res
}

func factoryCompletionValid(b []byte, path string) bool {
	var d struct {
		Choices        []json.RawMessage `json:"choices"`
		Content        []json.RawMessage `json:"content"`
		Output         []json.RawMessage `json:"output"`
		Candidates     []json.RawMessage `json:"candidates"`
		PromptFeedback json.RawMessage   `json:"promptFeedback"`
		TotalTokens    json.RawMessage   `json:"totalTokens"`
		Data           []json.RawMessage `json:"data"`
		Error          json.RawMessage   `json:"error"`
		Status         string            `json:"status"`
		Type           string            `json:"type"`
	}
	if json.Unmarshal(b, &d) != nil {
		return false
	}
	if len(d.Error) > 0 && string(d.Error) != "null" || d.Type == "error" {
		return true
	} // shared error path owns classification
	if _, _, ok := factory.ParseGeminiPath(path); ok {
		// A blocked prompt can return 200 with promptFeedback and no
		// candidates; that is still a complete upstream answer to relay.
		return len(d.Candidates) > 0 || len(d.PromptFeedback) > 0 && string(d.PromptFeedback) != "null"
	}
	if _, ok := factory.ParseGeminiCountTokens(path); ok {
		return len(d.TotalTokens) > 0
	}
	if path == "/v1/embeddings" {
		return len(d.Data) > 0
	}
	switch path {
	case "/v1/responses":
		return d.Status == "completed" && len(d.Output) > 0
	case "/v1/messages":
		return d.Type == "message" && len(d.Content) > 0
	default:
		return len(d.Choices) > 0
	}
}

func factoryPolicyRefused(status int, b []byte, h http.Header) bool {
	if status != 403 {
		return false
	}
	var doc struct {
		Code  string `json:"code"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return strings.Contains(strings.ToLower(string(b)), "content_policy_rejected")
	}
	return doc.Code == "content_policy_rejected" || doc.Error != nil && doc.Error.Code == "content_policy_rejected" || strings.Contains(strings.ToLower(string(b)), "content_policy_rejected")
}

// factoryHeaders writes the headers droid sends on a model call to Factory's
// prem plane, after dropping whatever client identity the caller presented.
// Factory serves a subscription's models to droid only: an honest gateway
// User-Agent or a forwarded agent's own x-session-id is what gets refused
// (magpie #242, #506), so these are replaced rather than preserved.
//
// The session and message ids derive from the gateway's own conversation
// fingerprint: one conversation keeps one session, and the values never carry
// caller text. The traceparent reuses that fingerprint so a conversation's
// calls share one trace while each message gets a distinct span.
func (s *Server) factoryHeaders(cfg *config.Config, h http.Header, acct config.Account, m config.FactoryModel, plan *factoryRequestPlan) {
	// Factory model calls must look exactly like droid's. Do not forward any
	// downstream header: the caller's headers can carry its address
	// (X-Forwarded-For and many CDN spellings), SDK/browser fingerprints
	// (x-stainless-*, Sec-*, OpenAI/Anthropic/Google headers), locale, origin,
	// trace baggage or a different Anthropic version. Rebuild the header set
	// from droid's identity alone, so an added proxy cannot be undone by a
	// header that names another egress or client.
	// Anthropic-Beta is functional feature negotiation, not an address or
	// transport fingerprint; keep the caller's beta list (droid's fast-mode
	// beta is added later) and drop every other downstream value.
	anthropicBeta := h.Get("Anthropic-Beta")
	for name := range h {
		h.Del(name)
	}
	h.Set("Authorization", "Bearer "+acct.RuntimeKey)
	// WorkOS's active organization is what droid sends; a fk- account has no
	// WorkOS org, so whoami's Factory org is the only value.
	org := acct.FactoryWorkOSOrgID
	if org == "" {
		org = acct.FactoryOrgID
	}
	if org == "" {
		if v := s.quota.View(acct.ID); v != nil && v.Factory != nil {
			org = v.Factory.OrgID
		}
	}
	if org != "" {
		h.Set("X-Factory-Org-Id", org)
	}
	factory.ApplyClientHeaders(h)
	ua := cfg.Factory.EffectiveUserAgent(acct)
	if strings.HasPrefix(ua, "Mozilla/") {
		// Config validation refuses this; a stored value from an older config
		// must not turn a CLI call into half a browser.
		ua = config.FactoryDefaultInferenceUserAgent
	}
	h.Set("User-Agent", ua)
	h.Set("X-Client-Version", config.FactoryClientVersion(ua))
	session, messageID, traceparent := "", "", ""
	if plan != nil {
		session, messageID, traceparent = plan.session, plan.messageID, plan.traceparent
	}
	// Only the headers the capture shows for this wire protocol: Chat
	// Completions and Messages carry no traceparent in droid 1.14.0.
	factory.ApplyProtocolForwardHeaders(h, wireProtocol(m.Protocol, plan), m.Upstream, session, messageID, cfg.Factory.EffectiveRequestSurface(), traceparent)
	// droid's model calls are JSON requests. Chat/Responses/Messages declare
	// JSON even when the response is an SSE stream; the Gemini route sends the
	// capture's wildcard Accept instead.
	// Accept-Encoding is droid's list (Bun's client offers every codec it
	// decodes); the response path decodes whatever the upstream chooses, so
	// this is never an undecodable body.
	h.Set("Accept-Encoding", factoryAcceptEncoding)
	// The codec list is droid's; nothing browser-shaped is added beside it.
	// Nothing sets Accept-Language, Origin or Sec-* on this request, and the
	// transport would have added gzip automatically if it were still enabled.
	h.Set("Content-Type", "application/json")
	if m.Protocol == "generate" {
		h.Set("Accept", "*/*")
	} else {
		h.Set("Accept", "application/json")
	}
	// droid's OpenAI client names Factory's platform organization with an
	// opaque `org-…` token. whoami does not expose that mapping, so it is sent
	// only when the operator configured the value observed for their org;
	// a made-up constant would be a different identity, not droid's.
	if m.Upstream == "openai" && cfg.Factory.OpenAIPlatform != "" {
		h.Set("OpenAI-Platform", cfg.Factory.OpenAIPlatform)
	}
	if m.Protocol == "messages" || m.Protocol == "anthropic" {
		// droid's Anthropic client is made with the literal key "placeholder"
		// and the same protocol version as the capture.
		factory.ApplyAPIEndpointHeaders(h)
		h.Set("Anthropic-Version", "2023-06-01")
		if anthropicBeta != "" {
			h.Set("Anthropic-Beta", anthropicBeta)
		}
	}
}

// wireProtocol maps a configured model route to the protocol actually put on
// the wire, which is what the captured header sets are keyed by.
func wireProtocol(protocol string, plan *factoryRequestPlan) string {
	if plan != nil && string(plan.to) != "" {
		return string(plan.to)
	}
	switch protocol {
	case "chat", "responses", "messages", "generate":
		return protocol
	case "anthropic":
		return "messages"
	}
	return "chat"
}

// factoryRequestIDs derives droid's x-session-id, x-assistant-message-id and
// traceparent for one request from the gateway's conversation fingerprint.
// They are opaque HMAC digests; none is caller text and none crosses accounts.
func (s *Server) factoryRequestIDs(cfg *config.Config, h http.Header, body []byte) (session, messageID, traceparent string) {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return "", "", factory.RandomTraceparent()
	}
	det := s.identity(cfg, config.ProviderFactory, h, m)
	if det.Fingerprint == "" {
		// Gemini's conversation is in contents/systemInstruction, which the
		// shared identity path only reaches through the fallback hash. Derive
		// droid's session from it even when that switch is off.
		if stable := stableConversation(m); stable != "" {
			det = Identity{Source: "message_hmac", KeyType: "message_hmac", Confidence: "fallback", Fingerprint: s.fingerprint(config.ProviderFactory, "gemini", stable)}
		}
	}
	if det.Fingerprint == "" {
		return "", "", factory.RandomTraceparent()
	}
	// The message id covers this request's own bytes as well as the
	// conversation, so two turns of one session are never the same message.
	messageHex := s.fingerprint(config.ProviderFactory, "factory-message", det.Fingerprint+"\x00"+string(body))
	return uuidFromDigest(det.Fingerprint), uuidFromDigest(messageHex), factory.Traceparent(det.Fingerprint[:32], messageHex[:16])
}

// uuidFromDigest renders a hex digest as a UUID-shaped value. Factory's ids are
// UUIDs; these bytes are a keyed digest, and are labelled as version 4 so that
// a reader parsing the shape does not reject them.
func uuidFromDigest(hexDigest string) string {
	if len(hexDigest) < 32 {
		return ""
	}
	b := []byte(hexDigest[:32])
	b[12] = '4'
	// variant nibble 8, 9, a or b
	b[16] = "89ab"[(b[16]>>2)&3]
	return string(b[0:8]) + "-" + string(b[8:12]) + "-" + string(b[12:16]) + "-" + string(b[16:20]) + "-" + string(b[20:32])
}

func factoryOverviewFor(views []accountView) map[string]any {
	identityFresh, quotaFresh := 0, 0
	for _, v := range views {
		if v.Provider != config.ProviderFactory {
			continue
		}
		if v.Factory != nil && !v.Factory.IdentityAt.IsZero() && time.Since(v.Factory.IdentityAt) < 15*time.Minute {
			identityFresh++
		}
		if v.QuotaFresh {
			quotaFresh++
		}
	}
	return map[string]any{"identityFresh": identityFresh, "quotaFresh": quotaFresh, "quotaReason": quota.FactoryQuotaReason, "catalogSource": config.FactoryReference}
}

func factoryModels(cfg *config.Config, group string) []byte {
	data := []map[string]any{}
	if len(cfg.Factory.Models) > 0 {
		data = append(data, map[string]any{"id": "auto", "object": "model", "owned_by": "factory", "protocol": "auto", "x_api_provider": "factory", "catalog_source": "gateway-virtual-auto"})
	}
	hasFast := false
	for _, m := range cfg.Factory.Models {
		if strings.HasSuffix(m.ID, "-fast") {
			hasFast = true
			break
		}
	}
	if hasFast {
		data = append(data, map[string]any{"id": "auto-fast", "object": "model", "owned_by": "factory", "protocol": "auto", "x_api_provider": "factory", "catalog_source": "gateway-virtual-auto"})
	}
	for _, a := range cfg.Accounts {
		if a.Provider == config.ProviderFactory && a.Group == group && a.Enabled {
			for _, m := range cfg.Factory.Models {
				data = append(data, map[string]any{"id": m.ID, "object": "model", "owned_by": "factory", "protocol": m.Protocol, "x_api_provider": m.Upstream, "catalog_source": "configured-reference-not-entitlement"})
			}
			break
		}
	}
	b, _ := json.Marshal(map[string]any{"object": "list", "data": data})
	return b
}

// testFactory separates free identity checks, quota permission, and paid model
// inference. All bodies keep the caller's native protocol; no policy adapter.
func (s *Server) testFactory(ctx context.Context, cfg *config.Config, a config.Account, inference bool, model string) map[string]any {
	started := time.Now()
	if a.Provider == config.ProviderFactory && a.FactoryRefreshToken != "" {
		next, terr := s.factoryAccountToken(ctx, cfg, a)
		if terr != nil {
			return map[string]any{"runtime": map[string]any{"ok": false, "ms": time.Since(started).Milliseconds(), "error": "Factory OAuth token refresh failed"}}
		}
		a = next
	}
	err := s.quota.RefreshNow(ctx, a)
	q := s.quota.Get(a.ID)
	out := map[string]any{"runtime": map[string]any{"ok": err == nil, "ms": time.Since(started).Milliseconds(), "note": "只读whoami身份检查；不是推理授权。模型列表为配置参考，非账号实际权限。"}}
	if err != nil {
		out["runtime"].(map[string]any)["error"] = redactSecrets(cfg, err.Error())
	}
	if q != nil && q.Factory != nil {
		out["factory"] = q.Factory
	}
	out["quotaReason"] = quota.FactoryQuotaReason
	s.metaDirty.Store(true)
	if !inference {
		return out
	}
	if err != nil {
		out["inference"] = map[string]any{"ok": false, "error": "身份检查未通过，不继续发送付费推理"}
		return out
	}
	current := s.cfg.Get()
	next, exists := current.Account(a.ID)
	if !exists || !current.PoolEnabled(a.Provider) || quota.ResponseBindingSource(cfg, a) != quota.ResponseBindingSource(current, next) || ctx.Err() != nil {
		out["inference"] = map[string]any{"ok": false, "error": "账号来源变化、号池关闭或测试已取消"}
		return out
	}
	if model == "" {
		model = config.FactoryDefaultModel
	}
	if model == "auto" || model == "auto-fast" {
		if resolved, ok := resolveFactoryAuto(cfg, model, "chat"); ok {
			model = resolved
		}
	}
	m, ok := cfg.Factory.Model(model)
	if !ok {
		out["inference"] = map[string]any{"ok": false, "error": "模型未配置，请在Factory设置中配置原生协议与上游"}
		return out
	}
	payload := map[string]any{"model": model, "stream": false}
	path := "/v1/chat/completions"
	switch m.Protocol {
	case "messages":
		path = "/v1/messages"
		payload["max_tokens"] = 32
		payload["messages"] = []map[string]string{{"role": "user", "content": "Reply with exactly OK."}}
	case "responses":
		path = "/v1/responses"
		payload["max_output_tokens"] = 64
		payload["input"] = "Reply with exactly OK."
	case "generate":
		path = "/v1beta/models/" + model + ":generateContent"
		payload = map[string]any{"contents": []map[string]any{{"role": "user", "parts": []map[string]string{{"text": "Reply with exactly OK."}}}}, "generationConfig": map[string]any{"maxOutputTokens": 64}}
	case "embeddings":
		path = "/v1/embeddings"
		payload = map[string]any{"model": model, "input": "Reply with exactly OK."}
	default:
		payload["max_tokens"] = 64
		payload["messages"] = []map[string]string{{"role": "user", "content": "Reply with exactly OK."}}
	}
	b, _ := json.Marshal(payload)
	plan, prepareErr := prepareFactory(cfg, path, b)
	if prepareErr != nil {
		out["inference"] = map[string]any{"ok": false, "error": prepareErr.Error()}
		return out
	}
	b = plan.body
	endpoint := cfg.InferenceBase(a, m.Protocol) + path[3:]
	if plan.generate {
		endpoint = cfg.InferenceBase(a, "generate") + "/generate"
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+a.RuntimeKey)
	req.Header.Set("Content-Type", "application/json")
	// The same identity the proxy sends: a test must fail for the reason the
	// production request would fail, not for a different client header.
	plan.session, plan.messageID, plan.traceparent = s.factoryRequestIDs(cfg, req.Header, b)
	s.factoryHeaders(cfg, req.Header, a, m, plan)
	if m.Protocol == "messages" {
		req.Header.Set("Anthropic-Version", "2023-06-01")
		if plan.beta {
			req.Header.Set("Anthropic-Beta", factoryFastBeta)
		}
	}
	start := time.Now()
	resp, e := s.factoryHTTPClient(cfg, a.ProxyURL).Do(req)
	result := map[string]any{"ok": false, "model": model, "protocol": m.Protocol}
	out["inference"] = result
	if e != nil {
		result["error"] = "Factory network request failed"
		return out
	}
	defer resp.Body.Close()
	// The transport sends droid's Accept-Encoding and Go does not decode
	// automatically, so the test reads the decoded body like the proxy does.
	if enc := resp.Header.Get("Content-Encoding"); enc != "" {
		decoded, derr := decodeFactoryBody(resp.Body, enc)
		if derr != nil {
			result["error"] = "Factory response used an unsupported content-encoding"
			return out
		}
		resp.Body = decoded
	}
	raw, e := io.ReadAll(io.LimitReader(resp.Body, (64<<10)+1))
	result["status"], result["ms"] = resp.StatusCode, time.Since(start).Milliseconds()
	if e != nil || len(raw) > 64<<10 {
		result["error"] = "响应不完整或超过测试读取上限"
		return out
	}
	if resp.StatusCode != 200 {
		result["error"] = redactSecrets(cfg, string(raw))
		result["policyRefused"] = factoryPolicyRefused(resp.StatusCode, raw, resp.Header)
		return out
	}
	var doc struct {
		Choices []json.RawMessage `json:"choices"`
		Content []json.RawMessage `json:"content"`
		Output  []json.RawMessage `json:"output"`
		Error   json.RawMessage   `json:"error"`
	}
	if json.Unmarshal(raw, &doc) == nil && (len(doc.Error) == 0 || string(doc.Error) == "null") {
		result["ok"] = factoryCompletionValid(raw, path)
	}
	result["body"] = redactSecrets(cfg, string(raw))
	return out
}
