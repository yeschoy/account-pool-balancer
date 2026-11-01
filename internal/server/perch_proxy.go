package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perch"
	"account-pool-balancer/internal/pool"
)

// The Perch pool's request path.
//
// Everything around this function is shared with the other pools: the lease
// (one account, its concurrency and RPM budget), the surrounding retry loop
// that switches accounts on a failure, the request log, and the error rules.
// What is Perch-specific lives here and in internal/perch:
//
//   - the body is an OpenAI chat body, and Perch wants its own envelope;
//   - the answer is Perch's event stream, and the client wants OpenAI SSE;
//   - the credential is a Supabase access token kept by the session keeper,
//     renewed on the way in when it is due.
//
// Nothing is shared between accounts: the token is read for this account only,
// under this account's lease, and a failure is recorded against this account
// only.

// perchRequestOpts reads the console's Perch settings into what the envelope
// builder needs.
func (s *Server) perchRequestOpts(cfg *config.Config, a config.Account) perch.EnvelopeOptions {
	p := cfg.Perch
	opt := perch.EnvelopeOptions{
		Lane:          p.Lane,
		GateNotice:    p.GateNotice,
		IdentityGuard: p.IdentityGuard,
		NeutralSystem: p.NeutralSystem,
		Prefix:        s.perchGatePrefix(),
	}
	if opt.Lane == "" {
		opt.Lane = config.DefaultPerch().Lane
	}
	if opt.GateNotice == "" {
		opt.GateNotice = config.DefaultPerch().GateNotice
	}
	if opt.IdentityGuard == "" {
		opt.IdentityGuard = config.DefaultPerch().IdentityGuard
	}
	if opt.NeutralSystem == "" {
		opt.NeutralSystem = config.DefaultPerch().NeutralSystem
	}
	opt.Attribution = map[string]any{"userId": a.PerchUserID, "source": "cli"}
	return opt
}

// perchGatePrefix resolves the surface-gate preamble, caching it in memory: it
// is derived once from an installed Perch client (or read from settings) and
// does not change while the process runs.
func (s *Server) perchGatePrefix() string {
	s.perchPrefixMu.Lock()
	defer s.perchPrefixMu.Unlock()
	if s.perchPrefixKnown {
		return s.perchPrefix
	}
	cfg := s.cfg.Get()
	if p := strings.TrimSpace(cfg.Perch.GatePrefix); p != "" {
		s.perchPrefix, s.perchPrefixSource, s.perchPrefixKnown = p, "settings", true
		return p
	}
	p, src := perch.DeriveGatePrefix()
	s.perchPrefix, s.perchPrefixSource, s.perchPrefixKnown = p, src, true
	return p
}

