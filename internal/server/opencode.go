package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/egressid"
	"account-pool-balancer/internal/opencode"
	"account-pool-balancer/internal/quota"
)

func openCodeNativeProtocol(cfg *config.Config, a config.Account, snap *quota.Snapshot, model string, now time.Time) opencode.Protocol {
	protocol := opencode.Protocol(cfg.OpenCode.Protocol(a.Provider, model))
	if protocol == "" && snap != nil && snap.OpenCode != nil && now.Sub(snap.OpenCode.ModelsAt) < 24*time.Hour {
		protocol = snap.OpenCode.Models[model].Protocol
	}
	if opencode.Path(protocol) == "" {
		return ""
	}
	return protocol
}

func (s *Server) prepareOpenCode(cfg *config.Config, a config.Account, path, model string, body []byte) (opencode.Protocol, opencode.Protocol, []byte, error) {
	from := opencode.FromPath(path)
	if from == "" || model == "" {
		return from, "", nil, errors.New("OpenCode requires an exact supported endpoint and model")
	}
	if !cfg.OpenCode.Enabled {
		return from, "", nil, errors.New("OpenCode is disabled")
	}
	snap := s.quota.View(a.ID)
	if snap == nil || snap.OpenCode == nil || snap.OpenCode.Source != quota.OpenCodeSource(cfg, a) || !snap.Catalog[model] {
		return from, "", nil, errors.New("account model catalog unavailable; sync this account first")
	}
	to := openCodeNativeProtocol(cfg, a, snap, model, time.Now())
	if opencode.Path(to) == "" {
		return from, to, nil, errors.New("model native protocol unknown; refresh capabilities or configure a tier-specific protocol override")
	}
	if from == to {
		return from, to, body, nil
	}
	if !cfg.OpenCode.Compatibility {
		return from, to, nil, errors.New("protocol conversion is disabled; use the model's native endpoint or enable limited compatibility")
	}
	b, err := opencode.ConvertRequest(from, to, body)
	return from, to, b, err
}

func (s *Server) openCodeCompatibility(w http.ResponseWriter, r *http.Request, cfg *config.Config, resp *http.Response, res *attemptResult, from, to opencode.Protocol, stream bool, attempt int, start time.Time) *attemptResult {
	fail := func(err error) *attemptResult {
		if r.Context().Err() != nil {
			res.kind = outCancelled
			return res
		}
		if resp.Request != nil && resp.Request.Context().Err() != nil {
			res.kind, res.status, res.terminal, res.category = outFailed, 504, "timeout", "timeout"
			return res
		}
		// The upstream already accepted and may have billed this call. Conversion
		// failures are not evidence of a bad account and must not trigger replay.
		res.kind, res.status, res.terminal, res.category = outFailed, 502, "compatibility", "compatibility"
		res.contentType = "application/json"
		res.body, _ = json.Marshal(map[string]any{"error": map[string]string{"type": "api_error", "code": "upstream_compatibility_error", "message": err.Error()}})
		return res
	}
	envelopeFailure := func(payload []byte) *attemptResult {
		var m map[string]any
		_ = json.Unmarshal(payload, &m)
		res.kind, res.status, res.terminal, res.category = outFailed, errorStatus(m, payload), "upstream_envelope", "upstream"
		res.body, res.contentType = payload, "application/json"
		return res
	}
	var reply *opencode.Reply
	var err error
	if strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		reply, err = opencode.ReadStream(from, resp.Body)
	} else {
		var b []byte
		b, err = io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
		if err == nil && len(b) > 16<<20 {
			err = errors.New("compatibility response exceeds 16 MiB")
		}
		if err == nil {
			var m map[string]any
			if json.Unmarshal(b, &m) == nil && isEnvelopeError(m) {
				return envelopeFailure(b)
			}
			reply, err = opencode.DecodeReply(from, b)
		}
	}
	if err != nil {
		var streamError *opencode.StreamError
		if errors.As(err, &streamError) && streamError.BeforeContent {
			return envelopeFailure(streamError.Payload)
		}
		return fail(err)
	}
	var b []byte
	if stream {
		b, err = reply.Stream(to)
	} else {
		var m map[string]any
		m, err = reply.Encode(to)
		if err == nil {
			b, err = json.Marshal(m)
		}
	}
	if err != nil {
		return fail(err)
	}
	h := w.Header()
	h.Set("X-Compatibility-Mode", "buffered")
	h.Set("X-Attempts", strconv.Itoa(attempt))
	setUpstreamRequestID(h, resp.Header, "")
	h.Set("Content-Type", "application/json")
	if stream {
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
	}
	if r.Context().Err() != nil {
		res.kind = outCancelled
		return res
	}
	w.WriteHeader(200)
	if _, err = w.Write(b); err != nil {
		res.kind = outCancelled
		return res
	}
	if stream {
		_ = http.NewResponseController(w).Flush()
	}
	if reply.Usage.Known {
		in, out, cached := reply.Usage.Input, reply.Usage.Output, reply.Usage.Cached
		writes, reasoning := reply.Usage.CacheWrite, reply.Usage.Reasoning
		if to == opencode.Messages {
			in = max(0, in-cached-writes)
		}
		res.usage = usage{in: &in, out: &out}
		if reply.Usage.CachedKnown {
			res.usage.cached = &cached
		}
		if reply.Usage.CacheWriteKnown {
			res.usage.cacheWrite = &writes
		}
		if reply.Usage.ReasoningKnown {
			res.usage.reasoning = &reasoning
		}
	}
	ms := time.Since(start).Milliseconds()
	res.ttft = &ms
	res.kind, res.status, res.result, res.terminal = outDelivered, 200, "success", "success"
	return res
}

