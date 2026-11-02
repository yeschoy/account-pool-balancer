// Package perchtelemetry reports what the Perch CLI would report, and by
// default reports it only to the local audit log.
//
// The switch is deliberate. The analysis of perchai-cli 2.4.121 found the CLI
// carrying a PostHog key that is empty (so the shipped client sends nothing)
// and a conversation record the pool has never sent. Turning either on for real
// adds traffic to a service the pool is meant to look ordinary to, so the pool
// ships with Egress=false: every report is built exactly as the CLI builds it,
// then written, redacted, to the event log where an operator can see what would
// have been sent. Setting Egress makes the same call for real, with the CLI's
// own opt-outs and budgets.
package perchtelemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/perch"
)

// Settings is what the reporter needs from the console configuration.
type Settings struct {
	// Egress sends for real. False means audit only.
	Egress bool
	// PostHogKey is the capture api_key. Empty disables PostHog, which is
	// exactly the state of the published client.
	PostHogKey  string
	PostHogHost string
	// OptedOut reflects the process environment (CI, PERCH_NO_TELEMETRY,
	// DO_NOT_TRACK). It only governs PostHog, as it does in the CLI.
	OptedOut bool
	// Platform is the platform property reported with events.
	Platform string
	// Version is the CLI version reported with events.
	Version string
}

// Repo is where the reporter records what it did.
type Repo interface {
	// Audit records one report: what it was, where it would go, and whether
	// it left the process.
	Audit(provider, accountID, kind, detail string)
}

// Reporter implements perch.Sink.
type Reporter struct {
	mu          sync.RWMutex
	set         Settings
	logf        func(format string, args ...any)
	repo        Repo
	http        *http.Client
	proxyClient *perch.Client
	// egressCtx identifies one enabled period; disabling cancels admitted work.
	egressCtx    context.Context
	egressCancel context.CancelFunc
	// posthogCtx also closes when PostHog is opted out.
	posthogCtx    context.Context
	posthogCancel context.CancelFunc
	// now and sleep are injectable so the retry budget can be tested without
	// waiting out the backoff.
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// New builds a reporter. rep may be nil.
func New(set Settings, rep Repo, logf func(format string, args ...any)) *Reporter {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	r := &Reporter{set: set, repo: rep, logf: logf, now: time.Now,
		http:        &http.Client{Timeout: perch.TurnTimeoutFinal + 5*time.Second},
		proxyClient: perch.NewClient(nil),
		sleep:       sleepCtx}
	if set.Egress {
		r.egressCtx, r.egressCancel = context.WithCancel(context.Background())
	}
	if posthogEnabled(set) {
		r.posthogCtx, r.posthogCancel = context.WithCancel(context.Background())
	}
	return r
}

// SetSettings swaps the settings (the console edits them live). Disabling egress
// cancels admitted work, but requests already in the transport may have sent data.
func (r *Reporter) SetSettings(set Settings) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.set.Egress && !set.Egress {
		r.egressCancel()
		r.egressCtx, r.egressCancel = nil, nil
	} else if !r.set.Egress && set.Egress {
		r.egressCtx, r.egressCancel = context.WithCancel(context.Background())
	}
	wasPostHogAllowed := posthogEnabled(r.set)
	postHogAllowed := posthogEnabled(set)
	changedDestination := r.set.PostHogKey != set.PostHogKey || r.set.PostHogHost != set.PostHogHost
	if wasPostHogAllowed && (!postHogAllowed || changedDestination) {
		r.posthogCancel()
		r.posthogCtx, r.posthogCancel = nil, nil
	}
	if postHogAllowed && (!wasPostHogAllowed || changedDestination) {
		r.posthogCtx, r.posthogCancel = context.WithCancel(context.Background())
	}
	r.set = set
}

func posthogEnabled(set Settings) bool {
	return set.Egress && !set.OptedOut && strings.TrimSpace(set.PostHogKey) != ""
}

// Settings returns the current settings.
func (r *Reporter) Settings() Settings {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.set
}

// SetHTTPClient replaces the transport (tests point it at a local server).
func (r *Reporter) SetHTTPClient(hc *http.Client) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if hc != nil {
		r.http = hc
	}
}

// SetClock replaces the clock and the waiter (tests).
func (r *Reporter) SetClock(now func() time.Time, sleep func(context.Context, time.Duration) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now != nil {
		r.now = now
	}
	if sleep != nil {
		r.sleep = sleep
	}
}

// PostHog builds one anonymous event and either sends it or audits it.
//
// The CLI swallows every error from this call and never retries, so neither do
// we: a failed event is a dropped event.
func (r *Reporter) PostHog(ctx context.Context, ev perch.PostHogEvent) error {
	return r.PostHogWithProxy(ctx, ev, "")
}