// attemptPerch serves one attempt for a Perch account.
func (s *Server) attemptPerch(w http.ResponseWriter, r *http.Request, cfg *config.Config, lease *pool.Lease, body []byte, proto string, rec *requestLog, ctx context.Context, res *attemptResult, timedOut *atomic.Bool) *attemptResult {
	acct := lease.Account
	prefix := s.perchGatePrefix()
	if err := perch.CheckGatePrefix(prefix); err != nil {
		// Without the preamble Perch refuses every call, so failing here with a
		// clear message beats forwarding and reporting Perch's 403 forever.
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusServiceUnavailable, "perch_gate", "config"
		res.body = []byte(err.Error())
		return res
	}

	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusBadRequest, "request", "request"
		res.body = []byte("request body must be a JSON object")
		return res
	}
	opt := s.perchRequestOpts(cfg, acct)
	opt.Model = rec.Model
	env := perch.BuildEnvelope(obj, opt)
	payload, err := json.Marshal(env)
	if err != nil {
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusInternalServerError, "request", "request"
		res.body = []byte("cannot build the Perch request")
		return res
	}

	// An account whose refresh token was refused last time has no usable
	// credential: serving it would spend a request to learn the same thing.
	// It comes back into service when an operator pastes a fresh session or a
	// later renewal succeeds.
	if cur, ok := s.cfg.Get().Account(acct.ID); ok && cur.PerchRefreshFailed {
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusUnauthorized, "upstream_http", "upstream"
		res.body = []byte(`{"ok":false,"error":"this account's Perch session was refused and needs signing in again","errorCode":"perch_session_invalid"}`)
		res.contentType = "application/json"
		res.accountScoped = true
		return res
	}

	// The token is read for THIS account, renewed first when due. A refusal
	// here is the account's problem and is recorded as such.
	rr := s.perch.keeper.Refresh(ctx, acct.ID, false)
	if rr.NeedLogin {
		s.markPerchNeedLogin(acct.ID, rr.Err)
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusUnauthorized, "upstream_http", "upstream"
		res.body = []byte(`{"ok":false,"error":"this account's Perch session has expired and needs signing in again","errorCode":"perch_session_invalid"}`)
		res.contentType = "application/json"
		res.accountScoped = true
		return res
	}
	tok, ok := s.perch.keeper.Get(acct.ID)
	if !ok || tok.AccessToken == "" {
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusUnauthorized, "upstream_http", "upstream"
		res.body = []byte("this account has no Perch session")
		res.accountScoped = true
		return res
	}

	base := cfg.Upstream.PerchBase
	if base == "" {
		base = perch.AppURL
	}
	stream := rec.Stream
	var firstEvent atomic.Bool
	onEvent := func(ev perch.Event) error {
		if timedOut.Load() {
			return errors.New("the request timed out")
		}
		return nil
	}

	call := perch.ModelCall{Base: base, Token: tok.AccessToken, Body: payload, ProxyURL: acct.ProxyURL}
	var timeout time.Duration
	if stream {
		timeout = time.Duration(cfg.Routing.RequestSec) * time.Second
	} else {
		timeout = time.Duration(cfg.Routing.RequestSec) * time.Second
	}

	if !stream {
		// A non-streaming request is assembled from the same events.
		result, err := s.perchClient().CallModel(ctx, call, timeout, func(perch.Event) error {
			firstEvent.Store(true)
			return onEvent(perch.Event{})
		})
		if err != nil {
			return s.perchFailure(res, acct, err, rec)
		}
		lease.MarkSent()
		out := map[string]any{
			"id":      "chatcmpl-" + perch.NewRunID()[4:],
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   rec.Model,
			"choices": []any{map[string]any{
				"index":         0,
				"message":       perchMessage(result),
				"finish_reason": perch.FinishReason(result.Done.FinishReason, len(result.Calls) > 0),
			}},
		}
		if u := perch.OpenAIUsage(result.Usage); u != nil {
			out["usage"] = u
		}
		raw, _ := json.Marshal(out)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		res.kind, res.status, res.result, res.terminal, res.category = outDelivered, http.StatusOK, "success", "success", ""
		res.trackUsage(result.Usage)
		return res
	}

	// Streaming: headers go out on the first frame so the client sees bytes as
	// soon as Perch produces them.
	var wrote atomic.Bool
	writeChunk := func(v any) {
		if !wrote.Swap(true) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			lease.MarkSent()
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return
		}
		_, _ = w.Write([]byte("data: "))
		_, _ = w.Write(raw)
		_, _ = w.Write([]byte("\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		firstEvent.Store(true)
	}

	id := "chatcmpl-" + perch.NewRunID()[4:]
	created := time.Now().Unix()
	chunk := func(delta map[string]any, finish any) map[string]any {
		return map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": rec.Model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
	}

	var textLen atomic.Int64
	writeChunk(chunk(map[string]any{"role": "assistant", "content": ""}, nil))

	var lastCalls []perch.RawToolCall
	onStreamEvent := func(ev perch.Event) error {
		switch ev.Type {
		case "answer_delta":
			if ev.Text == "" {
				return nil
			}
			textLen.Add(int64(len(ev.Text)))
			writeChunk(chunk(map[string]any{"content": ev.Text}, nil))
		case "reasoning_delta":
			if ev.Text == "" || !cfg.Perch.ExposeReasoning {
				return nil
			}
			writeChunk(chunk(map[string]any{"reasoning_content": ev.Text}, nil))
		case "tool_call_delta", "tool_use_end":
			if len(ev.ToolCalls) > 0 {
				lastCalls = ev.ToolCalls
			}
		case "stream_restart":
			// the upstream failed over mid-turn: everything sent so far is
			// void, so the accumulation restarts (no fake deltas go out)
			textLen.Store(0)
			lastCalls = nil
		}
		return nil
	}

	result, err := s.perchClient().CallModel(ctx, call, timeout, onStreamEvent)
	if err != nil {
		if wrote.Load() {
			// bytes already went downstream: report the failure in-band and end
			writeChunk(map[string]any{"error": map[string]any{
				"type": "api_error", "message": err.Error(), "code": perch.CodeOf(err),
			}})
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			res.kind, res.status, res.result, res.terminal, res.category = outDelivered, http.StatusOK, "failed", "upstream", "upstream"
			return res
		}
		return s.perchFailure(res, acct, err, rec)
	}

	if calls := result.Calls; len(calls) == 0 {
		calls = lastCalls
	} else {
		lastCalls = calls
	}
	if oc := perch.OpenAIToolCalls(lastCalls); len(oc) > 0 {
		writeChunk(chunk(map[string]any{"tool_calls": oc}, nil))
	}
	writeChunk(chunk(map[string]any{}, perch.FinishReason(result.Done.FinishReason, len(lastCalls) > 0)))
	if u := perch.OpenAIUsage(result.Usage); u != nil {
		writeChunk(map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": rec.Model,
			"choices": []any{}, "usage": u,
		})
	}
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	res.kind, res.status, res.result, res.terminal, res.category = outDelivered, http.StatusOK, "success", "success", ""
	res.trackUsage(result.Usage)
	return res
}

