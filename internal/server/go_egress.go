package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

var errGoEgress = errors.New("Go fixed egress verification failed; no alternate egress was attempted")
var errGoPolicyPause = errors.New("Go policy pause is active; review and recover the account before contacting upstream")

type goEgressView struct {
	Status     string    `json:"status"`
	ExpectedIP string    `json:"expectedIp,omitempty"`
	ObservedIP string    `json:"observedIp,omitempty"`
	CheckedAt  time.Time `json:"checkedAt,omitzero"`
	Reason     string    `json:"reason,omitempty"`
}
type goEgressEntry struct {
	view    goEgressView
	expires time.Time
	done    chan struct{}
}

func goEgressKey(cfg *config.Config, a config.Account) string {
	return config.HashKey(a.ID + "\x00" + a.ProxyURL + "\x00" + a.EgressIP + "\x00" + cfg.OpenCode.GoEgressCheckURL + "\x00" + strconv.Itoa(cfg.OpenCode.GoEgressCheckSec))
}

func (s *Server) goEgressView(cfg *config.Config, a config.Account) goEgressView {
	v := goEgressView{Status: "configured-only", ExpectedIP: a.EgressIP}
	if a.EgressIP != "" {
		v.Status = "unverified"
	}
	s.goEgress.Lock()
	defer s.goEgress.Unlock()
	if e := s.goEgress.entries[goEgressKey(cfg, a)]; e != nil {
		v = e.view
		if e.done != nil {
			v.Status = "checking"
		} else if !time.Now().Before(e.expires) {
			v.Status = "expired"
		}
	}
	return v
}

// Checks have no credentials and use the exact fixed account transport. An
// optional pin fails closed. A probe detects drift; it cannot make a rotating
// proxy static, nor promise that an IP stays unchanged between probes.
func (s *Server) ensureGoEgress(ctx context.Context, cfg *config.Config, a config.Account, force bool) error {
	if a.Provider != config.ProviderOpenCodeGo {
		return nil
	}
	if !cfg.OpenCode.Enabled {
		return errors.New("OpenCode is disabled; no upstream was contacted")
	}
	if s.pool != nil && !s.pool.PauseUntil(a.ID, "opencode-go-policy-refusal").IsZero() {
		return errGoPolicyPause
	}
	if a.EgressIP == "" && !force {
		return nil
	}
	if cfg.OpenCode.GoEgressCheckURL == "" {
		if a.EgressIP == "" {
			return nil
		}
		return errGoEgress
	}
	key := goEgressKey(cfg, a)
	for {
		s.goEgress.Lock()
		if s.goEgress.entries == nil {
			s.goEgress.entries = map[string]*goEgressEntry{}
		}
		if e := s.goEgress.entries[key]; e != nil {
			if e.done != nil {
				done := e.done
				s.goEgress.Unlock()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-done:
					force = false
					continue
				}
			}
			if !force && time.Now().Before(e.expires) {
				status := e.view.Status
				s.goEgress.Unlock()
				if status == "verified" || a.EgressIP == "" && status == "observed" {
					return nil
				}
				return errGoEgress
			}
		}
		if len(s.goEgress.entries) >= 1024 {
			oldest := ""
			var at time.Time
			for k, e := range s.goEgress.entries {
				if k != key && e.done == nil && (oldest == "" || e.expires.Before(at)) {
					oldest, at = k, e.expires
				}
			}
			if oldest != "" {
				delete(s.goEgress.entries, oldest)
			} else if s.goEgress.entries[key] == nil {
				s.goEgress.Unlock()
				return errGoEgress
			}
		}
		e := &goEgressEntry{view: goEgressView{Status: "checking", ExpectedIP: a.EgressIP}, done: make(chan struct{})}
		s.goEgress.entries[key] = e
		s.goEgress.Unlock()
		ip, err := s.probeGoIP(ctx, cfg.OpenCode.GoEgressCheckURL, a.ProxyURL)
		view := goEgressView{Status: "observed", ExpectedIP: a.EgressIP, ObservedIP: ip, CheckedAt: time.Now().UTC()}
		if err != nil {
			view.Status, view.Reason = "failed", "fixed egress IP check unavailable"
		} else if a.EgressIP != "" {
			if ip != a.EgressIP {
				err = errGoEgress
				view.Status, view.Reason = "mismatch", "observed IP differs from the configured pin"
			} else {
				view.Status = "verified"
			}
		}
		s.goEgress.Lock()
		e.view = view
		ttl := time.Duration(cfg.OpenCode.GoEgressCheckSec) * time.Second
		if err != nil {
			ttl = min(ttl, 5*time.Second)
		}
		e.expires = time.Now().Add(ttl)
		close(e.done)
		e.done = nil
		s.goEgress.Unlock()
		if err != nil {
			return errGoEgress
		}
		return nil
	}
}

func (s *Server) probeGoIP(ctx context.Context, endpoint, proxy string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errGoEgress
	}
	req.Header.Set("User-Agent", "account-pool-balancer/go-egress-check")
	req.Header.Set("Accept", "application/json, text/plain")
	resp, err := opencode.NoRedirect(s.httpClient(proxy)).Do(req)
	if err != nil {
		return "", errGoEgress
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errGoEgress
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(b) > 4096 {
		return "", errGoEgress
	}
	text := strings.TrimSpace(string(b))
	var obj struct {
		IP string `json:"ip"`
	}
	if json.Unmarshal(b, &obj) == nil && obj.IP != "" {
		text = obj.IP
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(text))
	if err != nil || ip.Zone() != "" {
		return "", errGoEgress
	}
	return ip.Unmap().String(), nil
}
