package quota

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/opencode"
)

// Snapshot is what was last read for one account. Each part keeps its own
// timestamp: a failed part keeps its previous value.
type Snapshot struct {
	WorkspaceID       string    `json:"workspaceId,omitempty"`
	WorkspaceMemberID string    `json:"workspaceMemberId,omitempty"`
	WorkspaceName     string    `json:"workspaceName,omitempty"`
	WorkspaceType     string    `json:"workspaceType,omitempty"`
	MembershipTier    string    `json:"membershipTier,omitempty"`
	WorkspaceAt       time.Time `json:"workspaceAt,omitzero"`

	PlanAt              time.Time `json:"planAt,omitzero"`
	Eligible            bool      `json:"eligible"`
	Tier                string    `json:"tier,omitempty"`
	Windows             []Window  `json:"windows"`
	Funding             string    `json:"funding,omitempty"`
	ModelCovered        *bool     `json:"modelCovered,omitempty"`
	PreflightBalanceUSD string    `json:"preflightBalanceUsd,omitempty"`

	WalletUSD    string    `json:"walletUsd,omitempty"`
	WalletSource string    `json:"walletSource,omitempty"` // wallet | preflight
	WalletAt     time.Time `json:"walletAt,omitzero"`

	Summary   *Summary  `json:"summary,omitempty"`
	SummaryAt time.Time `json:"summaryAt,omitzero"`

	// Coverage is preflight?modelId= per model: whether the model is a
	// Design Plan model on this account's tier (covered) or a 套餐模型 paid
	// from subscription credits / wallet (not covered).
	Coverage map[string]*Coverage `json:"coverage,omitempty"`

	// Catalog is the runtime key's own /v1/models: model id → enabled.
	// Which models a plan may use is read here, not from the pricing page,
	// whose display names don't match the API ids.
	Catalog      map[string]bool `json:"catalog,omitempty"`
	CatalogAt    time.Time       `json:"catalogAt,omitzero"`
	CatalogError string          `json:"catalogError,omitempty"`

	// Aliases maps a model id this account was asked for that it serves by
	// another name, asked id → the id to send upstream. Modal's Shared
	// Endpoints are hostnames unique per proxy token, so three tokens that
	// all run DeepSeek each publish a different id for it; a request naming
	// the base model is sent as this account's own hostname.
	Aliases map[string]string `json:"aliases,omitempty"`
	// Bases maps this account's own catalogue ids to the base model they
	// serve, so several accounts' ids collapse onto one name downstream.
	Bases map[string]string `json:"bases,omitempty"`

	// Ollama is the last GET /api/balance of an Ollama account: its API key
	// reads its own balance, there being no control key. Only a read younger
	// than the stale window is trusted.
	Ollama    *OllamaBalance `json:"ollama,omitempty"`
	BalanceAt time.Time      `json:"balanceAt,omitzero"`

	// Perch is the usage meter the Perch pool reads: Perch's own numbers, in
	// Perch's units. It is separate from Windows (which are OpenDesign's
	// shape) so neither provider's figures are misread as the other's.
	Perch      *PerchMeter `json:"perch,omitempty"`
	PerchAt    time.Time   `json:"perchAt,omitzero"`
	PerchError string      `json:"perchError,omitempty"`

	OpenCode *opencode.State `json:"opencode,omitempty"`

	AuthFailed  bool      `json:"authFailed"`
	LastError   string    `json:"lastError,omitempty"`
	LastErrorAt time.Time `json:"lastErrorAt,omitzero"`
	LastOKAt    time.Time `json:"lastOkAt,omitzero"`
	Failures    int       `json:"failures"`
	NextAt      time.Time `json:"nextAt,omitzero"`
}

// Coverage is one model's preflight answer.
type Coverage struct {
	Covered *bool     `json:"covered"`
	Funding string    `json:"funding"`
	At      time.Time `json:"at"`
}

// PerchMeter is what Perch's usage meter reported, converted to Perch's PT
// unit (a thousandth of a dollar, rounded up). Every figure is what Perch
// said; a figure Perch did not report stays zero and is shown as unknown.
type PerchMeter struct {
	// Plan is the plan label Perch announced.
	Plan string `json:"plan,omitempty"`
	// PlanCode is the raw code ("pilot", "pro", "ultra", …); it decides the
	// fallback caps below.
	PlanCode string `json:"planCode,omitempty"`
	// AllowancePT is the month's cap, and AllowanceSource says where it came
	// from: an entitlement Perch stated, Perch's own plan-code fallback, or
	// "none" (genuinely unknown).
	AllowancePT     int64  `json:"allowancePt,omitempty"`
	AllowanceSource string `json:"allowanceSource,omitempty"`
	// TierSelectionRequired: Perch says this account has not picked a plan
	// yet. It has no plan code and no entitlements, so there is no allowance
	// and no model access until its owner chooses one on the website.
	TierSelectionRequired bool `json:"tierSelectionRequired,omitempty"`
	// ManualLimitPT caps manual (non-plan) model calls, same provenance.
	ManualLimitPT     int64  `json:"manualLimitPt,omitempty"`
	ManualLimitSource string `json:"manualLimitSource,omitempty"`
	// CreditBalancePT is purchased credit, which sits outside the plan.
	CreditBalancePT int64 `json:"creditBalancePt,omitempty"`
	// RoostUnlimited: the plan's roost entitlement is unlimited.
	RoostUnlimited bool `json:"roostUnlimited,omitempty"`
	// Unlimited: an internal/staff account Perch does not meter.
	Unlimited bool `json:"unlimited,omitempty"`
	// WorkspaceID is the workspace the account's turns are attributed to, as
	// /api/perch-terminal/cli-context stated it. Empty means Perch said none
	// (or the route was not read); the model call then sends no attribution
	// block, exactly as the published client does.
	WorkspaceID string `json:"workspaceId,omitempty"`
	// UsedPT is this month's plan spend (the month's total less the manual
	// part, which the plan's allowance does not cover).
	UsedPT        int64     `json:"usedPt"`
	ManualPT      int64     `json:"manualPt"`
	DailyPT       int64     `json:"dailyPt"`
	WeeklyPT      int64     `json:"weeklyPt"`
	DailyCalls    int64     `json:"dailyCalls"`
	WeeklyCalls   int64     `json:"weeklyCalls"`
	MonthlyCalls  int64     `json:"monthlyCalls"`
	WeeklyTokens  int64     `json:"weeklyTokens"`
	MonthlyTokens int64     `json:"monthlyTokens"`
	PeriodStart   string    `json:"periodStart,omitempty"`
	ResetsAt      time.Time `json:"resetsAt,omitzero"`
	// Raw is Perch's answer, for the console's detail view.
	Raw map[string]any `json:"raw,omitempty"`
}

