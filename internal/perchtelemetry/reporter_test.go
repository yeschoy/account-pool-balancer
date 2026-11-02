package perchtelemetry_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"account-pool-balancer/internal/perch"
	pt "account-pool-balancer/internal/perchtelemetry"
)

// Contract tests for the two reporters. They mirror analysis.md: the PostHog
// body shape and its opt-outs, the cli-turn retry budget, and the fact that a
// redacted record carries no conversation text.

func TestBuildPostHogEventSetsLibLast(t *testing.T) {
	ev := perch.BuildPostHogEvent("key", perch.EventMessageSent, "machine-1", map[string]any{"$lib": "spoofed", "surface": "cli"})
	if ev.Properties["$lib"] != perch.PostHogLib {
		t.Fatalf("$lib = %v, want %q", ev.Properties["$lib"], perch.PostHogLib)
	}
	if ev.Properties["surface"] != "cli" {
		t.Fatal("caller properties were dropped")
	}
	if ev.Event != "message_sent" || ev.DistinctID != "machine-1" || ev.APIKey != "key" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestPostHogURLAndOptOut(t *testing.T) {
	if got := perch.PostHogURL(""); got != "https://us.i.posthog.com/i/v0/e/" {
		t.Fatalf("default URL = %q", got)
	}
	if got := perch.PostHogURL("http://127.0.0.1:9/"); got != "http://127.0.0.1:9/i/v0/e/" {
		t.Fatalf("custom URL = %q", got)
	}
	cases := []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{}, false},
		{map[string]string{"CI": "1"}, true},
		{map[string]string{"DO_NOT_TRACK": "true"}, true},
		{map[string]string{"PERCH_NO_TELEMETRY": "yes"}, true},
		// Environment variables are strings: any non-empty value opts out.
		{map[string]string{"CI": "0", "DO_NOT_TRACK": "false"}, true},
		{map[string]string{"PERCH_NO_TELEMETRY": " "}, true},
	}
	for _, c := range cases {
		if got := perch.OptedOut(func(k string) string { return c.env[k] }); got != c.want {
			t.Errorf("perch.OptedOut(%v) = %v, want %v", c.env, got, c.want)
		}
	}
	if perch.OptedOut(nil) {
		t.Fatal("a nil getenv must not opt out")
	}
}

func TestTurnBudgetMatchesTheCLI(t *testing.T) {
	if got := perch.TurnAttempts(perch.TurnStatusStreaming, "run_1"); got != 1 {
		t.Fatalf("streaming attempts = %d, want 1", got)
	}
	if got := perch.TurnAttempts(perch.TurnStatusComplete, ""); got != 1 {
		t.Fatalf("run-less attempts = %d, want 1", got)
	}
	if got := perch.TurnAttempts(perch.TurnStatusComplete, "run_1"); got != 3 {
		t.Fatalf("final attempts = %d, want 3", got)
	}
	if perch.TurnTimeout(perch.TurnStatusStreaming) != 12*time.Second || perch.TurnTimeout(perch.TurnStatusComplete) != 45*time.Second {
		t.Fatal("per-attempt timeouts drifted from the CLI")
	}
	if perch.TurnBackoff(1) != time.Second || perch.TurnBackoff(2) != 2*time.Second || perch.TurnBackoff(3) != 4*time.Second {
		t.Fatal("backoff is not 1 s doubling")
	}
}

func TestCLITurnURL(t *testing.T) {
	if got := perch.CLITurnURL("https://app.perchai.app"); got != "https://app.perchai.app/api/perch-terminal/cli-turn" {
		t.Fatalf("URL = %q", got)
	}
	// a base that already names the endpoint is not doubled
	if got := perch.CLITurnURL("https://app.perchai.app/api/perch-terminal/cli-turn/"); !strings.HasSuffix(got, "cli-turn") || strings.Count(got, "cli-turn") != 1 {
		t.Fatalf("doubled URL = %q", got)
	}
	if perch.CLITurnURL("") != "" {
		t.Fatal("an empty app URL has no endpoint")
	}
}

func TestRedactTurnRemovesConversationText(t *testing.T) {
	errText := "upstream said no"
	in := perch.CLITurn{
		Prompt: "my secret prompt", AssistantText: "the answer", ThreadID: "cli-cwd-abc",
		RunID: "run_1", Status: perch.TurnStatusComplete, Error: &errText,
		Events: []any{map[string]any{"type": "answer_delta", "text": "leak"}},
	}
	out := perch.RedactTurn(in)
	blob, _ := json.Marshal(out)
	for _, secret := range []string{"my secret prompt", "the answer", "upstream said no", "leak"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("redacted record still contains %q: %s", secret, blob)
		}
	}
	if out.ThreadID != "" || out.RunID != "" || out.Status != perch.TurnStatusComplete {
		t.Fatalf("redaction retained identity or lost status: %+v", out)
	}
}