// Catalog publication is based exclusively on per-account snapshots, never a
// group-level model listing from a different key or tier.
func (s *Server) openCodeModels(cfg *config.Config, group string) []any {
	out := s.openCodeGroupModels(cfg, group)
	if fallback := cfg.OpenCode.FallbackGroup(cfg.GroupPool(group)); fallback != "" {
		seen := map[string]bool{}
		for _, v := range out {
			seen[v.(map[string]any)["id"].(string)] = true
		}
		for _, v := range s.openCodeGroupModels(cfg, fallback) {
			if !seen[v.(map[string]any)["id"].(string)] {
				out = append(out, v)
			}
		}
	}
	return out
}

func (s *Server) openCodeGroupModels(cfg *config.Config, group string) []any {
	out := []any{}
	seen := map[string]bool{}
	now := time.Now()
	for _, a := range cfg.Accounts {
		if a.Group != group || !a.Enabled || !config.IsOpenCode(a.Provider) {
			continue
		}
		snap := s.quota.View(a.ID)
		if snap == nil || snap.OpenCode == nil || snap.OpenCode.Source != quota.OpenCodeSource(cfg, a) || now.Sub(snap.CatalogAt) >= 24*time.Hour {
			continue
		}
		for id, en := range snap.Catalog {
			if !en || seen[id] {
				continue
			}
			m := snap.OpenCode.Models[id]
			m.Protocol = openCodeNativeProtocol(cfg, a, snap, id, now)
			if opencode.Path(m.Protocol) == "" {
				continue
			}
			seen[id] = true
			entry := map[string]any{"id": id, "object": "model", "owned_by": a.Provider, "native_protocol": m.Protocol, "metadata": m}
			if m.Context > 0 {
				entry["context_window"] = m.Context
			}
			if m.Input > 0 {
				entry["max_input"] = m.Input
			}
			if m.Output > 0 {
				entry["max_output"] = m.Output
			}
			out = append(out, entry)
		}
	}
	return out
}

func (s *Server) testOpenCode(ctx context.Context, cfg *config.Config, a config.Account, inference bool, model string) map[string]any {
	out := map[string]any{}
	if !cfg.OpenCode.Enabled {
		out["runtime"] = map[string]any{"ok": false, "error": "OpenCode is disabled; no upstream was contacted"}
		return out
	}
	err := s.quota.RefreshNow(ctx, a)
	snap := s.quota.Get(a.ID)
	out["runtime"] = map[string]any{"ok": snap != nil && snap.CatalogError == "" && !snap.CatalogAt.IsZero(), "note": "model listing is not proof of inference authorization"}
	if err != nil {
		out["diagnostic"] = err.Error()
	}
	if !inference {
		return out
	}
	if model == "" {
		model = s.firstCatalogModel(a)
	}
	native := opencode.Protocol(cfg.OpenCode.Protocol(a.Provider, model))
	if native == "" && snap != nil && snap.OpenCode != nil {
		native = snap.OpenCode.Models[model].Protocol
	}
	if model == "" || opencode.Path(native) == "" {
		out["inference"] = map[string]any{"ok": false, "error": "sync models and native protocol first"}
		return out
	}
	payload := map[string]any{"model": model, "stream": false, "max_tokens": 1, "messages": []any{map[string]any{"role": "user", "content": "ping"}}}
	if native == opencode.Responses {
		delete(payload, "messages")
		delete(payload, "max_tokens")
		payload["input"] = "ping"
		payload["max_output_tokens"] = 1
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg.InferenceBase(a, string(native))+opencode.Path(native), strings.NewReader(string(b)))
	egressid.Apply(req.Header, egressid.For(a.ID))
	req.Header.Set("Content-Type", "application/json")
	if native == opencode.Messages {
		req.Header.Set("X-Api-Key", a.RuntimeKey)
		req.Header.Set("Anthropic-Version", "2023-06-01")
		if a.Provider == config.ProviderOpenCodeGo {
			q := req.URL.Query()
			q.Set("beta", "true")
			req.URL.RawQuery = q.Encode()
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+a.RuntimeKey)
	}
	start := time.Now()
	resp, err := opencode.NoRedirect(s.httpClient(a.ProxyURL)).Do(req)
	if err != nil {
		out["inference"] = map[string]any{"ok": false, "error": "OpenCode connection failed"}
		return out
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	result := map[string]any{"ok": resp.StatusCode/100 == 2, "status": resp.StatusCode, "ms": time.Since(start).Milliseconds(), "model": model, "nativeProtocol": native}
	if resp.StatusCode/100 != 2 {
		result["error"], _ = errorReason(cfg, body, resp.Header.Get("Content-Type"))
	}
	out["inference"] = result
	return out
}