// RemainingPT is the month's headroom when Perch stated a cap, else 0 with
// known=false: the console shows "unknown" rather than inventing a number.
func (m *PerchMeter) RemainingPT() (int64, bool) {
	if m == nil || m.Unlimited || m.AllowancePT <= 0 {
		return 0, false
	}
	rem := m.AllowancePT - m.UsedPT
	if rem < 0 {
		rem = 0
	}
	return rem, true
}

// Exhausted reports whether the meter says the month's allowance is spent.
// Without a stated cap there is no such verdict to give.
func (m *PerchMeter) Exhausted(now time.Time) bool {
	if m == nil || m.Unlimited || m.AllowancePT <= 0 {
		return false
	}
	if !m.ResetsAt.IsZero() && now.After(m.ResetsAt) {
		return false // the period rolled over; the meter is stale
	}
	return m.UsedPT >= m.AllowancePT
}

// FreshPerch: the meter was read within stale and the last read did not fail.
func (s *Snapshot) FreshPerch(now time.Time, stale time.Duration) bool {
	return s != nil && s.Perch != nil && s.PerchError == "" && !s.PerchAt.IsZero() && now.Sub(s.PerchAt) < stale
}

const (
	coverageTTL  = 6 * time.Hour
	catalogTTL   = 10 * time.Minute
	catalogStale = 24 * time.Hour
)

// catalogDue reports whether an account's catalogue should be read again:
// it is older than catalogTTL, or it was stored before alias tables existed
// (a snapshot from a build that didn't record base_model_id), so an upgraded
// instance would otherwise keep answering from alias-less data until the
// TTL ran out and publish raw endpoint hostnames. mayAlias says whether the
// account's upstream can publish base_model_id at all, so a pool that never
// does is not re-read on every tick.
func catalogDue(s *Snapshot, now time.Time, hasKey, mayAlias bool) bool {
	if !hasKey {
		return false
	}
	if now.Sub(s.CatalogAt) >= catalogTTL {
		return true
	}
	return mayAlias && len(s.Catalog) > 0 && len(s.Bases) == 0
}

// nextPoll is when to look at an account again. A catalogue that wasn't due
// this tick would otherwise wait a whole catalogTTL from now, so it would be
// read every other tick and be older than catalogTTL for most of that time —
// which the console reports as a stale catalogue. Landing the next poll on
// the moment it does fall due keeps it read once per TTL, in time.
func nextPoll(s *Snapshot, cfg *config.Config, now time.Time, hasKey bool) time.Time {
	next := now.Add(max(time.Duration(cfg.Quota.IntervalSec)*time.Second, catalogTTL))
	if !hasKey || s.CatalogAt.IsZero() {
		return next
	}
	if due := s.CatalogAt.Add(catalogTTL); due.After(now) && due.Before(next) {
		return due
	}
	return next
}

// DefaultDesignPlanModels are the Design Plan models as of 2026-10-23: the
// pricing page's list, and exactly what a Go-tier key's /v1/models returns.
// They classify a model only while no account with a control key has asked
// preflight about it.
var DefaultDesignPlanModels = map[string]bool{
	"claude-haiku-5.5": true, "deepseek-v4-flash": true, "deepseek-v4-flash-vision-exp": true,
	"deepseek-v4-pro": true, "deepseek-v4.1-flash": true, "glm-5.3-flash": true, "glm-5.3-flashx": true,
	"gpt-6-luna": true, "hy4-preview": true, "kimi-k2.7-code": true, "mimo-v2.6-flash": true, "mimo-v2.6-pro": true,
}