// fakeCLITurn answers the first n requests with the given statuses, then 200.
func fakeCLITurn(t *testing.T, statuses ...int) (*httptest.Server, *int) {
	t.Helper()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var turn perch.CLITurn
		if err := json.Unmarshal(body, &turn); err != nil {
			t.Errorf("body is not a cli-turn: %v", err)
		}
		calls++
		code := http.StatusOK
		if calls <= len(statuses) {
			code = statuses[calls-1]
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestReporterRetriesOnlyWhatTheCLIRetries(t *testing.T) {
	cases := []struct {
		name     string
		statuses []int
		want     int
	}{
		{"success first", nil, 1},
		{"5xx then success", []int{503}, 2},
		{"429 then success", []int{429}, 2},
		{"400 is not retried", []int{400}, 1},
		{"401 is not retried by the reporter", []int{401}, 1},
		{"three 5xx exhaust the budget", []int{500, 500, 500}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, calls := fakeCLITurn(t, c.statuses...)
			r := pt.New(pt.Settings{Egress: true}, nil, nil)
			r.SetClock(nil, func(ctx context.Context, d time.Duration) error { return nil })
			turn := perch.CLITurn{Status: perch.TurnStatusComplete, RunID: "run_1", ThreadID: "cli-cwd-abc"}
			_ = r.CLITurn(context.Background(), srv.URL, "tok", turn)
			if *calls != c.want {
				t.Fatalf("attempts = %d, want %d", *calls, c.want)
			}
		})
	}
}

func TestReporterAuditsWithoutEgress(t *testing.T) {
	srv, calls := fakeCLITurn(t)
	repo := &fakeRepo{}
	r := pt.New(pt.Settings{Egress: false}, repo, nil)
	turn := perch.CLITurn{Prompt: "secret", AssistantText: "answer", Status: perch.TurnStatusComplete, RunID: "run_1"}
	if err := r.CLITurn(context.Background(), srv.URL, "tok", turn); err != nil {
		t.Fatalf("audit-only reporting failed: %v", err)
	}
	if *calls != 0 {
		t.Fatalf("audit-only mode sent %d requests", *calls)
	}
	if len(repo.rows) != 1 {
		t.Fatalf("audited %d rows, want 1", len(repo.rows))
	}
	if strings.Contains(repo.rows[0], "secret") || strings.Contains(repo.rows[0], "answer") {
		t.Fatalf("audit row leaks the conversation: %q", repo.rows[0])
	}
	if !strings.Contains(repo.rows[0], "未发送") {
		t.Fatalf("audit row should say it was not sent: %q", repo.rows[0])
	}
}

func TestReporterPostHogNeedsAKeyAndHonoursOptOut(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts++
		var ev perch.PostHogEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("posthog body: %v", err)
		}
		if ev.Properties["$lib"] != perch.PostHogLib {
			t.Errorf("$lib = %v", ev.Properties["$lib"])
		}
	}))
	defer srv.Close()

	// no key: the CLI's own short-circuit, nothing is built or sent
	r := pt.New(pt.Settings{Egress: true, PostHogHost: srv.URL}, nil, nil)
	_ = r.PostHog(context.Background(), perch.BuildPostHogEvent("", perch.EventMessageSent, "m", nil))
	if posts != 0 {
		t.Fatalf("sent %d events with no key", posts)
	}

	// key but opted out: still nothing
	r = pt.New(pt.Settings{Egress: true, PostHogKey: "k", PostHogHost: srv.URL, OptedOut: true}, nil, nil)
	_ = r.PostHog(context.Background(), perch.BuildPostHogEvent("k", perch.EventMessageSent, "m", nil))
	if posts != 0 {
		t.Fatalf("sent %d events while opted out", posts)
	}

	// key, egress, not opted out: one event
	r = pt.New(pt.Settings{Egress: true, PostHogKey: "k", PostHogHost: srv.URL}, nil, nil)
	_ = r.PostHog(context.Background(), perch.BuildPostHogEvent("k", perch.EventMessageSent, "m", map[string]any{"surface": "cli"}))
	if posts != 1 {
		t.Fatalf("sent %d events, want 1", posts)
	}
}

