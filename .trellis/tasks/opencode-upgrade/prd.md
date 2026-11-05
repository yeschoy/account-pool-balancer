# OpenCode incremental upgrade

## Scope
Integrate authenticated OpenCode Zen and Go as separate providers in the existing account/config/group/key/pool/admin pipeline. Preserve existing providers and local-file persistence. Three client protocols must use native pass-through when possible; unsupported translations must fail explicitly. Never copy GPL reference code or implement free-tier impersonation/limit avoidance.

## Acceptance
- Explicit provider selection for opaque keys; no sk-prefix misclassification; separate groups and import keys.
- Account-local model listing intersected with tier capability metadata. Unknown quota is not zero. Go usage reports rolling/weekly/monthly percent windows, not money.
- Native JSON/SSE preserves tools and opaque fields. Compatibility conversion is explicit and rejects unrepresentable data. Stateful Responses never silently moves accounts.
- Reuse existing atomic lease, limits, logs, UI, config validation/reload, and persistent snapshots.
- Mock coverage and gofmt, go test, race, vet, build; live validation reported separately.
- Document real completion boundaries, license, upgrade/rollback and remaining requirements; no unsupported completion claims.

## Authorization
User explicitly requested architecture audit followed immediately by implementation and tests. This task stores planning locally because the inherited Trellis scripts/specs live outside this Git repository. No change to sibling projects or live credentials.

## Current follow-up: Go subscription pool gateway

The original integration above is historical scope. The current round is limited to paid OpenCode Go; Zen, anonymous/free models and Gemini/Google adaptation remain out of scope. The user clarified the product is an account-pool management gateway, not merely a direct Go client: Codex and Claude Code requests should use their session/cache signals to improve same-account affinity, with stable account egress, configurable concurrency, an overview and request/error logs. Preserve existing other-provider behavior; do not build a second scheduler or log system.

### Evidence at the planning baseline (subsequent local implementation in docs/opencode-go-gateway.md)

- `internal/server/identity.go:bodyKeys/identity` currently places body `prompt_cache_key` ahead of explicit session headers. Local HMAC affinity does not automatically supply an upstream Go session header.
- `internal/pool/pool.go` already provides atomic concurrency/RPM leases and prevents OpenCode binding-busy overflow; reuse it.
- `internal/server/admin.go:accountInput` and `web/app.js:editAccount` already expose per-account proxy/concurrency/RPM.
- `internal/server/opencode_proxy.go` tracks proxy reachability, not the actual observed public IP; a fixed proxy URL cannot prove a fixed egress IP.
- `internal/server/proxy.go:requestLog/errorLog` and `internal/stats/stats.go` already record provider/account/cache usage; extend these contracts rather than duplicate them.
- `internal/server/admin.go:apiOverview` still reports some global binding/key/strategy values; `web/app.js:loadOverview` needs Go-specific quota/help and metric semantics.

### Requirements and acceptance

- **G1 Session and cache affinity:** recognize supported Codex/Claude Code explicit session and parent identifiers, plus cache hints. Stateful response binding remains strict. Explicit conversation identity outranks shared cache hints; cache affinity must not merge independent server-side response chains. Preserve legitimate `prompt_cache_key`, `cache_control` and relevant native headers/body fields. Scope derived identities by downstream key/group; never use a per-request ID as a conversation. Mock cases must cover conflicting/changing hints, parent/child tasks and identical hints from different callers.
- **G2 Account continuity:** reuse the pool for same-conversation/same-account preference; account-busy waits are bounded and end in a local capacity response rather than silent overflow. Credential/proxy/base/catalog changes invalidate stale state. Log every permitted reassignment and its reason; never promise same-account routing guarantees an upstream cache hit.
- **G3 Stable per-account egress:** inference, metadata, quota and diagnostics use the account's configured fixed egress. No automatic proxy rotation/direct fallback on failure or policy refusal. Distinguish configured route from observed public IP; do not claim dynamic proxies have fixed IPs. Accounts may share one stable public IP; each account keeps its configured route and must not auto-rotate.
- **G4 Configurable limits:** reuse account concurrency/RPM with independent Go routing/wait settings. Concurrency reservation/release, cancellation and limits must remain atomic and testable; metadata/quota reads do not consume inference RPM or selections. No unapproved production default changes.
- **G5 Go overview:** show Go-only account availability, inflight/limits, requests/success/failure/cancellation, input/output/cache-read/cache-write where reported, cache measurement coverage, binding/affinity, errors/cooldowns, fixed-egress state and subscription window freshness. Do not sum shared subscription percentages or present price-table estimates as subscription billing. Provider-scoped counts and strategy must not leak global totals.
- **G6 Request/error logs:** extend existing logs with client family, identity/cache signal source, selected account, binding/cache-affinity result, model/protocol, egress label, timing, reported cache usage and known/unknown status. Separate local capacity/validation/egress/policy blocks from actual upstream errors; retain upstream/downstream status, rule, Retry-After, attempts and request correlation. Never log raw API keys, proxy credentials, raw session/cache identifiers or full conversation content by default.
- **G7 Risk and retry controls:** stable honest client identity and account egress, controlled load, policy stop/alert, and no replay after uncertain submission or committed output. No official-client impersonation, injected official tools/prompts, random identities or account/IP rotation to evade provider restrictions.

### Recent-hour request balancing follow-up

Add optional `least-requests-1h`: primary session and secondary cache affinity keep precedence; unbound selections rank eligible accounts within existing priority groups by gateway-forwarded attempts sent in the latest 60 minute buckets plus pending unsent leases. This is a minute-resolution rolling request-count LFU, not lifetime selections, quota percentages, token decay or exact last-use LRU. Count sticky hits and failed-but-sent attempts once at MarkSent; exclude diagnostics, metadata, local rejects and cancelled-before-send attempts. Persist sparse bounded buckets in the existing version-1 selection counters, deep-copy and validate on import/export, retain ordinary strategy/default behavior and no new scheduler/database. Existing concurrency/RPM, policy stops and strict stateful continuity stay authoritative. Validate clock expiry, restart, pending-lease contention and actual Go UI/API independently; no production configuration changes or paid inference.

### Out of scope and validation boundaries

No Zen/free/cross-tier fallback, Google protocol work, new scheduler/database, unrelated Token Harbor work, live deployment or paid inference without separate authorization. Existing native protocol/stateful safeguards must remain. Validate backend data contracts and the existing UI together using fake keys/local mocks; real cache effectiveness and provider permissions require separately authorized live acceptance.

### Approved decisions and implementation authorization

The user approved the proposed plan and stable-egress sharing: accounts may share a fixed public IP, with per-account binding and no automatic rotation. Implement locally with mocks and the existing UI; no deployment or paid inference is authorized. Add optional expected-IP pinning with an administrator-configured credential-free IP-check endpoint and bounded validation freshness. Empty pin preserves existing fixed-route behavior and must be labeled unverified, not falsely claimed as a verified static IP. Static public-IP assurance ultimately requires a static network/proxy; checks detect drift but cannot make a rotating proxy static. Existing other-provider and Zen behavior must remain unchanged.