// NotEntitled: there is evidence the account's plan can't call model —
// its catalogue lacks it or marks it disabled, or it is a 套餐模型 on Go,
// which has no 套餐模型 at all. An Ollama account has no plan gate here: the
// cloud's /v1/models is the public catalogue every key sees, so it says
// nothing about one key's entitlement, and a self-hosted server's list only
// changes when models are pulled.
func (s *Snapshot) NotEntitled(model string, now time.Time) bool {
	if s == nil || model == "" || s.Ollama != nil {
		return false
	}
	if len(s.Catalog) > 0 && now.Sub(s.CatalogAt) < catalogStale {
		// a base model this account serves through one of its aliases counts
		// as served: the request is translated back to that id when sent
		id := s.UpstreamModel(model)
		if enabled, ok := s.Catalog[id]; !ok || !enabled {
			return true
		}
	}
	tier := s.Tier
	if tier == "" {
		tier = s.MembershipTier
	}
	return strings.EqualFold(tier, "go") && s.ModelClass(model) == "package"
}

// Models are the catalogue's enabled models (nil while unknown or stale).
func (s *Snapshot) Models(now time.Time) []string {
	if s == nil || len(s.Catalog) == 0 || now.Sub(s.CatalogAt) >= catalogStale {
		return nil
	}
	out := make([]string, 0, len(s.Catalog))
	for m, en := range s.Catalog {
		if en {
			out = append(out, m)
		}
	}
	return out
}