func TestReporterConcurrentConfiguration(t *testing.T) {
	r := pt.New(pt.Settings{Egress: true, PostHogKey: "key"}, nil, nil)
	client := &http.Client{Transport: errorTransport{}}
	wait := func(context.Context, time.Duration) error { return nil }
	r.SetHTTPClient(client)
	r.SetClock(time.Now, wait)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			r.SetHTTPClient(client)
			r.SetClock(time.Now, wait)
			r.SetSettings(pt.Settings{Egress: true, PostHogKey: "key"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = r.PostHog(context.Background(), perch.PostHogEvent{APIKey: "key", Event: perch.EventMessageSent})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = r.CLITurn(context.Background(), "http://example.invalid", "token", perch.CLITurn{Status: perch.TurnStatusComplete, RunID: "run"})
		}
	}()
	wg.Wait()
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestDisableEgressStopsRetries(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls int
	r := pt.New(pt.Settings{Egress: true}, nil, nil)
	r.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	r.SetClock(nil, func(context.Context, time.Duration) error {
		close(started)
		<-release
		return nil
	})
	done := make(chan error, 1)
	go func() {
		done <- r.CLITurn(context.Background(), "http://example.invalid", "token", perch.CLITurn{Status: perch.TurnStatusComplete, RunID: "run"})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("retry wait did not start")
	}
	r.SetSettings(pt.Settings{Egress: false})
	r.SetSettings(pt.Settings{Egress: true})
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("report did not stop")
	}
	if calls != 1 {
		t.Fatalf("sent %d requests; old report retried after disable", calls)
	}
}

func TestDisableEgressCancelsInFlightRequest(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	r := pt.New(pt.Settings{Egress: true}, nil, nil)
	r.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		close(canceled)
		return nil, req.Context().Err()
	})})
	done := make(chan error, 1)
	go func() {
		done <- r.CLITurn(context.Background(), "http://example.invalid", "token", perch.CLITurn{Status: perch.TurnStatusStreaming})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	r.SetSettings(pt.Settings{})
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("request context was not canceled")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("report did not stop")
	}
}

type blockingJSON struct {
	started chan struct{}
	release chan struct{}
}

func (b blockingJSON) MarshalJSON() ([]byte, error) {
	close(b.started)
	<-b.release
	return []byte(`"value"`), nil
}

func TestPostHogOptOutInvalidatesOldEpoch(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	r := pt.New(pt.Settings{Egress: true, PostHogKey: "key"}, nil, nil)
	var calls int
	r.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	done := make(chan error, 1)
	go func() {
		done <- r.PostHog(context.Background(), perch.PostHogEvent{APIKey: "key", Event: perch.EventMessageSent, Properties: map[string]any{
			"blocked": blockingJSON{started: started, release: release},
		}})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("event serialization did not start")
	}
	r.SetSettings(pt.Settings{Egress: true, PostHogKey: "key", OptedOut: true})
	r.SetSettings(pt.Settings{Egress: true, PostHogKey: "key"})
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old event did not finish")
	}
	if calls != 0 {
		t.Fatalf("old event sent %d requests after opt-out", calls)
	}
	if err := r.PostHog(context.Background(), perch.PostHogEvent{APIKey: "key", Event: perch.EventMessageSent}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("new event sent %d requests, want 1", calls)
	}
}

func TestPostHogConfigurationChangeInvalidatesOldEvent(t *testing.T) {
	for _, next := range []pt.Settings{
		{Egress: true},
		{Egress: true, PostHogKey: "new-key"},
		{Egress: true, PostHogKey: "key", PostHogHost: "https://other.invalid"},
	} {
		t.Run(fmt.Sprintf("key=%t host=%t", next.PostHogKey != "", next.PostHogHost != ""), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			r := pt.New(pt.Settings{Egress: true, PostHogKey: "key"}, nil, nil)
			var calls int
			r.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
			})})
			done := make(chan error, 1)
			go func() {
				done <- r.PostHog(context.Background(), perch.PostHogEvent{APIKey: "key", Event: perch.EventMessageSent, Properties: map[string]any{
					"blocked": blockingJSON{started: started, release: release},
				}})
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("serialization did not start")
			}
			r.SetSettings(next)
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("old event did not finish")
			}
			if calls != 0 {
				t.Fatalf("sent %d stale events", calls)
			}
		})
	}
}

func TestPostHogStaleEventKeyIsNotSent(t *testing.T) {
	r := pt.New(pt.Settings{Egress: true, PostHogKey: "new-key"}, nil, nil)
	var calls int
	r.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})})
	if err := r.PostHog(context.Background(), perch.PostHogEvent{APIKey: "old-key", Event: perch.EventMessageSent}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("sent %d stale events", calls)
	}
}

