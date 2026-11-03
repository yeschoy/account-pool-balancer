package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"account-pool-balancer/internal/config"
	"account-pool-balancer/internal/perch"
	"account-pool-balancer/internal/perchdevice"
	"account-pool-balancer/internal/perchimport"
	"account-pool-balancer/internal/perchstore"
	"account-pool-balancer/internal/perchtelemetry"
	"account-pool-balancer/internal/quota"
)

// The Perch pool's runtime: a session keeper that renews Supabase tokens, a
// scheduler that keeps them fresh in the background, and the bindings between
// config accounts and stored sessions. Everything else (selection, cooldowns,
// health) is the shared pool package, exactly as the other pools use it.

// perchRuntime owns the keeper and its scheduler.
type perchRuntime struct {
	keeper         *perchstore.Keeper
	sched          *perchstore.Scheduler
	importMu       sync.Mutex
	pendingMu      sync.Mutex
	pendingImports map[string]bool

	// devices holds each account's sealed device identity (machine id, first
	// run / first prompt stamps). It is what makes one account's requests
	// look like one CLI install instead of a new machine every call.
	devices *perchdevice.Store
	// telemetry builds and (only when switched on) sends the two reports the
	// CLI emits. With egress off it audits them, redacted.
	telemetry *perchtelemetry.Reporter

	mu sync.Mutex
	// results caches the last health check per account, so the dashboard can
	// show "last checked" without re-checking.
	checks  map[string]perchCheck
	history []perchTrend

	// renew exchanges a refresh token for a new session. It is a field so a
	// test can observe the auto-signup path without reaching Supabase.
	renew func(context.Context, perch.Creds, string) (perch.Session, error)
}

// perchCheck is the last health verdict for one account.
type perchCheck struct {
	At     time.Time `json:"at"`
	Status string    `json:"status"`
	Detail string    `json:"detail,omitempty"`
	// Reachable: Perch answered at all (a network failure is neither).
	Reachable bool `json:"reachable"`
	banStatus int
}

type perchTrend struct {
	Day  string         `json:"day"`
	Seen map[string]int `json:"seen"`
}

// initPerch opens the sealed store and binds the config's accounts to their
// sessions. A failure to open it is fatal: running without it would serve an
// empty pool while looking healthy.
func (s *Server) initPerch() error {
	k, err := perchstore.Open(s.dataDir+"/perch-secrets.json", s.secret, s.perchSettings, s.logf)
	if err != nil {
		return err
	}
	s.perch = &perchRuntime{keeper: k, checks: map[string]perchCheck{}}
	// The device store is a second sealed file, under the same routing secret.
	// A failure here is not fatal for the pool: an account without stored
	// identity still works, it just looks new on every call.
	if dev, derr := perchdevice.Open(s.dataDir+"/perch-device.json", s.secret, s.logf); derr == nil {
		s.perch.devices = dev
	} else {
		s.logf("perch: device identity store unavailable: %v", derr)
	}
	s.perch.telemetry = perchtelemetry.New(s.telemetrySettings(s.cfg.Get()), auditSink{s}, s.logf)
	s.perch.sched = perchstore.NewScheduler(k)
	s.perch.sched.Concurrency = s.cfg.Get().Perch.RefreshConcurrency
	s.perch.sched.OnResult = s.onPerchRefresh
	s.quota.PerchFetch = s.fetchPerchMeter
	s.quota.PerchModels = s.fetchPerchModels
	s.rebindPerch(s.cfg.Get())
	// Delta cache sizing comes from settings, so a restart honours a value the
	// operator saved earlier instead of falling back to the package default.
	s.applyPerchDeltaLimits(s.cfg.Get().Perch)
	return nil
}

// perchSettings is the keeper's view of the console settings.
func (s *Server) perchSettings() perchstore.Config {
	p := s.cfg.Get().Perch
	def := perchstore.DefaultConfig()
	out := perchstore.Config{
		SupabaseKey:   strings.TrimSpace(p.SupabaseKey),
		AppURL:        p.AppURL,
		RefreshMargin: time.Duration(p.RefreshMarginMin) * time.Minute,
		Retries:       p.Retries,
		RetryBackoff:  time.Duration(p.RetryBackoffMs) * time.Millisecond,
		ProxyURL: func(accountID string) string {
			if a, ok := s.cfg.Get().Account(accountID); ok && a.Provider == config.ProviderPerch {
				return a.ProxyURL
			}
			return ""
		},
	}
	if out.SupabaseKey == "" {
		out.SupabaseKey = def.SupabaseKey
	}
	if out.AppURL == "" {
		out.AppURL = def.AppURL
	}
	if out.RefreshMargin <= 0 {
		out.RefreshMargin = def.RefreshMargin
	}
	return out
}

