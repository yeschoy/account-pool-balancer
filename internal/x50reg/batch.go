package x50reg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Job is one mailbox to turn into an account.
type Job struct {
	Mailbox  Mailbox
	Password string
	Proxy    string
}

// Result is the per-job outcome.
type Result struct {
	Email   string    `json:"email"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	Proxy   string    `json:"proxy,omitempty"`
	Account *Account  `json:"account,omitempty"`
	Elapsed string    `json:"elapsed"`
	At      time.Time `json:"at"`
}

// BatchConfig drives a multi-account run.
type BatchConfig struct {
	Mailboxes []Mailbox
	Proxies   *ProxyPool

	Concurrency  int
	MailWait     time.Duration
	MailInterval time.Duration
	Timeout      time.Duration
	KeyName      string
	APIBase      string
	Origin       string
	MailBase     string
	UserAgent    string

	// OutPath receives one JSON object per finished job (append).
	OutPath string
	// Tracer receives progress lines, already prefixed with the account.
	Tracer Tracer

	// passwords maps mailbox address to its chosen password; empty means a
	// freshly generated one per job.
	passwords map[string]string
}

// SetPasswords installs per-mailbox passwords.
func (c *BatchConfig) SetPasswords(m map[string]string) { c.passwords = m }

// PasswordFor returns the configured password for an address.
func (c BatchConfig) PasswordFor(email string) string { return c.passwords[email] }

// RunBatch registers every mailbox, up to Concurrency at a time, and appends
// results to OutPath as they finish.
func RunBatch(ctx context.Context, cfg BatchConfig) ([]Result, error) {
	if len(cfg.Mailboxes) == 0 {
		return nil, fmt.Errorf("no mailboxes")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MailWait <= 0 {
		cfg.MailWait = 180 * time.Second
	}
	if cfg.MailInterval <= 0 {
		cfg.MailInterval = 3 * time.Second
	}
	trace := cfg.Tracer
	if trace == nil {
		trace = func(string, ...any) {}
	}

	var (
		mu      sync.Mutex
		outMu   sync.Mutex
		results = make([]Result, 0, len(cfg.Mailboxes))
		outFile *os.File
	)
	if cfg.OutPath != "" {
		f, err := os.OpenFile(cfg.OutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, fmt.Errorf("open output: %w", err)
		}
		outFile = f
		defer f.Close()
	}

	sem := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	for _, mb := range cfg.Mailboxes {
		wg.Add(1)
		go func(mb Mailbox) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			start := time.Now()
			proxy := ""
			if cfg.Proxies != nil {
				proxy = WithStickySID(cfg.Proxies.Pick())
			}
			res := Result{Email: mb.Email, Proxy: redactProxy(proxy), At: start}
			acc, err := registerOne(ctx, cfg, mb, proxy, trace)
			res.Elapsed = time.Since(start).Round(time.Millisecond).String()
			if err != nil {
				res.Error = err.Error()
				trace("[FAIL] %s (%.1fs) %v", mb.Email, time.Since(start).Seconds(), err)
			} else {
				res.OK = true
				res.Account = acc
				trace("[ OK ] %s key=%s balance=%s$ (%.1fs)", mb.Email, acc.APIKey, acc.BalanceUSD, time.Since(start).Seconds())
			}
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
			if outFile != nil {
				outMu.Lock()
				if b, jerr := json.Marshal(res); jerr == nil {
					outFile.Write(append(b, '\n'))
					outFile.Sync()
				}
				outMu.Unlock()
			}
		}(mb)
	}
	wg.Wait()
	return results, nil
}

func registerOne(ctx context.Context, cfg BatchConfig, mb Mailbox, proxy string, trace Tracer) (*Account, error) {
	pw := cfg.PasswordFor(mb.Email)
	if pw == "" {
		pw = RandomPassword()
	}
	r, err := New(Config{
		APIBase:      cfg.APIBase,
		Origin:       cfg.Origin,
		MailBase:     cfg.MailBase,
		ProxyURL:     proxy,
		Email:        mb.Email,
		Password:     pw,
		Mailbox:      mb,
		KeyName:      cfg.KeyName,
		MailWait:     cfg.MailWait,
		MailInterval: cfg.MailInterval,
		Timeout:      cfg.Timeout,
		UserAgent:    cfg.UserAgent,
	})
	if err != nil {
		return nil, err
	}
	r.SetTracer(func(f string, a ...any) {
		trace("       %s: "+f, append([]any{mb.Email}, a...)...)
	})
	acc, err := r.Register(ctx)
	if err != nil && proxy != "" {
		var rej *ErrProxyRejected
		switch {
		case errors.As(err, &rej):
			cfg.Proxies.MarkBad(proxy)
			trace("       %s: proxy refused the tunnel, quarantined", mb.Email)
		case isNetworkError(err):
			cfg.Proxies.MarkBad(proxy)
		}
	}
	return acc, err
}

func isNetworkError(err error) bool {
	s := err.Error()
	for _, k := range []string{"proxyconnect", "socks connect", "connection refused", "i/o timeout", "no such host", "connection reset", "TLS handshake"} {
		if strings.Contains(s, k) {
			return true
		}
	}
	return false
}

// redactProxy hides credentials while keeping the gateway identity.
func redactProxy(raw string) string {
	if raw == "" {
		return ""
	}
	i := strings.Index(raw, "@")
	if i < 0 {
		return raw
	}
	scheme := strings.Index(raw, "://")
	if scheme < 0 {
		return "***" + raw[i:]
	}
	return raw[:scheme+3] + "***" + raw[i:]
}