func TestPostHogOptOutCancelsInFlight(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	r := pt.New(pt.Settings{Egress: true, PostHogKey: "key"}, nil, nil)
	r.SetHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		close(canceled)
		return nil, req.Context().Err()
	})})
	done := make(chan error, 1)
	go func() {
		done <- r.PostHog(context.Background(), perch.PostHogEvent{APIKey: "key", Event: perch.EventMessageSent})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	r.SetSettings(pt.Settings{Egress: true, PostHogKey: "key", OptedOut: true})
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("request context was not canceled")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("report did not stop")
	}
}

func TestCLITurnInvalidProxyAuditsWithoutSecrets(t *testing.T) {
	const secret = "secret-credential"
	repo := &fakeRepo{}
	r := pt.New(pt.Settings{Egress: true}, repo, nil)
	err := r.CLITurnWithProxy(context.Background(), "http://example.invalid", secret,
		perch.CLITurn{Status: secret, Prompt: secret}, "://"+secret)
	if err == nil {
		t.Fatal("invalid proxy succeeded")
	}
	if len(repo.rows) != 1 || !strings.Contains(repo.rows[0], "status=unknown result=proxy_error") {
		t.Fatalf("unexpected audit rows: %v", repo.rows)
	}
	if strings.Contains(repo.rows[0], secret) {
		t.Fatalf("audit leaked sensitive input: %q", repo.rows[0])
	}
}

type errorTransport struct{}

func (errorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("secret-network-error-token")
}

func TestAuditNeverRecordsSensitiveInputs(t *testing.T) {
	const secret = "sensitive-credential-pii"
	errText := secret
	turn := perch.CLITurn{
		Prompt: secret, AssistantText: secret, ThreadID: secret, RunID: secret,
		ChatMode: secret, PersonaID: &errText, Status: secret, Error: &errText,
		Events: []any{secret}, ContextSnapshot: map[string]any{"value": secret},
		Attachments: []any{secret}, AgentChat: map[string]any{"value": secret},
	}
	redacted, err := json.Marshal(perch.RedactTurn(turn))
	if err != nil || strings.Contains(string(redacted), secret) {
		t.Fatalf("redacted turn leaks input: %s, err=%v", redacted, err)
	}
	var logs []string
	repo := &fakeRepo{}
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	r := pt.New(pt.Settings{PostHogKey: secret}, repo, logf)
	endpoint := "http://" + secret + ":" + secret + "@example.invalid/path?token=" + secret
	if err := r.CLITurn(context.Background(), endpoint, secret, turn); err != nil {
		t.Fatal(err)
	}
	ev := perch.PostHogEvent{APIKey: secret, Event: secret, DistinctID: secret, Properties: map[string]any{"value": secret}}
	if err := r.PostHog(context.Background(), ev); err != nil {
		t.Fatal(err)
	}

	r.SetSettings(pt.Settings{Egress: true, PostHogKey: secret, PostHogHost: endpoint, OptedOut: true})
	_ = r.PostHog(context.Background(), ev)
	r.SetSettings(pt.Settings{Egress: true, PostHogKey: secret, PostHogHost: endpoint})
	r.SetHTTPClient(&http.Client{Transport: errorTransport{}})
	r.SetClock(nil, func(context.Context, time.Duration) error { return nil })
	_ = r.CLITurn(context.Background(), endpoint, secret, turn)
	_ = r.PostHog(context.Background(), ev)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, secret)
	}))
	defer srv.Close()
	r.SetHTTPClient(srv.Client())
	_ = r.CLITurn(context.Background(), srv.URL+"?token="+secret, secret, turn)
	success := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer success.Close()
	r.SetSettings(pt.Settings{Egress: true, PostHogKey: secret, PostHogHost: success.URL})
	_ = r.CLITurn(context.Background(), success.URL+"?token="+secret, secret, turn)
	_ = r.PostHog(context.Background(), ev)

	for _, row := range append(repo.rows, logs...) {
		for _, forbidden := range []string{secret, "secret-network-error-token", "example.invalid"} {
			if strings.Contains(row, forbidden) {
				t.Fatalf("audit/log contains %q: %q", forbidden, row)
			}
		}
	}
	if len(repo.rows) != 8 || len(logs) != 2 {
		t.Fatalf("unexpected audit/log counts: %d/%d", len(repo.rows), len(logs))
	}
}

type fakeRepo struct{ rows []string }

func (f *fakeRepo) Audit(provider, accountID, kind, detail string) {
	f.rows = append(f.rows, provider+"|"+accountID+"|"+kind+"|"+detail)
}
