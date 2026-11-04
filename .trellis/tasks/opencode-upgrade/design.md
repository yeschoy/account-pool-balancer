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

## Current Go pool-gateway follow-up (planning)

- Preserve the original integration and limit this round to paid Go. Reuse config/account/pool/stats/logstore/admin/web; no second gateway or scheduler.
- Trace `client session/cache signals → scoped identity → atomic pool lease/binding → account-fixed transport → native upstream → usage/error classification → provider-scoped stats/log projections → existing console`.
- Separate conversation identity from cache preference: `previous_response_id` is strict state, explicit session/parent signals must not be overridden by a shared/changing cache hint. Preserve original native cache directives; their presence alone is not measured cache usage.
- Derived upstream Go session IDs must be stable and caller-isolated, with real client UA preserved. No official identity synthesis or prompt/tool shaping.
- Reuse existing per-account limits and fixed `ProxyURL` across all account network paths. Current proxy health proves reachability only. Public-IP observation/pinning and distinct-IP validation await the PRD G3 decision; do not confuse a fixed proxy URL with a static public IP.
- Extend existing immutable log/stat contracts and provider-scoped UI. Keep reported cache reads/writes and measurement coverage distinct from account-affinity hits; unknown usage is not a zero hit rate. Audit overview global binding/key/strategy fields before presenting them as Go totals.
- Existing request/error logs gain explicit local-block versus upstream-error semantics and source enums without raw session/cache secrets. Binding-busy, source changes and safety stops must be visible rather than silently moving conversations.
- Backend, UI, mocked upstream and configuration rollback tests are one acceptance chain. No live mutations/deployment or paid inference in planning. The product's distinct-IP decision and final plan review precede implementation.

## Rollback
No destructive schema migration. Disable OpenCode accounts/remove their keys and revert executable. Back up config/metadata before old executables rewrite additive fields. Existing provider configuration and stats remain intact.
