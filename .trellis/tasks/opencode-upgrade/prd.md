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
