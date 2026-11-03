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

## Rollback
No destructive schema migration. Disable OpenCode accounts/remove their keys and revert executable. Back up config/metadata before old executables rewrite additive fields. Existing provider configuration and stats remain intact.
