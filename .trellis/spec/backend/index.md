# Account Pool Backend Contracts

**Before adding or completing any provider, read [Provider onboarding gates](provider-onboarding.md).** Account/quota/routing/rules/logs/test/UI persistence and isolation are mandatory, not optional follow-up work.

Read [OpenDesign diagnostics contracts](opendesign.md) before changing its
quota reads, error evidence, refresh API or account status projections.
Read [OpenCode integration contracts](opencode.md) before modifying config,
quota, pool, proxy, identity or admin code in this repository.
Read [Token Harbor contracts](tokenharbor.md) for its native-key integration,
cache accounting, strict continuation and write-only settings boundary.
Read [Explicit saved API-key reads](tokenharbor-keys.md) before changing
TokenHarbor Key reveal/copy or its admin credential boundary.
Read [Registration profiles and workers](tokenharbor-registration-identity.md)
for saved browser identities, completion order, task chronology and1–5 workers.
Read [Model Studio contracts](modelstudio.md) before changing the 阿里云百炼 pool's
plan×region host resolution, protocol allowlist, unknown-Credits projection or
its independent error/retry rules.
Read [Mistral quota and settings](mistral-quota.md) before changing Mistral budget reads, refresh/status projections, polling, routing or settings isolation.
Read [Mistral capacity and resource admission](mistral-capacity.md) for independent session LRU, immutable indexes, hot-owner checks, byte/wait/deadline/Transport protections and sparse stats/log budgets. `docs/mistral-capacity.md` preserves pre-change research and separately reports implemented/local-tested changes; no deployment or real300M TPM certification is implied. Do not equate mock usage, larger binding limits or HTTP2 connection counts with verified production TPM.
Read [Mistral adaptation](mistral.md) before changing Mistral cache hints,
assistant reasoning normalization or user-authorized admin Key/proxy display.
Read [Mistral server-only layout replay](mistral-layout.md) before changing its bounded text/thinking layout metadata, exact-match restoration, checkpoint/expiry/ambiguity or response-capture integration. Logical-order recovery does not mean original JSON/network bytes or permanent cache hits.
Read [Mistral Responses completion](mistral-responses.md) before changing native
Responses SSE terminal handling, EOF classification or no-replay guarantees.
Read [Rolling cache statistics](cache-stats.md) before changing cache-hit rates,
measured-token counters or administrator resets.
Read [Business log retention](logs.md) before changing log cleanup, recovery,
query cutoffs or live-tail retention.

Quality gate: gofmt; go test ./...; go test -race ./...; go vet ./...;
go build ./cmd/odb. Use local mocks, never claim credentials or client tests
that were not performed. Inherited shared thinking guides are in
`../.trellis/spec/guides/` relative to the repository root.
