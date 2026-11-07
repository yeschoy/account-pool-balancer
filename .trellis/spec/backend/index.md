# Account Pool Backend Contracts

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
Read [Business log retention](logs.md) before changing log cleanup, recovery,
query cutoffs or live-tail retention.

Quality gate: gofmt; go test ./...; go test -race ./...; go vet ./...;
go build ./cmd/odb. Use local mocks, never claim credentials or client tests
that were not performed. Inherited shared thinking guides are in
`../.trellis/spec/guides/` relative to the repository root.
