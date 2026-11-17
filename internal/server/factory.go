package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/quota"
)

const factoryTransportPrefix = "\x00factory:"

func (s *Server) factoryHTTPClient(cfg *config.Config, proxy string) *http.Client {
	r := cfg.Factory.EffectiveRouting()
	key := factoryTransportPrefix + strconv.Itoa(r.DirectMaxConns) + ":" + strconv.Itoa(r.ProxyMaxConns) + ":" + proxy
	return &http.Client{Transport: s.transportFor(proxy, r, key), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func factoryResponsesTruncated(res *attemptResult) *attemptResult {
	res.kind, res.result, res.status = outFailed, "failed", 502
	res.terminal, res.category, res.contentType = "incomplete_stream", "network", "application/json"
	res.body = []byte(`{"type":"error","error":{"type":"api_error","code":"factory_response_incomplete","message":"Factory Responses stream ended before a terminal event; the request was not replayed"}}`)
	return res
}

func factoryCompletionValid(b []byte, path string) bool {
	var d struct {
		Choices []json.RawMessage `json:"choices"`
		Content []json.RawMessage `json:"content"`
		Output  []json.RawMessage `json:"output"`
		Error   json.RawMessage   `json:"error"`
		Status  string            `json:"status"`
		Type    string            `json:"type"`
	}
	if json.Unmarshal(b, &d) != nil {
		return false
	}
	if len(d.Error) > 0 && string(d.Error) != "null" || d.Type == "error" {
		return true
	} // shared error path owns classification
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
		return false
	}
	return doc.Code == "content_policy_rejected" || doc.Error != nil && doc.Error.Code == "content_policy_rejected"
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg.InferenceBase(a, m.Protocol)+path[3:], bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+a.RuntimeKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "account-pool-balancer/factory-gateway")
	req.Header.Set("X-Api-Provider", m.Upstream)
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
