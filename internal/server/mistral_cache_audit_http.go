package server

// Header/usage diagnostics share the existing exact-Key, TTL and request budget.
// No body capture, cookie jar, request rewriting or extra upstream read is added.
import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
)

const (
	mistralAuditHeaderLimit = 64
	mistralAuditValueLimit  = 512
	mistralAuditUsageLimit  = 8 << 10
)

type cacheAuditHeader struct {
	Count     int      `json:"count"`
	Values    []string `json:"values,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

type cacheAuditHTTP struct {
	Headers     map[string]cacheAuditHeader `json:"headers"`
	Truncated   bool                        `json:"truncated,omitempty"`
	CookieNames []string                    `json:"cookieNames,omitempty"`
}

// Only technical headers are readable. Unknown header values stay observable
// through per-run HMACs; credentials, cookies and session/cache values never do.
func readableAuditHeader(name string) bool {
	switch strings.ToLower(name) {
	case "host", ":authority", ":method", "content-type", "content-length", "content-encoding", "accept", "accept-encoding", "user-agent",
		"date", "server", "cache-control", "age", "vary", "via", "retry-after", "x-cache", "x-cache-status", "cf-cache-status", "cf-ray",
		"x-request-id", "mistral-correlation-id", "x-kong-request-id", "x-kong-upstream-latency", "x-kong-proxy-latency",
		"x-envoy-upstream-service-time", "x-served-by", "x-region", "x-datacenter", "x-service-tier", "service-tier",
		"x-ratelimit-limit-requests", "x-ratelimit-remaining-requests", "x-ratelimit-reset-requests",
		"x-ratelimit-limit-tokens", "x-ratelimit-remaining-tokens", "x-ratelimit-reset-tokens":
		return true
	}
	return false
}

func (c *cacheAuditAttempt) privateAuditValue(value string) bool {
	if c.cfg != nil {
		if redactSecrets(c.cfg, value) != value {
			return true
		}
		for _, key := range c.cfg.ClientKeys {
			if key.Key != "" && strings.Contains(value, key.Key) {
				return true
			}
		}
	}
	for _, secret := range c.identities {
		if secret != "" && strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func auditUsageFieldName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, ch := range name {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-') {
			return false
		}
	}
	return true
}

func (c *cacheAuditAttempt) auditHeader(name string, values []string) (string, cacheAuditHeader) {
	if len(name) > 128 || c.privateAuditValue(name) {
		name = "private-" + c.request.audit.tag("http-header-name", []byte(name))
	}
	out := cacheAuditHeader{Count: len(values), Truncated: len(values) > 4}
	for _, value := range values[:min(len(values), 4)] {
		switch {
		case isSecretHeader(name):
			out.Values = append(out.Values, "[REDACTED]")
		case readableAuditHeader(name) && len(value) <= mistralAuditValueLimit && !c.privateAuditValue(value) &&
			(!(strings.EqualFold(name, "Host") || name == ":authority") || value == "api.mistral.ai" || value == "api.eu.mistral.ai" || value == "api.us.mistral.ai"):
			out.Values = append(out.Values, value)
		default:
			out.Tags = append(out.Tags, c.request.audit.tag("http-header-value", []byte(value)))
			out.Truncated = out.Truncated || len(value) > mistralAuditValueLimit
		}
	}
	return http.CanonicalHeaderKey(name), out
}

func (c *cacheAuditAttempt) auditHeaders(headers http.Header) *cacheAuditHTTP {
	out := &cacheAuditHTTP{Headers: make(map[string]cacheAuditHeader)}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	out.Truncated = len(names) > mistralAuditHeaderLimit
	for _, name := range names[:min(len(names), mistralAuditHeaderLimit)] {
		key, value := c.auditHeader(name, headers[name])
		out.Headers[key] = value
	}
	return out
}

func (c *cacheAuditAttempt) recordHeader(name string, values []string) {
	key, value := c.auditHeader(name, values)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log.RequestHeadersObserved = true
	if _, exists := c.log.RequestHTTP.Headers[key]; !exists && len(c.log.RequestHTTP.Headers) >= mistralAuditHeaderLimit {
		c.log.RequestHTTP.Truncated = true
		return
	}
	c.log.RequestHTTP.Headers[key] = value
}

func (c *cacheAuditAttempt) response(resp *http.Response) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Sanitize response echoes of Cookie values without changing the immutable
	// request-side identity slice still used by late transport callbacks.
	privacy := &cacheAuditAttempt{request: c.request, cfg: c.cfg, identities: append([]string(nil), c.identities...)}
	for _, cookie := range resp.Cookies() {
		if len(cookie.Value) >= 8 {
			privacy.identities = append(privacy.identities, cookie.Value)
		}
	}
	c.log.ResponseHTTP = privacy.auditHeaders(resp.Header)
	for i, cookie := range resp.Cookies() {
		if i >= 16 {
			c.log.ResponseHTTP.Truncated = true
			break
		}
		name := cookie.Name
		if !auditUsageFieldName(name) || c.privateAuditValue(name) {
			name = "private-" + c.request.audit.tag("cookie-name", []byte(name))
		}
		c.log.ResponseHTTP.CookieNames = append(c.log.ResponseHTTP.CookieNames, name)
	}
	c.log.UpstreamProtocol = resp.Proto
	if resp.TLS != nil {
		c.log.TLSALPN = resp.TLS.NegotiatedProtocol
	}
}

// Keep all ordinary usage counters, nested fields, exact numbers and enums.
// Unexpected text (which can echo prompts, IDs or credentials) is never stored.
// Bounds apply to the whole value; no raw partial JSON is saved on overflow.
func (c *cacheAuditAttempt) captureUsage(data []byte) {
	if c == nil || !hasUsage(data) || !json.Valid(data) {
		return
	}
	root := scanMistralObject(data)
	if root == nil {
		return
	}
	raw := root.get("usage")
	for _, key := range []string{"response", "message"} {
		if len(raw) != 0 {
			break
		}
		if inner := scanMistralObject(root.get(key)); inner != nil {
			raw = inner.get("usage")
		}
	}
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return
	}
	c.log.UsageBytes = len(raw)
	c.log.UsageTag = c.request.audit.tag("http-usage", raw)
	c.log.Usage = nil
	c.log.UsageTruncated, c.log.UsageSanitized = false, false
	if len(raw) > mistralAuditUsageLimit {
		c.log.UsageTruncated = true
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return
	}
	nodes := 0
	var clean func(any, int) (any, bool)
	clean = func(value any, depth int) (any, bool) {
		nodes++
		if depth > 8 || nodes > 256 {
			return nil, false
		}
		switch v := value.(type) {
		case map[string]any:
			if len(v) > 64 {
				return nil, false
			}
			out := make(map[string]any, len(v))
			for key, child := range v {
				if !auditUsageFieldName(key) || c.privateAuditValue(key) {
					key = "private-" + c.request.audit.tag("usage-field", []byte(key))
					c.log.UsageSanitized = true
				}
				x, ok := clean(child, depth+1)
				if !ok {
					return nil, false
				}
				out[key] = x
			}
			return out, true
		case []any:
			if len(v) > 64 {
				return nil, false
			}
			out := make([]any, len(v))
			for i, child := range v {
				x, ok := clean(child, depth+1)
				if !ok {
					return nil, false
				}
				out[i] = x
			}
			return out, true
		case string:
			if !c.privateAuditValue(v) && (v == "standard" || v == "priority" || v == "USD" || v == "EUR") {
				return v, true
			}
			c.log.UsageSanitized = true
			return "[REDACTED]", true
		default:
			return value, true // json.Number / boolean / null
		}
	}
	cleaned, ok := clean(value, 0)
	if !ok {
		c.log.UsageTruncated = true
		return
	}
	c.log.Usage, _ = json.Marshal(cleaned)
}