// rebindPerch re-points the keeper at the live accounts: an account id maps to
// its Supabase user id. Sessions of deleted accounts are kept on disk (a
// re-import under a new id would want them) but bound to nothing.
func (s *Server) rebindPerch(cfg *config.Config) {
	if s.perch == nil {
		return
	}
	live := map[string]string{}
	for _, a := range cfg.Accounts {
		if a.Provider == config.ProviderPerch && a.PerchUserID != "" {
			live[a.ID] = a.PerchUserID
		}
	}
	s.perch.keeper.Rebind(live)
	// Telemetry settings are read per report, but the reporter caches the
	// opt-out and egress switch, so a save has to reach it before the next
	// request rather than at the next restart.
	s.refreshTelemetrySettings(cfg)
	// a session on disk with no account: the account was deleted while the
	// session stayed. Forget it so a stale credential cannot linger.
	for _, uid := range s.perch.keeper.Users() {
		if _, ok := s.perch.keeper.ByUserID(uid); !ok {
			continue
		}
		bound := false
		for _, target := range live {
			if target == uid {
				bound = true
				break
			}
		}
		if !bound {
			s.perch.pendingMu.Lock()
			if !s.perch.pendingImports[uid] {
				s.perch.keeper.DropUser(uid)
			}
			s.perch.pendingMu.Unlock()
		}
	}
}

// perchAccounts lists the Perch accounts of a config.
func perchAccounts(cfg *config.Config) []config.Account {
	out := make([]config.Account, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		if a.Provider == config.ProviderPerch {
			out = append(out, a)
		}
	}
	return out
}

// onPerchRefresh records the outcome of a renewal: a refused refresh token is
// the only verdict that marks an account as needing a fresh sign-in, and
// turning that into a hard quarantine is what takes it out of service.
func (s *Server) onPerchRefresh(accountID string, res perchstore.RefreshResult) {
	if s.perch != nil && res.Revision != 0 && !s.perch.keeper.IsCurrentRefresh(accountID, res) {
		return
	}
	switch {
	case res.NeedLogin:
		s.markPerchNeedLogin(accountID, res.Err)
	case res.Err != nil:
		s.markPerchError(accountID, res.Err, false)
	case res.Renewed:
		s.markPerchHealthy(accountID)
	}
}

func (s *Server) markPerchNeedLogin(accountID string, err error) {
	msg := "刷新令牌被拒，需要重新登录"
	if err != nil {
		msg += "：" + err.Error()
	}
	_, uerr := s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == accountID {
				c.Accounts[i].PerchRefreshFailed = true
				c.Accounts[i].PerchLastError = msg
				c.Accounts[i].PerchLastErrorAt = time.Now()
				return nil
			}
		}
		return errNotPerchAccount
	})
	if uerr != nil {
		s.logf("perch: recording the refused refresh of %s failed: %v", accountID, uerr)
		return
	}
	// a refused refresh token is the one verdict that takes the account out
	// of service until somebody signs in again or a renewal succeeds
	if _, ok := s.cfg.Get().Account(accountID); ok {
		s.pool.Quarantine(accountID, http.StatusUnauthorized, msg, "perch-session-invalid")
	}
	s.logPerch(accountID, "session-expired", msg)
}

func (s *Server) markPerchError(accountID string, err error, definite bool) {
	msg := err.Error()
	_, _ = s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == accountID {
				if definite {
					c.Accounts[i].PerchRefreshFailed = true
				}
				c.Accounts[i].PerchLastError = msg
				c.Accounts[i].PerchLastErrorAt = time.Now()
				return nil
			}
		}
		return errNotPerchAccount
	})
	s.logPerch(accountID, "refresh-failed", msg)
}

func (s *Server) markPerchHealthy(accountID string) {
	_, err := s.cfg.Update(-1, func(c *config.Config) error {
		for i := range c.Accounts {
			if c.Accounts[i].ID == accountID {
				c.Accounts[i].PerchRefreshFailed = false
				c.Accounts[i].PerchLastError = ""
				c.Accounts[i].PerchLastErrorAt = time.Time{}
				return nil
			}
		}
		return errNotPerchAccount
	})
	if err != nil && !errors.Is(err, errNotPerchAccount) {
		s.logf("perch: clearing %s's error failed: %v", accountID, err)
		return
	}
	s.pool.RecoverQuarantine(accountID, "perch-session-invalid")
	s.logPerch(accountID, "refresh-ok", "")
}

var errNotPerchAccount = errors.New("not a perch account")

