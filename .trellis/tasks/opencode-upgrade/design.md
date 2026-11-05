# Design

- `config`: two provider IDs `opencode-zen`, `opencode-go`, preserving version 1. Declare OpenCode explicitly since credential format overlaps other providers. Tier bases include `/v1` for existing joining semantics. Add a separately validated OpenCode settings object and per-tier rules.
- `internal/opencode`: independent protocol/capability/usage HTTP contracts only; no account scheduler, database or alternate admin service. Own machine catalog parsing and bounded conservative protocol bridge.
- `quota.Poller`: persist tier metadata, account-local catalog and Go usage in existing Snapshot. Background reads use account proxy, preserve last success on failure, reject late writes after credential/base changes. Catalog is advertisement evidence, not proof of paid inference authorization.
- `pool`: unchanged selection/admission mutex; strict stateful continuation binding as an opt-in Request property. Downstream key scope included in OpenCode identity. Missing stateful binding fails closed rather than sending to a random account.
- `server`: small OpenCode branch in existing attempt with protocol-selected auth/path, no artificial client fingerprint or gate prompts. Existing native relay reused; compatibility converter bounded and explicit. Configuration changes invalidate model snapshots and transport limits. No proxy-to-direct fallback.
- `web`: extend existing pool tabs/forms/settings and quota renderer; never replace existing assets.
- Native protocols preserve all bytes. Converted protocols support a documented subset; non-representable fields/blocks return 400 instead of silently dropping. Encrypted thinking and previous_response_id cannot be converted.

## Implemented refinements

- Confirmed public catalog now declares provider-family inference paths. Add optional per-tier protocol Base maps, never auto-dial model.provider.api from public data. Catalog/usage bases stay separate. Published model counts exclude unknown protocols; account UI shows directory versus routable counts.
- Native Responses bindings checkpoint with credential/proxy/Base/source hashes. Ordinary sessions stay in memory. An explicitly authorized one-hop opposite-tier fallback keeps original key scope and original total attempt budget, including already-pinned fallback sessions.
- Compatibility SSE is deliberately buffered, capped at 16 MiB, and off by default. Explicit error envelopes before upstream content reuse existing failure rules; failures after upstream content do not replay. Missing usage is not fabricated as zero. Real-time cross-protocol streaming remains unfinished.
- Recognized supplier policy gates are non-overridable stop conditions; do not walk keys/exits even when a custom rule requests cooldown/failover.
- Request admission/drain context cancels in-flight work before final persistence/log close. Poller worker shutdown is joined; immutable quota copies are made outside the hot-path mutex.
- No live keyed upstream calls or five-client end-to-end certifications. Detailed public-source observation and remaining scope are in research/public-contract.md and docs/opencode.md.

## Current Go pool-gateway follow-up (locally implemented; not deployed)

- Preserve the original integration and limit this round to paid Go. Reuse config/account/pool/stats/logstore/admin/web; no second gateway or scheduler.
- Trace `client session/cache signals → scoped identity → atomic pool lease/binding → account-fixed transport → native upstream → usage/error classification → provider-scoped stats/log projections → existing console`.
- Separate conversation identity from cache preference: `previous_response_id` is strict state, explicit session/parent signals must not be overridden by a shared/changing cache hint. Preserve original native cache directives; their presence alone is not measured cache usage.
- Derived upstream Go session IDs must be stable and caller-isolated, with real client UA preserved. No official identity synthesis or prompt/tool shaping.
- Reuse existing per-account limits and fixed `ProxyURL` across all account network paths. Current proxy health proves reachability only. The user approved shared stable IPs. Add optional per-account expected-IP pinning, an admin-configured no-credential probe and bounded freshness/single-flight checks; inference and account metadata are blocked on a required pin-check failure. Empty pin is explicitly unverified. Do not confuse a fixed proxy URL with a static public IP.
- Extend existing immutable log/stat contracts and provider-scoped UI. Keep reported cache reads/writes and measurement coverage distinct from account-affinity hits; unknown usage is not a zero hit rate. Audit overview global binding/key/strategy fields before presenting them as Go totals.
- Existing request/error logs gain explicit local-block versus upstream-error semantics and source enums without raw session/cache secrets. Binding-busy, source changes and safety stops must be visible rather than silently moving conversations.
- Backend, UI, mocked upstream and configuration rollback tests are one acceptance chain. No live mutations/deployment or paid inference in planning. The user approved this plan and shared stable egress; implementation is authorized locally only.

### Local Go implementation details

- Go-only identity removes cache hints from primary identity extraction. The pool accepts a separate cache preference with source validation, first-owner retention on busy fallback and no eviction of session/response state. Existing primary lease admission remains atomic.
- Responses checkpoint adds an optional validated opaque session; legacy records without it remain readable. Cache preferences stay in memory and do not masquerade as response bindings.
- Optional account `egressIp` plus Go `goEgressCheckUrl/goEgressCheckSec` gates pinned inference and metadata with no-credential, no-redirect, single-flight fixed-transport checks. Empty pin stays configured-only. Probe failure marks old quota stale without inventing zero.
- Go `goPolicyCooldownSec` uses existing persisted pool cooldowns, including known shared-route/IP peers on explicit network abuse. Post-commit stream errors are logged/paused but never replayed.
- Existing stats exports gain measured cache-write/input/output/duration coverage, affinity and timing counters. Go overview/UI use provider-scoped bindings/keys/strategy and distinguish configured route from observed IP. Existing logs add source enums/observed IP and redact native identity metadata in optional detail capture.
- Operations, defaults, Mock/browser results and limitations are documented in docs/opencode-go-gateway.md. Production and real keyed client/cache effectiveness are not validated by this local delivery.

## Recent-hour balancing design

- Add `least-requests-1h` as an opt-in pure `routes.Order` case; preserve priority grouping, eligibility and session/cache preference in the existing atomic pool. Recent sent count + pending unsent leases is the primary score, then active count, prior fresh-selection sequence and rotating ties; cumulative lifetime selections and quota/token metrics are not this strategy's score.
- Add bounded sparse minute buckets to existing `Counter`, and attach its reference to each Lease. Commit once at MarkSent for both fresh and sticky sends, even with unlimited RPM. Pending changes to committed load atomically and cancels without fake usage. Reset/deletion detaches old counter references so late traces cannot revive them.
- Keep counter version1, optional JSON fields, 5-second/normal-shutdown persistence. Validate <=60 ordered nonnegative minutes/positive counts and overflow; clone mutable slices and discard expired/future entries at restore/export. Legacy snapshots begin empty; no completed-request stats are repurposed as send history.
- Existing WebUI gets an optional strategy description and Go hourly/pending count hint; browser fake-key API readback and a true Mock restart verify strategy isolation and restored counts. No live configuration changes or upstream paid inference.

### Mandatory eligibility clarification

The recent-hour strategy always applies the configured minimum health, even with
optional healthFilter=false. Reuse one pool health predicate for admission and
preview, before session/cache preference; preserve the old switch for other
strategies. Existing fresh-window exhaustion and quarantine/cooldown/permissions
remain authoritative. Do not fabricate exhaustion from stale/unknown data or
infer unhealthy from fewer than20 samples. No eligible account means error;
strict continuation and policy-bound sessions still cannot migrate.

## Rollback
No destructive schema migration. Disable OpenCode accounts/remove their keys and revert executable. Back up config/metadata before old executables rewrite additive fields. Existing provider configuration and stats remain intact.
