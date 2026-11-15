package telnyxreg

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// EgressProbe reports where the pinned proxy actually exits.
//
// Telnyx binds the one-time portal token to the egress that redeemed the
// signup, so every request for one account must leave from the same address.
// Before a run, the operator can compare this against the proxy's advertised
// region; during a run, a mid-flight change is what produces 401/403 on
// /v2/ai_dev_event_sessions even though the link is fresh.
type EgressProbe struct {
	URL   string        // default https://api.ipify.org
	HTTP  *http.Client  // must already carry the proxy
	Cache time.Duration // how long a lookup is reused; 0 = no cache
}

const defaultEgressURL = "https://api.ipify.org"

// IP returns the egress address seen by the probe.
func (p *EgressProbe) IP(ctx context.Context) (string, error) {
	u := p.URL
	if u == "" {
		u = defaultEgressURL
	}
	hc := p.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("egress probe → HTTP %d", resp.StatusCode)
	}
	ip := strings.TrimSpace(string(buf))
	if ip == "" {
		return "", fmt.Errorf("egress probe returned nothing")
	}
	return ip, nil
}

// CheckEgress pins the proxy's exit address for one account and makes sure the
// address is reachable before spending captcha credits.
func (r *Registrar) CheckEgress(ctx context.Context) (string, error) {
	p := &EgressProbe{HTTP: r.http}
	ip, err := p.IP(ctx)
	if err != nil {
		return "", fmt.Errorf("egress: %w", err)
	}
	r.mu.Lock()
	r.egressIP = ip
	r.mu.Unlock()
	return ip, nil
}

// EgressIP returns the address recorded by CheckEgress, if any.
func (r *Registrar) EgressIP() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.egressIP
}