// PostHogWithProxy reports an event using the specified proxy when non-empty.
func (r *Reporter) PostHogWithProxy(ctx context.Context, ev perch.PostHogEvent, proxyURL string) error {
	r.mu.RLock()
	set, hc, epoch := r.set, r.http, r.posthogCtx
	r.mu.RUnlock()
	if strings.TrimSpace(set.PostHogKey) == "" {
		return nil // no key: the CLI's own short-circuit
	}
	if ev.APIKey != set.PostHogKey {
		r.audit("", "", "posthog", fmt.Sprintf("event=%s result=stale_config", auditEvent(ev.Event)))
		return nil
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if !set.Egress {
		r.audit("", "", "posthog", fmt.Sprintf("event=%s bytes=%d result=not_sent (未发送)", auditEvent(ev.Event), len(body)))
		return nil
	}
	if set.OptedOut {
		r.audit("", "", "posthog", fmt.Sprintf("event=%s result=opt_out (未发送)", auditEvent(ev.Event)))
		return nil
	}
	if proxyURL != "" {
		hc, err = r.clientForProxy(proxyURL)
		if err != nil {
			r.logf("telemetry: posthog event %s dropped: proxy_error", auditEvent(ev.Event))
			r.audit("", "", "posthog", fmt.Sprintf("event=%s result=proxy_error", auditEvent(ev.Event)))
			return nil
		}
	}
	if !r.posthogAllowed(epoch) {
		r.audit("", "", "posthog", fmt.Sprintf("event=%s result=not_sent (未发送)", auditEvent(ev.Event)))
		return nil
	}
	ctx, stop := egressContext(ctx, epoch)
	defer stop()
	actx, cancel := context.WithTimeout(ctx, perch.PostHogTimeout)
	defer cancel()
	code, _, err := postJSON(actx, hc, perch.PostHogURL(set.PostHogHost), body, nil)
	if err != nil {
		r.logf("telemetry: posthog event %s dropped: network_error", auditEvent(ev.Event))
		r.audit("", "", "posthog", fmt.Sprintf("event=%s result=network_error", auditEvent(ev.Event)))
		return nil
	}
	r.audit("", "", "posthog", fmt.Sprintf("event=%s http_status=%d", auditEvent(ev.Event), code))
	return nil
}

// CLITurn reports one conversation record with the CLI's budget: one attempt
// for a streaming or run-less status, three for a final status with a runId,
// 45 s (12 s streaming) each, and a 1 s doubling backoff between them.
//
// The CLI retries a 429 and any 5xx, stops on other statuses, and refreshes a
// 401 at most once — refusing a refreshed token whose JWT `sub` differs from
// the original. That last rule is why the caller supplies the token: the
// reporter will not silently swap in another account's session.
func (r *Reporter) CLITurn(ctx context.Context, endpoint, token string, turn perch.CLITurn) error {
	return r.CLITurnWithProxy(ctx, endpoint, token, turn, "")
}

// CLITurnWithProxy reports a turn using the specified proxy when non-empty.
func (r *Reporter) CLITurnWithProxy(ctx context.Context, endpoint, token string, turn perch.CLITurn, proxyURL string) error {
	r.mu.RLock()
	set, hc, sleep, epoch := r.set, r.http, r.sleep, r.egressCtx
	r.mu.RUnlock()
	body, err := json.Marshal(turn)
	if err != nil {
		return err
	}
	if !set.Egress {
		r.audit("", "", "cli-turn", fmt.Sprintf("status=%s bytes=%d events=%d attachments=%d result=not_sent (未发送)",
			perch.RedactTurn(turn).Status, len(body), len(turn.Events), len(turn.Attachments)))
		return nil
	}
	if proxyURL != "" {
		hc, err = r.clientForProxy(proxyURL)
		if err != nil {
			r.audit("", "", "cli-turn", fmt.Sprintf("status=%s result=proxy_error", perch.RedactTurn(turn).Status))
			return err
		}
	}
	ctx, stop := egressContext(ctx, epoch)
	defer stop()
	attempts := perch.TurnAttempts(turn.Status, turn.RunID)
	timeout := perch.TurnTimeout(turn.Status)
	headers := map[string]string{"Authorization": "Bearer " + token}
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			if err := sleep(ctx, perch.TurnBackoff(i)); err != nil {
				if !r.egressAllowed(epoch) {
					r.audit("", "", "cli-turn", fmt.Sprintf("status=%s result=not_sent (未发送)", perch.RedactTurn(turn).Status))
					return lastErr
				}
				return err
			}
		}
		if !r.egressAllowed(epoch) {
			r.audit("", "", "cli-turn", fmt.Sprintf("status=%s result=not_sent (未发送)", perch.RedactTurn(turn).Status))
			return lastErr
		}
		actx, cancel := context.WithTimeout(ctx, timeout)
		code, _, err := postJSON(actx, hc, endpoint, body, headers)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if code >= 200 && code < 300 {
			r.audit("", "", "cli-turn", fmt.Sprintf("status=%s attempt=%d http_status=%d", perch.RedactTurn(turn).Status, i+1, code))
			return nil
		}
		lastErr = fmt.Errorf("HTTP %d", code)
		retryable := code >= 500 || code == http.StatusTooManyRequests
		if !retryable {
			r.logf("telemetry: cli-turn refused: status %d", code)
			break
		}
	}
	r.audit("", "", "cli-turn", fmt.Sprintf("status=%s result=failed", perch.RedactTurn(turn).Status))
	return lastErr
}

// egressAllowed admits work only for the currently enabled period. A request
// already admitted may race with disable, so its context is canceled as well.
func (r *Reporter) egressAllowed(epoch context.Context) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return epoch != nil && r.set.Egress && r.egressCtx == epoch
}

func (r *Reporter) posthogAllowed(epoch context.Context) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return epoch != nil && posthogEnabled(r.set) && r.posthogCtx == epoch
}

func egressContext(ctx, epoch context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(epoch, cancel)
	if epoch.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}

func (r *Reporter) audit(provider, accountID, kind, detail string) {
	if r.repo != nil {
		r.repo.Audit(provider, accountID, kind, detail)
	}
}

func postJSON(ctx context.Context, hc *http.Client, url string, body []byte, headers map[string]string) (int, []byte, error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	buf := make([]byte, 0, 512)
	tmp := make([]byte, 512)
	for len(buf) < 1<<20 {
		n, rerr := res.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	return res.StatusCode, buf, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func auditEvent(event string) string {
	switch event {
	case perch.EventFirstRun, perch.EventSessionStarted, perch.EventMessageSent, perch.EventFirstMessage, perch.EventUpgradePrompt:
		return event
	default:
		return "unknown"
	}
}
