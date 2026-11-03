// Package opencode contains independently implemented, authenticated OpenCode
// wire contracts. It does not own accounts, scheduling, or an admin server.
package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Protocol string

const (
	Chat      Protocol = "chat"
	Responses Protocol = "responses"
	Messages  Protocol = "messages"
)

func Path(p Protocol) string {
	switch p {
	case Chat:
		return "/chat/completions"
	case Responses:
		return "/responses"
	case Messages:
		return "/messages"
	}
	return ""
}
func FromPath(path string) Protocol {
	switch path {
	case "/v1/chat/completions":
		return Chat
	case "/v1/responses":
		return Responses
	case "/v1/messages":
		return Messages
	}
	return ""
}

type Model struct {
	Protocol         Protocol `json:"nativeProtocol,omitempty"`
	Context          int      `json:"contextWindow,omitempty"`
	Input            int      `json:"maxInput,omitempty"`
	Output           int      `json:"maxOutput,omitempty"`
	Reasoning        bool     `json:"reasoning"`
	Tools            bool     `json:"tools"`
	InputModalities  []string `json:"inputModalities,omitempty"`
	OutputModalities []string `json:"outputModalities,omitempty"`
}

type Capabilities map[string]map[string]Model

// NoRedirect preserves the caller's fixed transport while refusing redirect
// credential leaks (including same-host path redirects to a login page).
func NoRedirect(hc *http.Client) *http.Client {
	c := *hc
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func ReadJSON(ctx context.Context, hc *http.Client, endpoint, key string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return errors.New("invalid OpenCode endpoint")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "account-pool-balancer")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := NoRedirect(hc).Do(req)
	if err != nil {
		return errors.New("OpenCode connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("OpenCode endpoint returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil || len(b) > 16<<20 {
		return errors.New("OpenCode response unreadable or too large")
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("invalid OpenCode JSON")
	}
	return nil
}

func FetchCapabilities(ctx context.Context, hc *http.Client, endpoint, zenDoc, goDoc string) (Capabilities, error) {
	type entry struct {
		ID       string `json:"id"`
		Provider *struct {
			NPM string `json:"npm"`
		} `json:"provider"`
		Limit      struct{ Context, Input, Output int } `json:"limit"`
		Reasoning  bool                                 `json:"reasoning"`
		ToolCall   bool                                 `json:"tool_call"`
		Modalities struct{ Input, Output []string }     `json:"modalities"`
	}
	type provider struct {
		NPM    string           `json:"npm"`
		Models map[string]entry `json:"models"`
	}
	var providers map[string]json.RawMessage
	if err := ReadJSON(ctx, hc, endpoint, "", &providers); err != nil {
		return nil, err
	}
	out := Capabilities{"zen": {}, "go": {}}
	for id, raw := range providers {
		tier := ""
		// Restrict provider IDs rather than treating any URL containing opencode
		// as an entitlement declaration. No upstream URL from this data is dialed.
		switch id {
		case "opencode", "opencode-zen":
			tier = "zen"
		case "opencode-go":
			tier = "go"
		}
		if tier == "" {
			continue
		}
		var p provider
		if json.Unmarshal(raw, &p) != nil {
			return nil, errors.New("invalid OpenCode provider capability JSON")
		}
		for id, m := range p.Models {
			if m.ID != "" {
				id = m.ID
			}
			sdk := p.NPM
			if m.Provider != nil && m.Provider.NPM != "" {
				sdk = m.Provider.NPM
			}
			var proto Protocol
			switch sdk {
			case "@ai-sdk/openai":
				proto = Responses
			case "@ai-sdk/openai-compatible":
				proto = Chat
			case "@ai-sdk/anthropic":
				proto = Messages
			}
			out[tier][id] = Model{Protocol: proto, Context: m.Limit.Context, Input: m.Limit.Input, Output: m.Limit.Output, Reasoning: m.Reasoning, Tools: m.ToolCall, InputModalities: m.Modalities.Input, OutputModalities: m.Modalities.Output}
		}
	}
	if len(out["zen"])+len(out["go"]) == 0 {
		return nil, errors.New("OpenCode capability catalog contains no known providers")
	}
	for tier, endpoint := range map[string]string{"zen": zenDoc, "go": goDoc} {
		if endpoint == "" {
			continue
		}
		// An administrator explicitly configuring a supplemental source expects it
		// to work. A failed source must not silently install potentially wrong SDK defaults.
		rows, err := fetchDocs(ctx, hc, endpoint)
		if err != nil {
			return nil, err
		}
		for id, proto := range rows {
			m := out[tier][id]
			m.Protocol = proto
			out[tier][id] = m
		}
	}
	return out, nil
}

var endpointRow = regexp.MustCompile("\\|[^|]+\\|\\s*`?([^|`\\s]+)`?\\s*\\|\\s*`[^`]+/v1/(chat/completions|responses|messages)`")

func fetchDocs(ctx context.Context, hc *http.Client, endpoint string) (map[string]Protocol, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid OpenCode protocol docs URL")
	}
	resp, err := NoRedirect(hc).Do(req)
	if err != nil {
		return nil, errors.New("OpenCode protocol docs unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("OpenCode protocol docs HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil || len(b) > 4<<20 {
		return nil, errors.New("invalid OpenCode protocol docs")
	}
	out := map[string]Protocol{}
	for _, m := range endpointRow.FindAllStringSubmatch(string(b), -1) {
		out[m[1]] = FromPath("/v1/" + m[2])
	}
	if len(out) == 0 {
		return nil, errors.New("OpenCode protocol docs contain no endpoint rows")
	}
	return out, nil
}

type discoveryCall struct {
	done    chan struct{}
	models  Capabilities
	err     error
	expires time.Time
}

// Discovery coalesces public capability reads per fixed egress and source.
// Returned maps are immutable; an account stores only entries it advertised.
type Discovery struct {
	mu      sync.Mutex
	entries map[string]*discoveryCall
}

// Invalidate expires a completed entry for an explicit administrator refresh.
// An in-flight refresh is coalesced rather than duplicated.
func (d *Discovery) Invalidate(scope, endpoint, zenDoc, goDoc string) {
	key := strings.Join([]string{scope, endpoint, zenDoc, goDoc}, "\x00")
	d.mu.Lock()
	defer d.mu.Unlock()
	if c := d.entries[key]; c != nil && !c.expires.IsZero() {
		delete(d.entries, key)
	}
}

func (d *Discovery) Get(ctx context.Context, hc *http.Client, scope, endpoint, zenDoc, goDoc string, ttl time.Duration) (Capabilities, error) {
	key := strings.Join([]string{scope, endpoint, zenDoc, goDoc}, "\x00")
	d.mu.Lock()
	if d.entries == nil {
		d.entries = map[string]*discoveryCall{}
	}
	if c := d.entries[key]; c != nil && (c.expires.IsZero() || time.Now().Before(c.expires)) {
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.done:
			return c.models, c.err
		}
	}
	// Bound retained public catalogs even with thousands of fixed proxies.
	// Active calls are never evicted; their waiting readers still own them.
	if len(d.entries) >= 128 {
		oldestKey := ""
		var oldest time.Time
		for k, c := range d.entries {
			if !c.expires.IsZero() && (oldest.IsZero() || c.expires.Before(oldest)) {
				oldestKey, oldest = k, c.expires
			}
		}
		if oldestKey != "" {
			delete(d.entries, oldestKey)
		}
	}
	c := &discoveryCall{done: make(chan struct{})}
	d.entries[key] = c
	d.mu.Unlock()
	models, err := FetchCapabilities(ctx, hc, endpoint, zenDoc, goDoc)
	d.mu.Lock()
	c.models, c.err = models, err
	if err != nil {
		ttl = 15 * time.Second
	}
	c.expires = time.Now().Add(ttl)
	close(c.done)
	d.mu.Unlock()
	return models, err
}
