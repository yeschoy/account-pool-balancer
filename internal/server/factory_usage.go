package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
)

// factoryUsageModel is one model's share of the scanned request log.
type factoryUsageModel struct {
	Model        string  `json:"model"`
	Requests     int64   `json:"requests"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CachedTokens int64   `json:"cachedTokens"`
	CostUSD      float64 `json:"costUsd"`
	Priced       bool    `json:"priced"` // false: the model has no rate
	// TTFTP50/TTFTP95 are the measured time-to-first-token of this model's
	// successful streaming requests, so a caller can tell in advance what to
	// expect instead of watching a blank screen.
	TTFTP50     int64 `json:"ttftP50Ms,omitempty"`
	TTFTP95     int64 `json:"ttftP95Ms,omitempty"`
	TTFTSamples int64 `json:"ttftSamples,omitempty"`
}

// factoryUsageAccount is one Factory account's share of the scanned request log.
type factoryUsageAccount struct {
	AccountID    string  `json:"accountId"`
	AccountName  string  `json:"accountName,omitempty"`
	Email        string  `json:"email,omitempty"`
	Configured   bool    `json:"configured"`
	Enabled      bool    `json:"enabled"`
	Requests     int64   `json:"requests"`
	Success      int64   `json:"success"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CachedTokens int64   `json:"cachedTokens"`
	CostUSD      float64 `json:"costUsd"`
	Unpriced     int64   `json:"unpriced,omitempty"`
	LastAt       string  `json:"lastAt,omitempty"`
}

// factoryUsageTotals is what Google's published rates make of the requests the
// Factory pool served. It is an estimate from the recorded token counts, not an
// upstream invoice: a request whose usage was never reported cannot be priced.
type factoryUsageTotals struct {
	From         string                `json:"from"` // first timestamp in the logs
	To           string                `json:"to"`
	Requests     int64                 `json:"requests"`
	InputTokens  int64                 `json:"inputTokens"`
	OutputTokens int64                 `json:"outputTokens"`
	CachedTokens int64                 `json:"cachedTokens"`
	CostUSD      float64               `json:"costUsd"`
	Unpriced     int64                 `json:"unpriced"` // requests with no rate or no usage
	Models       []factoryUsageModel   `json:"models"`
	Accounts     []factoryUsageAccount `json:"accounts,omitempty"`
	ScannedAt    string                `json:"scannedAt"`
}

type factoryUsageCache struct {
	mu     sync.Mutex
	at     time.Time
	rev    int64
	totals factoryUsageTotals
}

// factoryUsage recomputes the totals at most once a minute (or when config
// changes): the request log is append-only and its segments are small, so a
// full scan is cheap, but the console refreshes often.
func (s *Server) factoryUsage(cfg *config.Config) factoryUsageTotals {
	s.usageCache.mu.Lock()
	defer s.usageCache.mu.Unlock()
	if !s.usageCache.at.IsZero() && s.usageCache.rev == cfg.Revision && time.Since(s.usageCache.at) < time.Minute {
		return s.usageCache.totals
	}
	totals := s.scanFactoryUsage(cfg)
	s.usageCache.at = time.Now()
	s.usageCache.rev = cfg.Revision
	s.usageCache.totals = totals
	return totals
}