// PublicModels are the names this account answers to: its own catalogue ids
// with every alias replaced by the base model it stands for. Several
// accounts serving one base model therefore publish one name between them,
// and the request is translated back per account by UpstreamModel.
// Nil while unknown or stale, like Models.
func (s *Snapshot) PublicModels(now time.Time) []string {
	ids := s.Models(now)
	if ids == nil {
		return nil
	}
	seen := make(map[string]bool, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		name := PublicModelID(id, s.Bases[id])
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// UpstreamModel is the id to send this account when a caller asks for
// model: the account's own id when it has one, else the alias standing for
// that base model, else model unchanged (so a pool that knows nothing about
// aliases is unaffected).
func (s *Snapshot) UpstreamModel(model string) string {
	if s == nil || model == "" {
		return model
	}
	if s.Catalog[model] {
		return model
	}
	if id := s.Aliases[model]; id != "" {
		return id
	}
	// Resolve short names from the stored base metadata as well, so older
	// snapshots work immediately without waiting for the next catalogue read.
	id := ""
	for upstream, base := range s.Bases {
		if s.Catalog[upstream] && PublicModelID(upstream, base) == model && (id == "" || upstream < id) {
			id = upstream
		}
	}
	if id != "" {
		return id
	}
	return model
}

func (s *Snapshot) clone() *Snapshot {
	if s == nil {
		return nil
	}
	c := *s
	c.OpenCode = s.OpenCode.Clone()
	c.Windows = append([]Window(nil), s.Windows...)
	if s.Catalog != nil {
		c.Catalog = make(map[string]bool, len(s.Catalog))
		for m, v := range s.Catalog {
			c.Catalog[m] = v
		}
	}
	if s.Aliases != nil {
		c.Aliases = make(map[string]string, len(s.Aliases))
		for m, v := range s.Aliases {
			c.Aliases[m] = v
		}
	}
	if s.Bases != nil {
		c.Bases = make(map[string]string, len(s.Bases))
		for m, v := range s.Bases {
			c.Bases[m] = v
		}
	}
	if s.Coverage != nil {
		c.Coverage = make(map[string]*Coverage, len(s.Coverage))
		for m, v := range s.Coverage {
			cv := *v
			c.Coverage[m] = &cv
		}
	}
	if s.Summary != nil {
		sum := *s.Summary
		c.Summary = &sum
	}
	if s.ModelCovered != nil {
		v := *s.ModelCovered
		c.ModelCovered = &v
	}
	if s.Ollama != nil {
		ob := *s.Ollama
		c.Ollama = &ob
	}
	if s.Perch != nil {
		pm := *s.Perch
		c.Perch = &pm
	}
	return &c
}

// Fresh: the plan windows were read within stale and the control key works.
func (s *Snapshot) Fresh(now time.Time, stale time.Duration) bool {
	return s != nil && !s.AuthFailed && !s.PlanAt.IsZero() && now.Sub(s.PlanAt) < stale
}

// EffectiveRemaining is a window's remaining credits, its whole limit once
// its reset has gone by.
func (w Window) EffectiveRemaining(now time.Time) int64 {
	if !w.ResetsAt.IsZero() && !now.Before(w.ResetsAt) {
		return w.Limit
	}
	return w.Remaining
}

// UsedShare is the share (0–100) of the fullest window used.
func (s *Snapshot) UsedShare(now time.Time) float64 {
	share := 0.0
	for _, w := range s.Windows {
		if w.Limit <= 0 {
			share = 100
			continue
		}
		used := float64(w.Limit-w.EffectiveRemaining(now)) / float64(w.Limit) * 100
		share = max(share, min(max(used, 0), 100))
	}
	return share
}

// PlanRoom: a Design Plan is active and every window has credits left.
func (s *Snapshot) PlanRoom(now time.Time) bool {
	if !s.Eligible || len(s.Windows) == 0 {
		return false
	}
	for _, w := range s.Windows {
		if w.EffectiveRemaining(now) <= 0 {
			return false
		}
	}
	return true
}

// WalletPositive: the wallet is above $0.
func (s *Snapshot) WalletPositive() bool {
	v, ok := ParseUSD(s.WalletUSD)
	return ok && v > 0
}

// ModelClass is "plan" for a Design Plan model, "package" for a 套餐模型
// (never drawn from the plan windows), "unknown" before preflight has said.
func (s *Snapshot) ModelClass(model string) string {
	if s == nil || model == "" {
		return "unknown"
	}
	c := s.Coverage[model]
	if c == nil || c.Covered == nil {
		return "unknown"
	}
	if *c.Covered {
		return "plan"
	}
	return "package"
}

func positiveCredits(v string) bool {
	f, ok := ParseUSD(v)
	return ok && f > 0
}

// SubscriptionCredits: 套餐模型额度 left, and whether the summary saying so
// is fresh.
func (s *Snapshot) SubscriptionCredits(now time.Time, stale time.Duration) (positive, known bool) {
	if s.Summary == nil || now.Sub(s.SummaryAt) >= stale {
		return false, false
	}
	return positiveCredits(s.Summary.SubscriptionCredits) || positiveCredits(s.Summary.SubscriptionCreditsUSD), true
}

// PaidPositive: money that costs real payment is left (wallet or recharge credits).
func (s *Snapshot) PaidPositive() bool {
	if s.WalletPositive() {
		return true
	}
	return s.Summary != nil && (positiveCredits(s.Summary.RechargeCredits) || positiveCredits(s.Summary.RechargeCreditsUSD))
}

// FreshCatalog: the model list was read within stale, which for a pool with
// no quota endpoint (Modal) is all there is to be fresh about.
func (s *Snapshot) FreshCatalog(now time.Time, stale time.Duration) bool {
	return s != nil && !s.AuthFailed && !s.CatalogAt.IsZero() && now.Sub(s.CatalogAt) < stale
}

// FreshOllama is the Ollama balance read within stale.
func (s *Snapshot) FreshOllama(now time.Time, stale time.Duration) bool {
	return s != nil && s.Ollama != nil && !s.AuthFailed && !s.BalanceAt.IsZero() && now.Sub(s.BalanceAt) < stale
}

// OllamaFunding says what pays for an Ollama account now: the included
// allowance first (its period's remaining credits, or the legacy session and
// weekly percentages), then purchased credits. source is included |
// purchased | unknown; exhausted is true only with positive evidence that
// both are empty, and never from a stale or failed read.
func (s *Snapshot) OllamaFunding(now time.Time, stale time.Duration) (source string, exhausted bool) {
	if !s.FreshOllama(now, stale) {
		return "unknown", false
	}
	b := s.Ollama
	// a period that has run out is a fresh allowance, not an old one
	if b.IncludedUSD != "" {
		if !b.PeriodUntil.IsZero() && !now.Before(b.PeriodUntil) {
			return "included", false
		}
		if positiveCredits(b.IncludedUSD) {
			return "included", false
		}
	}
	if b.SessionPercent != nil && (b.SessionResetsAt.IsZero() || now.Before(b.SessionResetsAt)) && *b.SessionPercent > 0 {
		return "included", false
	}
	if b.WeeklyPercent != nil && (b.WeeklyResetsAt.IsZero() || now.Before(b.WeeklyResetsAt)) && *b.WeeklyPercent > 0 {
		return "included", false
	}
	if positiveCredits(b.PurchasedUSD) {
		return "purchased", false
	}
	return "", true
}

// OllamaUsedShare is the share (0–100) of the fullest Ollama limit used: the
// included allowance, or the legacy session/weekly percentage spent.
func (s *Snapshot) OllamaUsedShare(now time.Time) float64 {
	b := s.Ollama
	if b == nil {
		return 0
	}
	share := 0.0
	if left, ok := ParseUSD(b.IncludedUSD); ok {
		if all, ok2 := ParseUSD(b.AllowanceUSD); ok2 && all > 0 {
			share = max(share, min(max((all-left)/all*100, 0), 100))
		}
	}
	if b.SessionPercent != nil {
		share = max(share, float64(100-*b.SessionPercent))
	}
	if b.WeeklyPercent != nil {
		share = max(share, float64(100-*b.WeeklyPercent))
	}
	return share
}

// OllamaRenewals are when an Ollama account's limits reset, the latest first.
func (s *Snapshot) OllamaRenewals(now time.Time) []time.Time {
	b := s.Ollama
	if b == nil {
		return nil
	}
	var out []time.Time
	for _, t := range []time.Time{b.PeriodUntil, b.WeeklyResetsAt, b.SessionResetsAt} {
		if t.After(now) {
			out = append(out, t)
		}
	}
	return out
}

// FundingFor says what would pay for model now, in the official order: Design
// Plan windows (Design Plan models only), then 套餐模型额度, then wallet /
// recharge. source is plan | subscription | wallet | unknown. exhausted is
// true only with positive evidence that nothing pays (or only the wallet,
// when allowWallet is off): a zero wallet alone never proves it.
func (s *Snapshot) FundingFor(now time.Time, stale time.Duration, model string, allowWallet bool) (source string, exhausted bool) {
	if s.ModelClass(model) != "package" && s.PlanRoom(now) {
		return "plan", false
	}
	sub, known := s.SubscriptionCredits(now, stale)
	if sub {
		return "subscription", false
	}
	if s.PaidPositive() {
		return "wallet", !allowWallet
	}
	if !known {
		return "unknown", false
	}
	return "", true
}

// ExhaustedUntil is when a used-up plan comes back: the latest reset among
// the windows with nothing left (zero when unknown).
func (s *Snapshot) ExhaustedUntil(now time.Time) time.Time {
	var t time.Time
	for _, w := range s.Windows {
		if w.EffectiveRemaining(now) <= 0 && w.ResetsAt.After(t) {
			t = w.ResetsAt
		}
	}
	return t
}

// Renewals are the windows' resets, the longest window first.
func (s *Snapshot) Renewals() []time.Time {
	out := make([]time.Time, 0, len(s.Windows))
	for _, w := range s.Windows {
		out = append(out, w.ResetsAt)
	}
	return out
}

// Pace is the longest window's share left per hour until it renews.
func (s *Snapshot) Pace(now time.Time) float64 {
	if len(s.Windows) == 0 || s.Windows[0].Limit <= 0 {
		return 0
	}
	w := s.Windows[0]
	left := float64(w.EffectiveRemaining(now)) / float64(w.Limit) * 100
	hours := max(w.ResetsAt.Sub(now).Hours(), 1)
	return left / hours
}

type Poller struct {
	Cfg           func() *config.Config
	HTTPFor       func(proxyURL string) *http.Client
	OnUpdate      func(accountID string)
	GoEgressCheck func(context.Context, *config.Config, config.Account) error
	// PerchFetch reads a Perch account's usage meter. It is a hook so this
	// package needs no knowledge of Perch's session handling (which lives in
	// internal/perchstore): given an account it returns the meter, or an
	// error whose text the console shows.
	PerchFetch func(ctx context.Context, a config.Account) (*PerchMeter, error)
	// PerchModels reads a Perch account's hosted-model catalogue. Perch has no
	// OpenAI-compatible /models, so this is the only way its accounts ever get
	// a catalog; without it every Perch account reads as "no models", and a
	// pool-wide /v1/models has nothing to publish.
	PerchModels func(ctx context.Context, a config.Account) (*Catalog, error)

	discovery opencode.Discovery
	workers   sync.WaitGroup
	mu        sync.Mutex
	snaps     map[string]*Snapshot
	running   map[string]chan struct{}
	pending   map[string]bool
	urgent    map[string]bool
	models    map[string]time.Time // models requested lately → last seen
	wake      chan struct{}
}

func NewPoller(cfg func() *config.Config, httpFor func(string) *http.Client) *Poller {
	return &Poller{Cfg: cfg, HTTPFor: httpFor, snaps: map[string]*Snapshot{}, running: map[string]chan struct{}{}, pending: map[string]bool{}, urgent: map[string]bool{},
		models: map[string]time.Time{}, wake: make(chan struct{}, 1)}
}

// NoteModel records a requested model so each account's coverage of it is
// read. A model seen for the first time is read on every account soon.
func (p *Poller) NoteModel(model string) {
	if model == "" || len(model) > 100 {
		return
	}
	p.mu.Lock()
	_, seen := p.models[model]
	p.models[model] = time.Now()
	if len(p.models) > 200 {
		oldest, at := "", time.Now()
		for m, t := range p.models {
			if t.Before(at) {
				oldest, at = m, t
			}
		}
		delete(p.models, oldest)
	}
	if !seen {
		for id, s := range p.snaps {
			if s.ModelClass(model) == "unknown" {
				p.rescheduleLocked(id, s)
			}
		}
	}
	p.mu.Unlock()
	if !seen {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

// coverageDue lists up to n recent models whose coverage s lacks or has stale.
func (p *Poller) coverageDue(s *Snapshot, n int, now time.Time) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var missing, stale []string
	for m, seen := range p.models {
		if now.Sub(seen) > 24*time.Hour {
			continue
		}
		c := s.Coverage[m]
		switch {
		case c == nil:
			missing = append(missing, m)
		case now.Sub(c.At) > coverageTTL:
			stale = append(stale, m)
		}
	}
	out := append(missing, stale...)
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func (p *Poller) Load(m map[string]*Snapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, s := range m {
		if s != nil {
			s.NextAt = time.Time{} // read again soon after a restart
			p.snaps[id] = s
		}
	}
}

func (p *Poller) Get(id string) *Snapshot {
	p.mu.Lock()
	s := p.snaps[id]
	p.mu.Unlock()
	return s.clone() // immutable snapshots can be cloned outside the admission hot-path lock
}

// View returns the stored snapshot itself, without a copy, for hot read-only
// paths such as account selection. A stored snapshot is never modified in
// place (every change stores a new one), so it stays valid; callers must not
// modify it.
func (p *Poller) View(id string) *Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snaps[id]
}

// rescheduleLocked stores a copy of s due at the next tick (caller holds p.mu).
func (p *Poller) rescheduleLocked(id string, s *Snapshot) {
	c := *s
	c.NextAt = time.Time{}
	p.snaps[id] = &c
}

func (p *Poller) All() map[string]*Snapshot {
	p.mu.Lock()
	out := make(map[string]*Snapshot, len(p.snaps))
	for id, s := range p.snaps {
		out[id] = s
	}
	p.mu.Unlock()
	// Large per-account catalogs must not block inference's quota.View while
	// metadata/admin readers deep-copy every account.
	for id, s := range out {
		out[id] = s.clone()
	}
	return out
}

// Forget drops an account's snapshot (deleted, or its control key changed).
func (p *Poller) Forget(id string) {
	p.mu.Lock()
	delete(p.snaps, id)
	delete(p.pending, id)
	delete(p.urgent, id)
	p.mu.Unlock()
}

// Refresh asks for id to be read at the next tick, unless it was read in
// the last 10 seconds.
func (p *Poller) Refresh(id string) {
	p.refresh(id, false)
}

// RefreshSession schedules a read after credentials have been installed, even
// when periodic polling is disabled.
func (p *Poller) RefreshSession(id string) {
	p.refresh(id, true)
}

func (p *Poller) refresh(id string, force bool) {
	p.mu.Lock()
	s := p.snaps[id]
	if s == nil {
		s = &Snapshot{}
		p.snaps[id] = s
	}
	if force || time.Since(s.LastOKAt) > 10*time.Second {
		if force {
			p.urgent[id] = true
		}
		if p.running[id] != nil {
			p.pending[id] = true
		} else {
			p.rescheduleLocked(id, s)
		}
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Poller) Run(ctx context.Context) {
	defer p.workers.Wait() // shutdown must not close logs/state beneath active refreshes
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.wake:
		}
		p.dispatch(ctx)
	}
}

func (p *Poller) dispatch(ctx context.Context) {
	cfg := p.Cfg()
	now := time.Now()
	p.mu.Lock()
	inFlight := len(p.running)
	var urgent, due []config.Account
	for _, a := range cfg.Accounts {
		if config.IsOpenCode(a.Provider) && !cfg.OpenCode.Enabled {
			continue
		}
		if a.RuntimeKey == "" || p.running[a.ID] != nil || (!cfg.Quota.Enabled && !p.urgent[a.ID]) {
			continue
		}
		s := p.snaps[a.ID]
		if p.urgent[a.ID] {
			urgent = append(urgent, a)
		} else if s == nil || !now.Before(s.NextAt) {
			due = append(due, a)
		}
	}
	for _, a := range append(urgent, due...) {
		if inFlight >= cfg.Quota.Concurrency {
			break
		}
		inFlight++
		p.running[a.ID] = make(chan struct{})
		delete(p.urgent, a.ID)
		p.workers.Add(1)
		go func(a config.Account) {
			defer p.workers.Done()
			c, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			_ = p.fetch(c, cfg, a)
			p.finish(a.ID)
		}(a)
	}
	p.mu.Unlock()
}

// RefreshNow reads one account synchronously (admin button).
func (p *Poller) RefreshNow(ctx context.Context, a config.Account) error {
	for {
		p.mu.Lock()
		if done := p.running[a.ID]; done != nil {
			p.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		p.running[a.ID] = make(chan struct{})
		p.mu.Unlock()
		break
	}
	defer p.finish(a.ID)
	if config.IsOpenCode(a.Provider) {
		return p.fetchOpenCodeForce(ctx, p.Cfg(), a, true)
	}
	if a.ControlKey == "" || a.Provider == config.ProviderModal || a.Provider == config.ProviderPerch {
		// only the catalogue (and, for Ollama, the balance) can be read
		p.mu.Lock()
		if s := p.snaps[a.ID]; s != nil {
			c := *s
			c.CatalogAt = time.Time{}
			c.BalanceAt = time.Time{}
			c.PerchAt = time.Time{}
			p.snaps[a.ID] = &c
		}
		p.mu.Unlock()
	}
	return p.fetch(ctx, p.Cfg(), a)
}

func (p *Poller) finish(id string) {
	p.mu.Lock()
	if p.pending[id] {
		if s := p.snaps[id]; s != nil {
			p.rescheduleLocked(id, s)
		}
		delete(p.pending, id)
	}
	close(p.running[id])
	delete(p.running, id)
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func backoff(failures int) time.Duration {
	d := time.Minute
	for i := 1; i < failures && d < 15*time.Minute; i++ {
		d *= 2
	}
	return min(d, 15*time.Minute)
}

// applyCatalog stores a fresh catalogue and the alias tables it implies:
// Bases collapses the account's ids onto the base model each stands for, so
// the pool can publish one name for several accounts' endpoints, and
// Aliases resolves that name back to this account's own id.
func applyCatalog(s *Snapshot, c *Catalog, now time.Time) {
	s.Catalog, s.CatalogAt, s.CatalogError = make(map[string]bool, len(c.Models)), now, ""
	s.Bases = nil
	for id, m := range c.Models {
		s.Catalog[id] = m.Enabled
		if m.Base == "" {
			continue
		}
		if s.Bases == nil {
			s.Bases = map[string]string{}
		}
		// the base model is itself a usable id unless another entry claims
		// the name; when several ids on one account share it, the first in
		// catalogue order wins so the choice is stable across reads
		if _, dup := s.Bases[m.Base]; !dup || id < s.Bases[m.Base] {
			s.Bases[id] = m.Base
			s.Bases[m.Base] = m.Base
		}
	}
	s.Aliases = nil
	for id, base := range s.Bases {
		if id == base {
			continue
		}
		if s.Aliases == nil {
			s.Aliases = map[string]string{}
		}
		if _, dup := s.Aliases[base]; !dup || id < s.Aliases[base] {
			s.Aliases[base] = id
		}
	}
	// an alias that the account also serves under its own name stays as is
	for base := range s.Aliases {
		if s.Catalog[base] {
			delete(s.Aliases, base)
		}
	}
}

func (p *Poller) fetch(ctx context.Context, cfg *config.Config, a config.Account) error {
	switch a.Provider {
	case config.ProviderOpenCodeZen, config.ProviderOpenCodeGo:
		return p.fetchOpenCode(ctx, cfg, a)
	case config.ProviderOllama:
		return p.fetchOllama(ctx, cfg, a)
	case config.ProviderModal:
		return p.fetchCatalogOnly(ctx, cfg, a)
	case config.ProviderPerch:
		return p.fetchPerch(ctx, cfg, a)
	}
	client := &Client{Base: cfg.Upstream.ControlBase, HTTP: p.HTTPFor(a.ProxyURL)}
	p.mu.Lock()
	prev := p.snaps[a.ID].clone()
	p.mu.Unlock()
	s := prev
	if s == nil {
		s = &Snapshot{}
	}
	now := time.Now()
	fail := func(err error) error {
		s.LastError, s.LastErrorAt = err.Error(), now
		s.AuthFailed = IsAuth(err)
		s.Failures++
		s.NextAt = now.Add(backoff(s.Failures))
		p.store(a.ID, s)
		return err
	}

	if catalogDue(s, now, a.RuntimeKey != "", false) {
		if cat, err := FetchCatalogFull(ctx, p.HTTPFor(a.ProxyURL), cfg.RuntimeBase(a), a.RuntimeKey); err == nil {
			applyCatalog(s, cat, now)
		} else {
			s.CatalogError = err.Error() // the last good catalogue stays
		}
	}
	if a.ControlKey == "" {
		s.NextAt = nextPoll(s, cfg, now, a.RuntimeKey != "")
		p.store(a.ID, s)
		return nil
	}

	ws := s.WorkspaceID
	if ws == "" || now.Sub(s.WorkspaceAt) > 10*time.Minute {
		w, err := client.Workspace(ctx, a.ControlKey)
		if err != nil {
			return fail(err)
		}
		if s.WorkspaceID != "" && s.WorkspaceID != w.WorkspaceID() {
			// a different workspace: what was read for the old one is void
			s = &Snapshot{Catalog: s.Catalog, CatalogAt: s.CatalogAt, CatalogError: s.CatalogError}
		}
		s.WorkspaceID, s.WorkspaceMemberID = w.WorkspaceID(), w.MemberID
		s.WorkspaceName, s.WorkspaceType, s.MembershipTier = w.Name, w.Type, w.PersonalMembershipTier
		s.WorkspaceAt = now
		ws = s.WorkspaceID
	}

	pf, err := client.Preflight(ctx, a.ControlKey, ws, "")
	if err != nil {
		return fail(err)
	}
	s.PlanAt, s.Eligible, s.Tier, s.Windows = now, pf.Eligible, pf.Tier, pf.Windows
	s.Funding, s.ModelCovered, s.PreflightBalanceUSD = pf.Funding, pf.ModelCovered, pf.BalanceUSD
	s.AuthFailed = false

	var partErr error
	if w, err := client.Wallet(ctx, a.ControlKey, ws); err == nil {
		s.WalletUSD, s.WalletSource, s.WalletAt = w.BalanceUSD, "wallet", now
	} else {
		partErr = err
		if _, ok := ParseUSD(pf.BalanceUSD); ok {
			s.WalletUSD, s.WalletSource, s.WalletAt = pf.BalanceUSD, "preflight", now
		}
	}
	if sum, err := client.Summary(ctx, a.ControlKey, ws); err == nil {
		s.Summary, s.SummaryAt = &sum, now
	} else if partErr == nil {
		partErr = err
	}
	// which requested models are Design Plan models on this tier
	for _, m := range p.coverageDue(s, 6, now) {
		mp, err := client.Preflight(ctx, a.ControlKey, ws, m)
		if err != nil {
			if partErr == nil {
				partErr = err
			}
			break
		}
		if s.Coverage == nil {
			s.Coverage = map[string]*Coverage{}
		}
		s.Coverage[m] = &Coverage{Covered: mp.ModelCovered, Funding: mp.Funding, At: now}
	}

	s.LastOKAt = now
	s.Failures = 0
	if partErr != nil {
		s.LastError, s.LastErrorAt = "partial: "+partErr.Error(), now
	} else {
		s.LastError = ""
	}
	s.NextAt = now.Add(time.Duration(cfg.Quota.IntervalSec) * time.Second)
	p.store(a.ID, s)
	return nil
}

// fetchPerch reads a Perch account's usage meter. It reads recorded spend
// through Perch's own RPC, so it consumes no model allowance; a failure here
// is Parked against the meter only and never against the session, which the
// Perch pool's own keeper maintains independently.
func (p *Poller) fetchPerch(ctx context.Context, cfg *config.Config, a config.Account) error {
	p.mu.Lock()
	prev := p.snaps[a.ID].clone()
	p.mu.Unlock()
	s := prev
	if s == nil {
		s = &Snapshot{}
	}
	now := time.Now()

	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	met, err := p.PerchFetch(c, a)
	cancel()
	if err != nil {
		s.PerchError, s.LastError, s.LastErrorAt = err.Error(), err.Error(), now
		s.Failures++
		s.NextAt = now.Add(backoff(s.Failures))
		p.store(a.ID, s)
		return err
	}
	s.Perch, s.PerchAt, s.PerchError = met, now, ""
	// The workspace is an identity, so it tracks the latest answer in both
	// directions: a workspace Perch no longer states is cleared rather than
	// kept from the previous read. A stale id would attribute a turn to a
	// workspace the account has since left, which is worse than the null block
	// the published client sends when it has none.
	if met != nil {
		if met.WorkspaceID != "" {
			s.WorkspaceID, s.WorkspaceAt = met.WorkspaceID, now
		} else {
			s.WorkspaceID, s.WorkspaceAt = "", time.Time{}
		}
	}
	s.LastOKAt, s.Failures = now, 0
	s.LastError = ""
	// The catalogue is read on its own schedule and failure: not having a
	// model list must not mark a healthy session as failing.
	if p.PerchModels != nil && catalogDue(s, now, true, false) {
		cc, cancel := context.WithTimeout(ctx, 30*time.Second)
		cat, cerr := p.PerchModels(cc, a)
		cancel()
		if cerr == nil && cat != nil && len(cat.Models) > 0 {
			applyCatalog(s, cat, now)
		} else if cerr != nil {
			s.CatalogError = cerr.Error() // the last good catalogue stays
		}
	}
	interval := max(time.Duration(cfg.Perch.QuotaIntervalSec)*time.Second, time.Minute)
	s.NextAt = now.Add(interval)
	p.store(a.ID, s)
	return nil
}

// ollamaBalanceEvery is the shortest gap between two balance reads of the
// same Ollama account: /api/balance allows 10 requests a minute per user,
// shared by every key that user has, so the reads stay well under that even
// with several accounts and a short quota interval.
const ollamaBalanceEvery = 30 * time.Second

// fetchOllama reads an Ollama account's balance (and, less often, its model
// list). The API key is both the inference key and the balance key, so there
// is no control key and a refused balance means the key itself is dead.
func (p *Poller) fetchOllama(ctx context.Context, cfg *config.Config, a config.Account) error {
	client := &Client{Base: cfg.ControlBase(a), HTTP: p.HTTPFor(a.ProxyURL)}
	p.mu.Lock()
	prev := p.snaps[a.ID].clone()
	p.mu.Unlock()
	s := prev
	if s == nil {
		s = &Snapshot{}
	}
	now := time.Now()
	fail := func(err error) error {
		s.LastError, s.LastErrorAt = err.Error(), now
		s.AuthFailed = IsAuth(err)
		s.Failures++
		s.NextAt = now.Add(backoff(s.Failures))
		p.store(a.ID, s)
		return err
	}

	if catalogDue(s, now, a.RuntimeKey != "", false) {
		if cat, err := FetchCatalogFull(ctx, p.HTTPFor(a.ProxyURL), cfg.RuntimeBase(a), a.RuntimeKey); err == nil {
			applyCatalog(s, cat, now)
		} else {
			s.CatalogError = err.Error() // the last good catalogue stays
		}
	}

	b, err := client.Balance(ctx, cfg.ControlBase(a), a.RuntimeKey)
	if err != nil {
		return fail(err)
	}
	s.Ollama, s.BalanceAt = &b, now
	s.AuthFailed = false
	s.LastOKAt, s.Failures, s.LastError = now, 0, ""
	interval := max(time.Duration(cfg.Quota.IntervalSec)*time.Second, ollamaBalanceEvery)
	s.NextAt = now.Add(interval)
	p.store(a.ID, s)
	return nil
}

// fetchCatalogOnly reads an account's model list and nothing else. Modal's
// Shared Endpoints have no quota endpoint: usage is billed per token from the
// workspace balance and only the dashboard shows it, so a refused balance
// can only be learned from the error itself (a 402 on a request). What the
// catalogue does tell us is which Endpoints this proxy token can reach: the
// model field is an Endpoint's hostname, so /v1/models is the set of models
// the key may call, and it costs nothing.
func (p *Poller) fetchCatalogOnly(ctx context.Context, cfg *config.Config, a config.Account) error {
	p.mu.Lock()
	prev := p.snaps[a.ID].clone()
	p.mu.Unlock()
	s := prev
	if s == nil {
		s = &Snapshot{}
	}
	now := time.Now()
	// The catalogue read is the only thing that can be asked of a Modal
	// account, and on Modal it is also the only free check of the token: the
	// inference host authenticates /v1/models (401 "invalid proxy auth
	// credentials") while its request paths answer "unknown inference model"
	// to a bad token as well, so a request can't tell a dead token from a
	// wrong model name.
	if catalogDue(s, now, a.RuntimeKey != "", true) {
		cat, err := FetchCatalogFull(ctx, p.HTTPFor(a.ProxyURL), cfg.RuntimeBase(a), a.RuntimeKey)
		switch {
		case err == nil:
			applyCatalog(s, cat, now)
			s.AuthFailed = false
		case IsAuth(err):
			s.AuthFailed, s.CatalogError = true, err.Error()
			s.LastError, s.LastErrorAt = err.Error(), now
			s.Failures++
			s.NextAt = now.Add(backoff(s.Failures))
			p.store(a.ID, s)
			return err
		default:
			s.CatalogError = err.Error() // the last good catalogue stays
		}
	}
	s.LastOKAt = now
	if !s.AuthFailed {
		s.LastError = ""
	}
	s.NextAt = nextPoll(s, cfg, now, a.RuntimeKey != "")
	p.store(a.ID, s)
	return nil
}

func (p *Poller) store(id string, s *Snapshot) { // an account deleted while it was read is not brought back
	if _, ok := p.Cfg().Account(id); !ok {
		return
	}
	p.mu.Lock()
	p.snaps[id] = s
	p.mu.Unlock()
	if p.OnUpdate != nil {
		p.OnUpdate(id)
	}
}