// fetchPerchMeter is the quota poller's hook: it renews the session first when
// it is due (so a stale token cannot be mistaken for an exhausted meter) and
// then reads the allowance. It never spends model allowance.
func (s *Server) fetchPerchMeter(ctx context.Context, a config.Account) (meter *quota.PerchMeter, resultErr error) {
	if s.perch == nil {
		return nil, errors.New("the perch pool is not running")
	}
	if _, ok := s.perch.keeper.Get(a.ID); !ok {
		// no session yet: nothing to read, and no error worth alarming over
		return nil, nil
	}
	started := time.Now()
	defer func() {
		s.logPerchDiagnostic(a.ID, "quota", perchFingerprintProxySource(a.ProxyURL), "", started, resultErr)
	}()
	// renew on the way in, exactly as the CLI does
	if res := s.perch.keeper.Refresh(ctx, a.ID, false); res.NeedLogin {
		return nil, fmt.Errorf("会话需要重新登录：%v", res.Err)
	}
	ctx = perch.WithProxy(ctx, a.ProxyURL)
	read := func() (*quota.PerchMeter, error) {
		sec, ok := s.perch.keeper.Get(a.ID)
		if !ok || sec.AccessToken == "" {
			return nil, errors.New("账号没有可用的 Access Token")
		}
		return s.readPerchMeter(ctx, sec, s.perchSettings())
	}
	out, err := read()
	if err != nil && perch.KindOf(err) == perch.KindAuth {
		// the token went stale between the check and the read: renew once and
		// try again rather than reporting a dead account
		if res := s.perch.keeper.Refresh(ctx, a.ID, true); res.Err == nil {
			out, err = read()
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fetchPerchModels reads one account's hosted-model catalogue. Perch only
// publishes it at /api/perchai/models (the web app's own picker source); the
// pool has no OpenAI-compatible /models to forward, so this is what gives the
// Perch pool something to answer GET /v1/models with.
//
// Every id is marked servable. Perch's `locked` flag is the app's paywall
// hint — it marks what the plan does not include in its picker — but the
// model-call proxy serves those ids anyway (an account whose entitlements say
// limits.allowed_models = ["*"] may name any of them), so hiding them would
// publish a fraction of what the pool actually answers.
func (s *Server) fetchPerchModels(ctx context.Context, a config.Account) (catalog *quota.Catalog, resultErr error) {
	if s.perch == nil {
		return nil, errors.New("the perch pool is not running")
	}
	sec, ok := s.perch.keeper.Get(a.ID)
	if !ok || sec.AccessToken == "" {
		return nil, errors.New("账号没有可用的 Access Token")
	}
	cfg := s.cfg.Get()
	started := time.Now()
	defer func() {
		s.logPerchDiagnostic(a.ID, "models", perchFingerprintProxySource(a.ProxyURL), cfg.Perch.AppURL, started, resultErr)
	}()
	cat, err := s.perchClient().Models(perch.WithProxy(ctx, a.ProxyURL), cfg.Perch.AppURL, sec.AccessToken, cfg.Perch.Lane)
	if err != nil {
		return nil, err
	}
	out := &quota.Catalog{Models: make(map[string]quota.CatalogModel, len(cat.Models))}
	for _, m := range cat.Models {
		if m.ID == "" {
			continue
		}
		out.Models[m.ID] = quota.CatalogModel{Enabled: true, Name: m.Name}
	}
	return out, nil
}

// readPerchMeter reads the account's plan and usage.
//
// The account route is authoritative and carries what the usage RPC cannot:
// the entitlement list (which states the monthly cap) and the credit balance.
// The RPC is read as well because it reports the day/week windows the account
// route does not always include; whichever answer actually arrives is used, so
// a failure of one does not cost the other.
func (s *Server) readPerchMeter(ctx context.Context, sec perchstore.Secrets, cfg perchstore.Config) (*quota.PerchMeter, error) {
	appURL := sec.AppURL
	if appURL == "" {
		appURL = cfg.AppURL
	}
	acct, accErr := s.perchClient().Account(ctx, appURL, sec.AccessToken)
	met, rpcErr := s.perchClient().Usage(ctx, sec.Creds(cfg.SupabaseKey, cfg.AppURL), sec.AccessToken)
	// The workspace identity comes from cli-context, the same route the CLI
	// resolves it through. It is a separate, best-effort read: not having it
	// costs the attribution block on a model call and nothing else, so a
	// failure here must never fail the meter read it rides along with.
	wsID := ""
	if wc, werr := s.perchClient().CliContext(ctx, appURL, sec.AccessToken); werr == nil {
		wsID = wc.WorkspaceID
	}

	switch {
	case accErr == nil:
		// the account route answered: it is the authority on caps
		if rpcErr == nil {
			acct.Meter = mergeMeters(acct.Meter, met)
		}
		// Perch's own free-tier figure, used only when the account states no
		// cap of its own. A failure here is not worth reporting: it changes
		// which estimate the console shows, nothing else.
		if acct.StarterCapPT == 0 && !acct.Unlimited && acct.MonthlyLimitPT == 0 {
			if usd, err := s.perchClient().StarterMonthlyUSD(ctx, appURL, sec.AccessToken); err == nil {
				acct.StarterMonthlyLimits(usd)
			}
		}
		out := accountToQuota(acct)
		out.WorkspaceID = wsID
		return out, nil
	case rpcErr == nil:
		// only the RPC answered: spend without a cap, which the console shows
		// as "unknown" rather than guessing
		out := meterToQuota(met)
		out.WorkspaceID = wsID
		return out, nil
	default:
		// Preserve an auth failure so the caller can attempt one refresh.
		if perch.KindOf(accErr) == perch.KindAuth {
			return nil, accErr
		}
		if perch.KindOf(rpcErr) == perch.KindAuth {
			return nil, rpcErr
		}
		return nil, accErr
	}
}

// mergeMeters keeps the RPC's window figures where the account route omitted
// them. Both report the same month; the account route's PT figures stay.
func mergeMeters(acct, rpc perch.Meter) perch.Meter {
	out := acct
	if out.DailyUSD == 0 && !out.DailyPTKnown {
		out.DailyUSD = rpc.DailyUSD
	}
	if out.WeeklyUSD == 0 && !out.WeeklyPTKnown {
		out.WeeklyUSD = rpc.WeeklyUSD
	}
	if out.DailyCalls == 0 {
		out.DailyCalls = rpc.DailyCalls
	}
	if out.WeeklyCalls == 0 {
		out.WeeklyCalls = rpc.WeeklyCalls
	}
	if out.WeeklyTokens == 0 {
		out.WeeklyTokens = rpc.WeeklyTokens
	}
	if out.MonthlyCalls == 0 {
		out.MonthlyCalls = rpc.MonthlyCalls
	}
	if out.MonthlyTokens == 0 {
		out.MonthlyTokens = rpc.MonthlyTokens
	}
	if out.MonthlyUSD == 0 {
		out.MonthlyUSD = rpc.MonthlyUSD
	}
	if out.MonthlyManualUSD == 0 {
		out.MonthlyManualUSD = rpc.MonthlyManualUSD
	}
	if out.PeriodStart == "" {
		out.PeriodStart = rpc.PeriodStart
	}
	if out.Raw == nil {
		out.Raw = rpc.Raw
	}
	return out
}

// accountToQuota turns the account answer into the pool's shape.
func accountToQuota(a perch.Account) *quota.PerchMeter {
	out := meterToQuota(a.Meter)
	out.Plan = a.PlanLabel
	out.PlanCode = a.PlanCode
	out.AllowancePT = a.MonthlyLimitPT
	out.AllowanceSource = string(a.MonthlySource)
	out.TierSelectionRequired = a.TierSelectionRequired
	out.ManualLimitPT = a.ManualLimitPT
	out.ManualLimitSource = string(a.ManualSource)
	out.CreditBalancePT = a.CreditBalancePT
	out.RoostUnlimited = a.RoostUnlimited
	out.Unlimited = a.Unlimited
	// An unlimited plan has no cap to state; leaving AllowancePT at zero keeps
	// "remaining" unknown rather than "fully spent".
	if a.Unlimited {
		out.AllowancePT, out.AllowanceSource = 0, string(perch.AllowanceNone)
	}
	return out
}

func (s *Server) perchClient() *perch.Client {
	if s.perch != nil {
		return s.perch.keeper.Client()
	}
	return perch.NewClient(nil)
}

// perchRenew renews one session, through the test hook when one is installed.
func (s *Server) perchRenew(ctx context.Context, creds perch.Creds, refreshToken string) (perch.Session, error) {
	if s.perch != nil && s.perch.renew != nil {
		return s.perch.renew(ctx, creds, refreshToken)
	}
	return s.perchClient().Renew(ctx, creds, refreshToken)
}

// meterToQuota converts Perch's answer into the quota package's shape.
func meterToQuota(m perch.Meter) *quota.PerchMeter {
	manualPT := perch.PtOf(m.MonthlyManualUSD)
	var usedPT int64
	switch {
	case m.MonthlyPTKnown && m.ManualPTKnown:
		// the account route states both in PT: no conversion, no guesswork
		manualPT = m.ManualPTExact
		usedPT = maxInt64(m.MonthlyPTExact-m.ManualPTExact, 0)
	case m.MonthlyPTKnown:
		usedPT = maxInt64(m.MonthlyPTExact-manualPT, 0)
	default:
		// The RPC reports monthly_usd, which Perch defines as the month's total
		// INCLUDING manual calls, so the plan's spend is the difference. It is
		// taken at zero as a floor — a manual figure larger than the total
		// means the two windows disagree, and a negative spend is meaningless.
		usedPT = maxInt64(perch.PtOf(m.MonthlyUSD)-manualPT, 0)
	}
	out := &quota.PerchMeter{
		UsedPT:        usedPT,
		ManualPT:      manualPT,
		DailyPT:       dailyPT(m),
		WeeklyPT:      weeklyPT(m),
		DailyCalls:    m.DailyCalls,
		WeeklyCalls:   m.WeeklyCalls,
		MonthlyCalls:  m.MonthlyCalls,
		WeeklyTokens:  m.WeeklyTokens,
		MonthlyTokens: m.MonthlyTokens,
		PeriodStart:   m.PeriodStart,
		ResetsAt:      m.ResetAt(),
	}
	if len(m.Raw) > 0 {
		out.Raw = m.Raw
		// Perch's meter answers only a browser cookie for the app's own
		// route; the RPC reports spend. A plan or a cap is taken when one is
		// actually named, never assumed.
		for _, key := range []string{"plan", "plan_name", "tier", "planName"} {
			if v, ok := m.Raw[key].(string); ok && strings.TrimSpace(v) != "" {
				out.Plan = strings.TrimSpace(v)
				break
			}
		}
		for _, key := range []string{"monthly_allowance_pt", "monthly_allowance_usd", "allowance_pt", "allowance_usd"} {
			if v, ok := numOf(m.Raw[key]); ok && v > 0 {
				if strings.HasSuffix(key, "_usd") {
					out.AllowancePT = perch.PtOf(v)
				} else {
					out.AllowancePT = int64(v)
				}
				break
			}
		}
	}
	return out
}

// dailyPT / weeklyPT prefer the exact PT figure when the account route gave one.
func dailyPT(m perch.Meter) int64 {
	if m.DailyPTKnown {
		return m.DailyPTExact
	}
	return perch.PtOf(m.DailyUSD)
}

func weeklyPT(m perch.Meter) int64 {
	if m.WeeklyPTKnown {
		return m.WeeklyPTExact
	}
	return perch.PtOf(m.WeeklyUSD)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func numOf(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		var f float64
		if _, err := fmt.Sscanf(x, "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// ---- account creation from an import -------------------------------------------

// perchImportPlan is what an import will do, computed before anything is
// written: which documents are new accounts, which merely refresh a stored
// session, and which are duplicates.
type perchImportPlan struct {
	Doc     perchimport.Parsed
	Source  string
	Account *config.Account // existing account this binds to, if any
	// Action: "create" | "session" | "duplicate"
	Action   string
	Reason   string
	ProxyURL string
}

// importPerch serializes planning and application across all import entry points.
// Config callbacks must not acquire importMu: cfg.Update calls them while
// holding its lock, and they access the keeper synchronously.
func (s *Server) importPerch(res perchimport.Result, group string, prepare func([]perchImportPlan) error) (perchimport.Summary, error) {
	s.perch.importMu.Lock()
	defer s.perch.importMu.Unlock()
	plans := s.planPerchImport(s.cfg.Get(), res)
	if prepare != nil {
		if err := prepare(plans); err != nil {
			return perchimport.Summary{}, err
		}
	}
	return s.applyPerchImport(plans, group), nil
}

// planPerchImport matches parsed documents against the live config.
//
// The rules:
//   - a Supabase user id the pool already knows is the same account;
//   - the same id twice in one upload: the first wins, the rest are duplicates;
//   - a document whose session is older than the stored one is a duplicate
//     that changes nothing (the store enforces it again on write).
func (s *Server) planPerchImport(cfg *config.Config, res perchimport.Result) []perchImportPlan {
	byUser := map[string]*config.Account{}
	for i := range cfg.Accounts {
		if uid := cfg.Accounts[i].PerchUserID; uid != "" {
			byUser[uid] = &cfg.Accounts[i]
		}
	}
	seen := map[string]string{}
	out := make([]perchImportPlan, 0, len(res.ParsedDocs))
	for i, doc := range res.ParsedDocs {
		plan := perchImportPlan{Doc: doc, Source: res.Sources[i]}
		claims := perch.TokensOf(doc.AccessToken)
		if sub, ok := claims["sub"].(string); ok && strings.TrimSpace(sub) != "" && sub != doc.UserID {
			plan.Action, plan.Reason = "invalid", "Session 身份与账号不一致"
			out = append(out, plan)
			continue
		}
		if first, dup := seen[doc.UserID]; dup {
			plan.Action = "duplicate"
			plan.Reason = "同一次导入中与 " + first + " 是同一个账号（Supabase User ID 相同）"
			out = append(out, plan)
			continue
		}
		seen[doc.UserID] = res.Sources[i]
		if a := byUser[doc.UserID]; a != nil {
			plan.Account = a
			plan.Action = "session"
			stored, has := s.perch.keeper.ByUserID(doc.UserID)
			switch {
			case !has:
				plan.Action = "session"
				plan.Reason = "补上缺失的 Session"
			case stored.RefreshToken == doc.RefreshToken && stored.AccessToken == doc.AccessToken:
				plan.Action = "duplicate"
				plan.Reason = "与已保存的 Session 完全相同"
			}
		} else {
			plan.Action = "create"
		}
		out = append(out, plan)
	}
	return out
}

// applyPerchImport stores sessions and creates accounts. It is deliberately
// per-document — one failure is recorded and the rest proceed — but the work
// is batched: sessions are sealed once and config.json is written once, so a
// thousand accounts import in one pass instead of a thousand.
func (s *Server) applyPerchImport(plans []perchImportPlan, group string) perchimport.Summary {
	if group == "" {
		group = config.GroupForProvider(config.ProviderPerch)
	}
	now := time.Now().UTC()
	outs := make([]perchimport.Outcome, len(plans))
	// workFor[i] is how plans[i] is stored; nil when it writes no session.
	type slot struct {
		sec perchstore.Secrets
		opt perchstore.PutOptions
		at  int // index into the batched write
	}
	workFor := make([]*slot, len(plans))
	newID := make([]string, len(plans))
	var batch []slot

	for i, plan := range plans {
		outs[i] = perchimport.Outcome{File: plan.Source, UserID: plan.Doc.UserID, Email: plan.Doc.Email}
		switch plan.Action {
		case "invalid":
			outs[i].Status, outs[i].Reason = "failed", plan.Reason
		case "duplicate":
			outs[i].Status, outs[i].Reason = "duplicate", plan.Reason
			if plan.Account != nil {
				outs[i].AccountID = plan.Account.ID
			}
		case "session":
			if plan.Account == nil {
				outs[i].Status, outs[i].Reason = "failed", "账号不一致"
				continue
			}
			outs[i].AccountID = plan.Account.ID
			// the record's own fetchedAt (derived from the token) decides
			// freshness; `now` is only the fallback for a file that states none
			w := slot{sec: plan.Doc.Secrets(), opt: perchstore.PutOptions{BindAs: plan.Account.ID, FetchedAt: now}, at: len(batch)}
			workFor[i] = &w
			batch = append(batch, w)
		case "create":
			newID[i] = config.NewID("acc_")
			w := slot{sec: plan.Doc.Secrets(), opt: perchstore.PutOptions{BindAs: newID[i], FetchedAt: now, AllowCreate: true}, at: len(batch)}
			workFor[i] = &w
			batch = append(batch, w)
		}
	}

	// Protect sessions awaiting their config account from unrelated config callbacks.
	s.perch.pendingMu.Lock()
	if s.perch.pendingImports == nil {
		s.perch.pendingImports = make(map[string]bool)
	}
	for i := range plans {
		if newID[i] != "" {
			s.perch.pendingImports[plans[i].Doc.UserID] = true
		}
	}
	s.perch.pendingMu.Unlock()
	defer func() {
		s.perch.pendingMu.Lock()
		for i := range plans {
			if newID[i] != "" {
				delete(s.perch.pendingImports, plans[i].Doc.UserID)
			}
		}
		s.perch.pendingMu.Unlock()
	}()

	// 1) seal every session once
	var stored []perchstore.PutResult
	if len(batch) > 0 {
		secs := make([]perchstore.Secrets, len(batch))
		opts := make([]perchstore.PutOptions, len(batch))
		for k := range batch {
			secs[k], opts[k] = batch[k].sec, batch[k].opt
		}
		res, err := s.perch.keeper.PutMany(secs, opts)
		if err != nil {
			for i := range plans {
				if workFor[i] != nil {
					outs[i].Status, outs[i].Reason = "failed", "保存 Session 失败："+err.Error()
				}
			}
			return perchimport.Summarize(len(plans), outs)
		}
		stored = res
	}

	// 2) record what the write settled, and which accounts are new
	var toCreate []int
	for i := range plans {
		if workFor[i] == nil {
			continue
		}
		res := stored[workFor[i].at]
		if res.Skipped {
			outs[i].Status, outs[i].Reason = "skipped", res.Reason
			continue
		}
		if newID[i] != "" {
			toCreate = append(toCreate, i)
		} else {
			outs[i].Status = "updated"
		}
	}

	// 3) create the new accounts in one config write
	if len(toCreate) > 0 {
		_, err := s.cfg.Update(-1, func(c *config.Config) error {
			known := map[string]bool{}
			for _, other := range c.Accounts {
				if other.PerchUserID != "" {
					known[other.PerchUserID] = true
				}
			}
			for _, i := range toCreate {
				doc := plans[i].Doc
				if known[doc.UserID] {
					continue
				}
				known[doc.UserID] = true
				c.Accounts = append(c.Accounts, config.Account{
					ID: newID[i], Provider: config.ProviderPerch, Name: accountName(doc), Email: doc.Email,
					UserID: doc.UserID, PerchUserID: doc.UserID,
					Group: group, Enabled: true, RuntimeKey: config.PerchRuntimeKey(doc.UserID), ProxyURL: plans[i].ProxyURL,
					CreatedAt: time.Now(),
				})
			}
			return nil
		})
		if err != nil {
			// nothing was created: the sessions just written must go too, or a
			// later import would be told they are already stored
			for _, i := range toCreate {
				s.perch.keeper.DropUser(plans[i].Doc.UserID)
				outs[i].Status, outs[i].Reason = "failed", "创建账号失败："+err.Error()
			}
		} else {
			for _, i := range toCreate {
				outs[i].Status, outs[i].AccountID = "created", newID[i]
			}
		}
	}

	// 4) let the pool pick the new sessions up
	for i := range outs {
		if outs[i].Status == "created" || outs[i].Status == "updated" {
			s.afterPerchSession(outs[i].AccountID)
			s.logPerch(outs[i].AccountID, "imported", outs[i].Email)
		}
	}
	return perchimport.Summarize(len(plans), outs)
}

// accountName is the console label an imported account gets.
func accountName(doc perchimport.Parsed) string {
	if doc.Email != "" {
		return doc.Email
	}
	name := doc.UserID
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

func (s *Server) afterPerchSession(accountID string) {
	a, ok := s.cfg.Get().Account(accountID)
	if !ok || a.Provider != config.ProviderPerch {
		return
	}
	if a.PerchRefreshFailed {
		s.markPerchHealthy(accountID)
	}
	s.quota.RefreshSession(accountID)
	s.perch.sched.Wake()
}

// ---- health checks --------------------------------------------------------------

// checkPerchAccount asks Perch who the account is, using the cheapest call
// that proves the session works: the GoTrue user read. It spends no model
// allowance. The token is renewed first when it is due.
func (s *Server) checkPerchAccount(ctx context.Context, a config.Account) perchCheck {
	res := perchCheck{At: time.Now()}
	sec, ok := s.perch.keeper.Get(a.ID)
	if !ok {
		res.Status, res.Detail = "need-login", "没有保存的 Session"
		return res
	}
	cfg := s.perchSettings()
	if rr := s.perch.keeper.Refresh(ctx, a.ID, false); rr.NeedLogin {
		res.Status, res.Detail, res.Reachable = "need-login", rr.Err.Error(), true
		return res
	}
	if sec, ok = s.perch.keeper.Get(a.ID); !ok || sec.AccessToken == "" {
		res.Status, res.Detail = "need-login", "Session 中没有 Access Token"
		return res
	}
	ctx = perch.WithProxy(ctx, a.ProxyURL)
	u, err := s.perchClient().User(ctx, sec.Creds(cfg.SupabaseKey, cfg.AppURL), sec.AccessToken)
	if err != nil {
		if status := perchUserBannedStatus(err); status != 0 {
			res.Status, res.Detail, res.Reachable, res.banStatus = "banned", err.Error(), true, status
			return res
		}
		switch perch.KindOf(err) {
		case perch.KindAuth:
			// the token is gone but the refresh may still work
			if rr := s.perch.keeper.Refresh(ctx, a.ID, true); rr.Err == nil {
				if sec2, ok := s.perch.keeper.Get(a.ID); ok {
					if u2, err2 := s.perchClient().User(ctx, sec2.Creds(cfg.SupabaseKey, cfg.AppURL), sec2.AccessToken); err2 == nil {
						u, err = u2, nil
					} else if status := perchUserBannedStatus(err2); status != 0 {
						res.Status, res.Detail, res.Reachable, res.banStatus = "banned", err2.Error(), true, status
						return res
					}
				}
			}
		}
		if err != nil {
			res.Status = "error"
			res.Detail = err.Error()
			res.Reachable = perch.KindOf(err) != perch.KindTransient
			return res
		}
	}
	if u.UserID != "" && u.UserID != a.PerchUserID {
		res.Status = "error"
		res.Detail = fmt.Sprintf("Session 属于 Supabase 用户 %s，不是本账号的 %s", u.UserID, a.PerchUserID)
		res.Reachable = true
		return res
	}
	// The account route answers 200 for a banned account, so a check that stops
	// here reports a banned account as healthy — which is exactly how a whole
	// pool of banned accounts looked fine. The plan gate is the one read that
	// states the ban plainly.
	if ps, perr := s.perchClient().PlanStatus(ctx, cfg.AppURL, sec.AccessToken); perr == nil {
		switch {
		case ps.Banned():
			res.Status, res.Detail, res.Reachable, res.banStatus = "banned", ps.Detail(), true, ps.Status
			return res
		case ps.PlanRequired:
			// Not a ban: the account has simply never chosen a plan, so no
			// hosted-model turn can start. Say so instead of "ok".
			res.Status, res.Detail, res.Reachable = "no-plan", "账号尚未选择套餐（待选套餐），模型调用会被拒绝", true
			return res
		case ps.ErrorCode != "":
			res.Status, res.Detail, res.Reachable = "error", ps.Detail(), true
			return res
		}
	}

	res.Status, res.Reachable = "ok", true
	if u.Email != "" && u.Email != a.Email {
		// Perch is the authority on the address; keep the console in step
		_, _ = s.cfg.Update(-1, func(c *config.Config) error {
			for i := range c.Accounts {
				if c.Accounts[i].ID == a.ID {
					c.Accounts[i].Email = u.Email
				}
			}
			return nil
		})
	}
	return res
}

func perchUserBannedStatus(err error) int {
	var pe *perch.Error
	if errors.As(err, &pe) && (pe.Status == http.StatusUnauthorized || pe.Status == http.StatusForbidden) && strings.EqualFold(pe.JSONCode, "banned") {
		return pe.Status
	}
	return 0
}

// recordCheck caches a verdict and folds it into the day's trend.
func (s *Server) recordCheck(accountID string, c perchCheck) {
	if s.perch == nil {
		return
	}
	s.perch.mu.Lock()
	defer s.perch.mu.Unlock()
	s.perch.checks[accountID] = c
	day := c.At.Format("2006-01-02")
	for i := range s.perch.history {
		if s.perch.history[i].Day == day {
			s.perch.history[i].Seen[c.Status]++
			return
		}
	}
	h := perchTrend{Day: day, Seen: map[string]int{c.Status: 1}}
	s.perch.history = append(s.perch.history, h)
	if len(s.perch.history) > 90 {
		s.perch.history = s.perch.history[len(s.perch.history)-90:]
	}
}

// checkPerchBatch runs checks with bounded concurrency. One account failing
// never stops the others; each verdict is recorded on its own.
func (s *Server) checkPerchBatch(ctx context.Context, accounts []config.Account, onResult func(string, perchCheck)) {
	limit := max(s.cfg.Get().Perch.CheckConcurrency, 1)
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, a := range accounts {
		a := a
		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			continue
		}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			c := s.checkPerchAccount(ctx, a)
			s.recordCheck(a.ID, c)
			s.applyCheck(a, c)
			if onResult != nil {
				onResult(a.ID, c)
			}
		}()
	}
	wg.Wait()
}

// applyCheck turns a verdict into pool state: a dead session is quarantined
// (out of service until an operator acts or a renewal succeeds), a good one
// recovers a quarantined account, and a network failure changes nothing.
func (s *Server) applyCheck(a config.Account, c perchCheck) {
	switch c.Status {
	case "banned":
		s.pool.Quarantine(a.ID, c.banStatus, "perch-banned", "perch-banned")
		if err := s.saveMetadata(true); err != nil {
			s.logf("failed to persist banned quarantine for %s: %v", a.ID, err)
		}
	case "need-login":
		s.markPerchNeedLogin(a.ID, errors.New(c.Detail))
	case "ok":
		if a.PerchRefreshFailed || a.PerchLastError != "" {
			s.markPerchHealthy(a.ID)
		}
	case "error":
		// Reachable=false means the network, not the account
		if !c.Reachable {
			return
		}
		s.markPerchError(a.ID, errors.New(c.Detail), false)
	}
}

// RunPerch starts the Perch loops.
func (s *Server) RunPerch(ctx context.Context) {
	if s.perch == nil {
		return
	}
	tick := time.Duration(s.cfg.Get().Perch.RefreshTickSec) * time.Second
	if tick <= 0 {
		tick = 30 * time.Second
	}
	s.perch.sched.Tick = tick
	go s.perch.sched.Run(ctx)

	// health checks run on their own, slower clock
	go func() {
		period := time.Duration(s.cfg.Get().Perch.CheckIntervalSec) * time.Second
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				accts := perchAccounts(s.cfg.Get())
				if len(accts) > 0 {
					s.checkPerchBatch(ctx, accts, nil)
				}
			}
		}
	}()
}

// stopPerch flushes the sealed store.
func (s *Server) stopPerch() {
	if s.perch == nil {
		return
	}
	if err := s.perch.keeper.Flush(); err != nil {
		s.logf("perch: flushing the session store failed: %v", err)
	}
}

// sortedCopy keeps console output stable.
func sortedCopy(in []config.Account) []config.Account {
	out := append([]config.Account(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
