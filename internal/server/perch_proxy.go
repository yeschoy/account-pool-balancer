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

// perchConversationFingerprint resolves which conversation a request belongs to,
// so the pool can give it a stable run id.
//
// This is the bridge the delta cache needs: OpenAI chat requests, Responses
// requests and every agent CLI name their conversation differently — a session
// header, a prompt_cache_key, a previous_response_id, or (failing those) only
// their first messages. identity() already normalises all of those into one
// HMAC fingerprint, so chat and Responses requests from the same agent resolve
// to the SAME fingerprint and therefore the same run id. That is what lets a
// delta fire across protocols instead of only within one.
//
// An empty result means the request names no conversation; the caller then
// falls back to the ticket's run id.
func (s *Server) perchConversationFingerprint(cfg *config.Config, acct config.Account, r *http.Request, body map[string]any) string {
	// The group keeps one pool's fingerprints separate from another's, matching
	// how proxy.go scopes session affinity.
	id := s.identity(cfg, acct.Group, r.Header, body)
	if id.Fingerprint == "" {
		return ""
	}
	// Only an EXPLICIT conversation may own a run id.
	//
	// identity() falls back to an HMAC of the first messages when no header or
	// body field names the conversation. That is the right default for
	// account affinity, but it is wrong for a run id: two unrelated chats that
	// happen to open with the same greeting would share one run, and the delta
	// layer would then claim a prefix the server holds for the other chat.
	// Without an explicit conversation the caller keeps the ticket's run id.
	if id.Confidence != "explicit" {
		return ""
	}
	return id.Fingerprint
}

// perchAttribution builds Perch's own attribution block for one account.
//
// The published client (o0) sends this block only when BOTH userId and
// workspaceId are present; with either missing it sends `null`. That matters
// for more than tidiness: {userId} alone is a shape the real client never
// produces, so a pool that sends it is identifiable by that alone. The
// workspace id comes from the account's last quota snapshot, which is where the
// quota poller records what `/api/v1/workspaces/current` answered.
func (s *Server) perchAttribution(a config.Account) map[string]any {
	userID := strings.TrimSpace(a.PerchUserID)
	if userID == "" {
		return nil
	}
	var workspaceID string
	if q := s.quota.All()[a.ID]; q != nil {
		workspaceID = strings.TrimSpace(q.WorkspaceID)
	}
	if workspaceID == "" {
		// o0 returns null rather than a partial block.
		return nil
	}
	return map[string]any{
		"userId":      userID,
		"workspaceId": workspaceID,
		"source":      "cli",
	}
}

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
	opt.Attribution = s.perchAttribution(a)
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
	p, src := perch.ResolveGatePrefix(s.cfg.Get().Perch.GatePrefix)
	s.perchPrefix, s.perchPrefixSource, s.perchPrefixKnown = p, src, true
	return p
}