// perchMessage builds the non-streaming assistant message.
func perchMessage(r perch.CallResult) map[string]any {
	msg := map[string]any{"role": "assistant", "content": r.Text}
	if r.Text == "" {
		msg["content"] = nil
	}
	if calls := perch.OpenAIToolCalls(r.Calls); len(calls) > 0 {
		out := make([]map[string]any, 0, len(calls))
		for _, c := range calls {
			delete(c, "index")
			out = append(out, c)
		}
		msg["tool_calls"] = out
	}
	return msg
}

// trackUsage records the token counts for the account's health view.
func (r *attemptResult) trackUsage(u *perch.Usage) {
	if u == nil {
		return
	}
	in, out := u.InputTokens, u.OutputTokens
	r.usage.in, r.usage.out = &in, &out
}

// perchFailure classifies a Perch error for the pool: an expired session is an
// account-scoped 401 (quarantined and retried on another account), a plan
// refusal keeps Perch's own status so the matching error rule applies, and a
// transport failure is a plain network error.
func (s *Server) perchFailure(res *attemptResult, acct config.Account, err error, rec *requestLog) *attemptResult {
	var me *perch.ModelError
	if errors.As(err, &me) {
		if me.Auth {
			// the access token was refused: renew once and let the retry loop
			// try again — a rotated refresh token is the only lasting failure
			if rr := s.perch.keeper.Refresh(context.Background(), acct.ID, true); rr.NeedLogin {
				s.markPerchNeedLogin(acct.ID, rr.Err)
			}
			res.kind, res.status, res.terminal, res.category = outFailed, http.StatusUnauthorized, "upstream_http", "upstream"
			res.body = []byte(me.Error())
			res.accountScoped = true
			return res
		}
		res.kind, res.status, res.terminal, res.category = outFailed, me.Status, "upstream_http", "upstream"
		// The body has to read as Perch's own JSON so the error rules can match
		// on errorCode (perch_surface_required / starter_model_blocked /
		// turn_rate_limited), and so the console's reason is readable.
		payload := map[string]any{"ok": false, "error": me.Err, "errorCode": me.Code}
		if me.Limit != nil {
			payload["limit"] = me.Limit
		}
		raw, _ := json.Marshal(payload)
		res.body = raw
		res.contentType = "application/json"
		return res
	}
	if errors.Is(err, context.Canceled) {
		res.kind, res.status, res.terminal, res.category = outCancelled, 499, "client_cancelled", "network"
		return res
	}
	res.kind, res.status, res.terminal, res.category = outFailed, http.StatusBadGateway, "network", "network"
	res.body = []byte(err.Error())
	return res
}