// scanFactoryUsage reads every request-log segment and prices the Factory rows
// from their recorded tokens.
func (s *Server) scanFactoryUsage(cfg *config.Config) factoryUsageTotals {
	price := cfg.Factory.EffectivePricing()
	totals := factoryUsageTotals{ScannedAt: time.Now().Format(time.RFC3339)}
	byModel := map[string]*factoryUsageModel{}
	byAccount := map[string]*factoryUsageAccount{}
	for _, a := range cfg.Accounts {
		if a.Provider != config.ProviderFactory {
			continue
		}
		byAccount[a.ID] = &factoryUsageAccount{
			AccountID:   a.ID,
			AccountName: a.Name,
			Email:       a.Email,
			Configured:  true,
			Enabled:     a.Enabled,
		}
	}
	ttft := map[string][]int64{}
	var paths []string
	if s.dataDir != "" {
		paths, _ = filepath.Glob(filepath.Join(s.dataDir, "logs", "requests-*.jsonl"))
		sort.Strings(paths)
	}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 256<<10), 8<<20)
		for sc.Scan() {
			var rec struct {
				TS           string   `json:"ts"`
				Provider     string   `json:"provider"`
				Model        string   `json:"model"`
				AccountID    string   `json:"accountId"`
				AccountName  string   `json:"accountName"`
				InputTokens  *int64   `json:"inputTokens"`
				OutputTokens *int64   `json:"outputTokens"`
				CachedTokens *int64   `json:"cachedTokens"`
				Cost         *float64 `json:"cost"`
				TTFTMs       *int64   `json:"ttftMs"`
				Status       int      `json:"status"`
				Currency     string   `json:"currency"`
			}
			if json.Unmarshal(sc.Bytes(), &rec) != nil {
				continue
			}
			if rec.Provider != config.ProviderFactory {
				continue
			}
			if totals.From == "" || rec.TS < totals.From {
				totals.From = rec.TS
			}
			if rec.TS > totals.To {
				totals.To = rec.TS
			}
			totals.Requests++
			if rec.InputTokens != nil {
				totals.InputTokens += *rec.InputTokens
			}
			if rec.OutputTokens != nil {
				totals.OutputTokens += *rec.OutputTokens
			}
			if rec.CachedTokens != nil {
				totals.CachedTokens += *rec.CachedTokens
			}
			if rec.TTFTMs != nil && *rec.TTFTMs > 0 && rec.Status == 200 {
				ttft[rec.Model] = append(ttft[rec.Model], *rec.TTFTMs)
			}
			m := byModel[rec.Model]
			if m == nil {
				m = &factoryUsageModel{Model: rec.Model}
				byModel[rec.Model] = m
			}
			m.Requests++
			if rec.InputTokens != nil {
				m.InputTokens += *rec.InputTokens
			}
			if rec.OutputTokens != nil {
				m.OutputTokens += *rec.OutputTokens
			}
			if rec.CachedTokens != nil {
				m.CachedTokens += *rec.CachedTokens
			}
			var acc *factoryUsageAccount
			if rec.AccountID != "" {
				acc = byAccount[rec.AccountID]
				if acc == nil {
					acc = &factoryUsageAccount{AccountID: rec.AccountID, AccountName: rec.AccountName}
					byAccount[rec.AccountID] = acc
				} else if acc.AccountName == "" && rec.AccountName != "" {
					acc.AccountName = rec.AccountName
				}
				acc.Requests++
				if rec.Status >= 200 && rec.Status < 300 {
					acc.Success++
				}
				if rec.InputTokens != nil {
					acc.InputTokens += *rec.InputTokens
				}
				if rec.OutputTokens != nil {
					acc.OutputTokens += *rec.OutputTokens
				}
				if rec.CachedTokens != nil {
					acc.CachedTokens += *rec.CachedTokens
				}
				if rec.TS > acc.LastAt {
					acc.LastAt = rec.TS
				}
			}
			// A recorded cost is authoritative; otherwise price the tokens with
			// the rate in force at the request's time.
			cost, currency, ok := 0.0, "", false
			if rec.Cost != nil && *rec.Cost > 0 {
				cost, currency, ok = *rec.Cost, rec.Currency, true
			} else if ts, err := time.Parse(time.RFC3339Nano, rec.TS); err == nil {
				if v, cur, found := price.Cost(rec.Model, ts, rec.InputTokens, rec.CachedTokens, rec.OutputTokens, false); found {
					cost, currency, ok = v, cur, true
				}
			}
			if !ok || currency != "USD" {
				totals.Unpriced++
				if acc != nil {
					acc.Unpriced++
				}
				continue
			}
			m.CostUSD += cost
			m.Priced = true
			totals.CostUSD += cost
			if acc != nil {
				acc.CostUSD += cost
			}
		}
		f.Close()
	}
	for name, values := range ttft {
		if len(values) == 0 {
			continue
		}
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		m := byModel[name]
		if m == nil {
			m = &factoryUsageModel{Model: name}
			byModel[name] = m
		}
		m.TTFTSamples = int64(len(values))
		m.TTFTP50 = values[(len(values)-1)*50/100]
		m.TTFTP95 = values[(len(values)-1)*95/100]
	}
	models := make([]factoryUsageModel, 0, len(byModel))
	for _, m := range byModel {
		models = append(models, *m)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].CostUSD > models[j].CostUSD })
	totals.Models = models
	if len(byAccount) > 0 {
		accounts := make([]factoryUsageAccount, 0, len(byAccount))
		for _, a := range byAccount {
			accounts = append(accounts, *a)
		}
		sort.Slice(accounts, func(i, j int) bool {
			if accounts[i].CostUSD != accounts[j].CostUSD {
				return accounts[i].CostUSD > accounts[j].CostUSD
			}
			if accounts[i].Requests != accounts[j].Requests {
				return accounts[i].Requests > accounts[j].Requests
			}
			if accounts[i].AccountName != accounts[j].AccountName {
				return accounts[i].AccountName < accounts[j].AccountName
			}
			return accounts[i].AccountID < accounts[j].AccountID
		})
		totals.Accounts = accounts
	}
	return totals
}

// apiFactoryUsage serves the estimate; the console calls it from the Factory
// page so the number is visible without waiting for a full overview refresh.
func (s *Server) apiFactoryUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"usage": s.factoryUsage(s.cfg.Get())})
}