// attemptPerch serves one attempt for a Perch account.
func (s *Server) attemptPerch(w http.ResponseWriter, r *http.Request, cfg *config.Config, lease *pool.Lease, body []byte, proto string, rec *requestLog, ctx context.Context, res *attemptResult, timedOut *atomic.Bool) *attemptResult {
	acct := lease.Account
	surface := perch.SurfaceFor(cfg.Perch.Surface)
	if surface != perch.SurfaceWeb {
		// The CLI surface is refused unless the first system turn opens with
		// Perch's own preamble. Without it every call is a 403, so failing
		// here with a clear message beats forwarding and reporting that
		// forever.
		if err := perch.CheckGatePrefix(s.perchGatePrefix()); err != nil {
			res.kind, res.status, res.terminal, res.category = outFailed, http.StatusServiceUnavailable, "perch_gate", "config"
			res.body = []byte(err.Error())
			return res
		}
	}

	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusBadRequest, "request", "request"
		res.body = []byte("request body must be a JSON object")
		return res
	}
	var payload []byte
	if surface == perch.SurfaceWeb {
		// The web lane accepts one user message: no system channel, no tools.
		// The caller's system turns and history are folded into that message
		// (see perch.BuildWebMessage), and a request that cannot be expressed
		// there is reported to the caller rather than silently degraded.
		msg, ok := perch.BuildWebMessage(obj, "")
		if !ok {
			res.kind, res.status, res.terminal, res.category = outFailed, http.StatusBadRequest, "request", "request"
			res.body = []byte(`{"ok":false,"error":"the request has no user message; Perch's web lane needs one","errorCode":"request"}`)
			res.contentType = "application/json"
			return res
		}
		msg = msg.WithModelOption(perch.WebOptionID(rec.Model))
		// The web lane has no tool channel: a declaration is dropped rather
		// than sent to a field the endpoint ignores, and the caller's request
		// is answered as a plain turn.
		var err error
		if payload, err = json.Marshal(msg); err != nil {
			res.kind, res.status, res.terminal, res.category = outFailed, http.StatusInternalServerError, "request", "request"
			res.body = []byte("cannot build the Perch request")
			return res
		}
	}
	// The CLI envelope is built after the ticket is known: a ticketed turn must
	// carry the ticket's own runId (the published client takes its runId from
	// the ticket response), and a fresh random one would be a pairing the real
	// client never makes.

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
		s.onPerchRefresh(acct.ID, rr)
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusUnauthorized, "upstream_http", "upstream"
		res.body = []byte(`{"ok":false,"error":"this account's Perch session has expired and needs signing in again","errorCode":"perch_session_invalid"}`)
		res.contentType = "application/json"
		res.accountScoped = true
		return res
	}
	tok, ok := s.perch.keeper.Get(acct.ID)
	res.turnToken, res.turnAccount = tok.AccessToken, acct
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
	var terminalBanned bool
	onEvent := func(ev perch.Event) error {
		if ev.Type == "done" && ev.OK != nil && !*ev.OK && strings.EqualFold(ev.ErrorCode, "banned") {
			terminalBanned = true
		}
		if timedOut.Load() {
			return errors.New("the request timed out")
		}
		return nil
	}

	webBase := cfg.Upstream.PerchWebBase
	if webBase == "" {
		webBase = perch.ChatURL
	}
	ua, locale := s.perchCallIdentity(cfg)

	// The turn ticket, before the model call — the order the published CLI
	// uses (issue, then call with x-perch-turn-ticket). Only the CLI surface
	// uses one; the web lane neither needs nor sends it.
	var ticket *perch.Ticket
	if perch.SurfaceFor(surface) != perch.SurfaceWeb {
		if cfg.Perch.TurnTicketOn() {
			tk, terr := s.perchClient().TicketFor(ctx, perch.TicketRequest{
				Key:       acct.ID,
				Base:      base,
				Token:     tok.AccessToken,
				Profile:   cfg.Perch.TicketProfile,
				ProxyURL:  acct.ProxyURL,
				UserAgent: ua,
				Locale:    locale,
			})
			if terr != nil {
				// An enforced ticket failure stops the turn before any model
				// call, which is what the CLI does. It is never a reason to
				// switch accounts and replay: that is precisely the allowance
				// circumvention this gate exists to stop.
				return s.perchTicketFailure(res, acct, terr)
			}
			ticket = tk
		}
	}

	if perch.SurfaceFor(surface) != perch.SurfaceWeb {
		opt := s.perchRequestOpts(cfg, acct)
		opt.Model = rec.Model
		// The CLI names a thread on its own envelope, and the pool fills the
		// caller's conversation when it has one, else the CLI's cwd thread.
		opt.ThreadID = s.perchThreadID(cfg, "")
		opt.ChatMode = cfg.Perch.Lane
		// The run id follows the CONVERSATION, not the ticket.
		//
		// The delta cache is keyed by endpoint+runId, and a downstream request
		// is a separate HTTP transaction that re-sends the whole conversation.
		// Deriving the run id from the caller's conversation fingerprint is what
		// lets two requests of one chat hit the same cache entry and send only
		// the new tail. A per-ticket run id would make every request a new run,
		// which is exactly the case where a delta can never fire.
		if id := s.perchConversationFingerprint(cfg, acct, r, obj); id != "" {
			opt.RunID = s.perchClient().RunFor(acct.ID, id)
		} else if ticket != nil && ticket.RunID != "" {
			// No conversation to continue: keep the ticket's run, as before.
			opt.RunID = ticket.RunID
		}
		var err error
		if payload, err = json.Marshal(perch.BuildEnvelope(obj, opt)); err != nil {
			res.kind, res.status, res.terminal, res.category = outFailed, http.StatusInternalServerError, "request", "request"
			res.body = []byte("cannot build the Perch request")
			return res
		}
	}

	// What this turn is, for the cli-turn record: the reporter reads it after
	// the attempt settles (prompt and answer are added there, then redacted).
	// It names the ticket's run id, because the model calls of this turn did.
	turnRunID := perch.NewRunID()
	if ticket != nil && ticket.RunID != "" {
		turnRunID = ticket.RunID
	}
	res.turn = perchTurn{
		Thread: s.perchThreadID(cfg, ""),
		Mode:   cfg.Perch.Lane,
		RunID:  turnRunID,
		Prompt: perch.PromptOf(obj),
	}

	// One use of the account's ticket, counted the way the published client
	// counts it: per model-call, including a retry. The count is advisory — the
	// server decides whether a call is admitted — but without it the console
	// reports a ticket as untouched forever.
	if ticket != nil {
		if tk := s.perchClient().UseTicket(acct.ID); tk != "" {
			ticket.Token = tk
		}
	}

	call := perch.ModelCall{Base: base, WebBase: webBase, Token: tok.AccessToken, Body: payload,
		Surface: surface, ProxyURL: acct.ProxyURL, UserAgent: ua, Locale: locale, Ticket: ticket,
		AccountKey: acct.ID}
	diagnosticTarget := base
	if perch.SurfaceFor(surface) == perch.SurfaceWeb {
		diagnosticTarget = webBase
	}
	diagnosticProxy := "unconfigured_unknown"
	if acct.ProxyURL != "" {
		diagnosticProxy = "account_proxy"
	}
	var timeout time.Duration
	if stream {
		timeout = time.Duration(cfg.Routing.RequestSec) * time.Second
	} else {
		timeout = time.Duration(cfg.Routing.RequestSec) * time.Second
	}

	res.turnStarted = true
	if !stream {
		// A non-streaming request is assembled from the same events.
		started := time.Now()
		result, err := s.perchClient().CallModel(ctx, call, timeout, func(ev perch.Event) error {
			firstEvent.Store(true)
			return onEvent(ev)
		})
		s.logPerchDiagnostic(acct.ID, "model", diagnosticProxy, diagnosticTarget, started, err)
		if err != nil {
			if terminalBanned {
				err = &perch.ModelError{Status: http.StatusForbidden, Code: "banned", Err: err.Error()}
			}
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
		res.turn.Answer = result.Text
		res.trackUsage(result.Usage)
		return res
	}

	// Streaming. Nothing is written downstream until either
	//   - Perch has produced an event worth relaying (a non-empty answer or
	//     reasoning delta), so an upstream 401/429/5xx before that point can
	//     still be answered with its real HTTP status and switched to another
	//     account by the surrounding retry loop; or
	//   - the call finished, in which case the whole answer is written at once.
	// The role frame is held back with it: writing it eagerly used to commit a
	// 200 for a turn that had not received a single usable frame.
	var wrote atomic.Bool
	writeChunk := func(v any) {
		// A streamFrame may carry the empty assistant frame plus the first real
		// frame; unwrap it here so it is never marshalled as a bare object.
		role, isRole := v.(streamFrame)
		if isRole {
			v = role.payload
		}
		if !wrote.Swap(true) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			lease.MarkSent()
		}
		writeFrame(w, v)
		if isRole && role.open && role.next != nil {
			writeFrame(w, role.next)
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
	var answer strings.Builder

	var lastCalls []perch.RawToolCall
	onStreamEvent := func(ev perch.Event) error {
		if err := onEvent(ev); err != nil {
			return err
		}
		if res.ttft == nil && ((ev.Type == "answer_delta" || ev.Type == "reasoning_delta") && ev.Text != "" ||
			(ev.Type == "tool_call_delta" || ev.Type == "tool_use_end") && len(ev.ToolCalls) > 0) {
			ttft := time.Since(rec.TS).Milliseconds()
			res.ttft = &ttft
		}
		switch ev.Type {
		case "answer_delta":
			if ev.Text == "" {
				return nil
			}
			textLen.Add(int64(len(ev.Text)))
			if answer.Len() < turnAnswerCap {
				answer.WriteString(ev.Text)
			}
			writeChunk(streamFrame{open: true, payload: chunk(map[string]any{"role": "assistant", "content": ""}, nil),
				next: chunk(map[string]any{"content": ev.Text}, nil)})
		case "reasoning_delta":
			if ev.Text == "" || !cfg.Perch.ExposeReasoning {
				return nil
			}
			writeChunk(streamFrame{open: true, payload: chunk(map[string]any{"role": "assistant", "content": ""}, nil),
				next: chunk(map[string]any{"reasoning_content": ev.Text}, nil)})
		case "tool_call_delta", "tool_use_end":
			if len(ev.ToolCalls) > 0 {
				lastCalls = ev.ToolCalls
			}
		case "stream_restart":
			// the upstream failed over mid-turn: everything sent so far is
			// void, so the accumulation restarts (no fake deltas go out)
			textLen.Store(0)
			answer.Reset()
			lastCalls = nil
		}
		return nil
	}

	started := time.Now()
	result, err := s.perchClient().CallModel(ctx, call, timeout, onStreamEvent)
	s.logPerchDiagnostic(acct.ID, "model", diagnosticProxy, diagnosticTarget, started, err)
	if err != nil {
		if terminalBanned {
			err = &perch.ModelError{Status: http.StatusForbidden, Code: "banned", Err: err.Error()}
		}
		if wrote.Load() {
			// A real frame already went downstream: the status line is spent,
			// so the failure has to be reported in-band and the stream closed.
			failure := s.perchFailure(&attemptResult{}, acct, err, rec)
			if failure.terminal == "upstream_http" || failure.terminal == "upstream_envelope" {
				s.evaluate(cfg, acct, rec.Model, failure, rec.RequestID, len(rec.Attempts), rec.ClientKeyID, rec.ClientKeyName)
				res.evaluated = true
			}
			writeFrame(w, map[string]any{"error": map[string]any{
				"type": "api_error", "message": err.Error(), "code": perch.CodeOf(err),
			}})
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			res.kind, res.status, res.result, res.terminal, res.category = outDelivered, http.StatusOK, "failed", "upstream", "upstream"
			return res
		}
		// Nothing went downstream: a real HTTP status still applies.
		return s.perchFailure(res, acct, err, rec)
	}

	if calls := result.Calls; len(calls) == 0 {
		calls = lastCalls
	} else {
		lastCalls = calls
	}
	role := streamFrame{open: true, payload: chunk(map[string]any{"role": "assistant", "content": ""}, nil)}
	if oc := perch.OpenAIToolCalls(lastCalls); len(oc) > 0 {
		role.next = chunk(map[string]any{"tool_calls": oc}, nil)
		writeChunk(role)
	} else {
		writeChunk(role)
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
	res.turn.Answer = answer.String()
	res.trackUsage(result.Usage)
	return res
}

// streamFrame carries the empty assistant frame that opens an OpenAI stream
// together with the first real frame, so the two are written as one commit.
type streamFrame struct {
	open    bool
	payload map[string]any
	next    map[string]any
}

// writeFrame writes one `data:` SSE frame and flushes it.
func writeFrame(w http.ResponseWriter, v any) {
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
}

// turnAnswerCap bounds how much of a streamed answer the cli-turn record keeps.
// The record carries a digest and a length, never the text, so this only needs
// to be long enough for the digest to describe the same answer twice.
const turnAnswerCap = 64 << 10

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
	if !u.DecodedFromJSON || u.InputTokensPresent {
		in := u.InputTokens
		r.usage.in = &in
	}
	if !u.DecodedFromJSON || u.OutputTokensPresent {
		out := u.OutputTokens
		r.usage.out = &out
	}
	if u.CacheReadInputTokensPresent || u.CacheReadInputTokens != 0 {
		cached := u.CacheReadInputTokens
		r.usage.cached = &cached
	}
}

// perchFailure classifies a Perch error for the pool: an expired session is an
// account-scoped 401 (quarantined and retried on another account), a plan
// refusal keeps Perch's own status so the matching error rule applies, and a
// transport failure is a plain network error.
func (s *Server) perchFailure(res *attemptResult, acct config.Account, err error, rec *requestLog) *attemptResult {
	var me *perch.ModelError
	if errors.As(err, &me) {
		if me.Code == perch.TicketCodeUnavailable {
			// The server refused the ticket itself, not the credential. Drop it
			// so the next request issues a fresh one instead of re-presenting a
			// ticket the upstream has already disowned.
			s.perchClient().DropTicket(acct.ID)
		}
		if me.Auth && !strings.EqualFold(me.Code, "banned") {
			// the access token was refused: renew once and let the retry loop
			// try again — a rotated refresh token is the only lasting failure
			if rr := s.perch.keeper.Refresh(context.Background(), acct.ID, true); rr.NeedLogin {
				s.onPerchRefresh(acct.ID, rr)
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

// perchTicketFailure answers a refused turn ticket.
//
// Both verdicts are terminal for the turn. A turn-rate limit is the account's
// plan talking, and an unavailable enforced ticket means Perch will not start a
// turn at all; neither gets better by replaying the same request somewhere
// else, which is what the surrounding retry loop would otherwise do. The body
// is Perch's own JSON shape so the console and the error rules see a real
// errorCode rather than a Gateway-specific one.
func (s *Server) perchTicketFailure(res *attemptResult, acct config.Account, err error) *attemptResult {
	var te *perch.TicketError
	if !errors.As(err, &te) {
		res.kind, res.status, res.terminal, res.category = outFailed, http.StatusBadGateway, "perch_ticket", "upstream"
		res.body = []byte(err.Error())
		return res
	}
	status := te.Status
	if status <= 0 {
		status = http.StatusBadGateway
	}
	if te.IsRateLimited() {
		status = http.StatusTooManyRequests
	}
	payload := map[string]any{"ok": false, "error": te.Error(), "errorCode": te.Code}
	raw, _ := json.Marshal(payload)
	res.kind, res.status, res.terminal, res.category = outFailed, status, "upstream_http", "upstream"
	res.body = raw
	res.contentType = "application/json"
	if te.RetryAfter > 0 {
		res.retryAfter = te.RetryAfter
	}
	// A rate-limit verdict is about the account's allowance, so it is recorded
	// against that account only. It must not be treated as a reason to rotate.
	res.accountScoped = te.IsRateLimited()
	return res
}
