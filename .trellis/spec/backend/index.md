# Account Pool Backend Contracts

Read [OpenCode integration contracts](opencode.md) before modifying config,
quota, pool, proxy, identity or admin code in this repository.
Read [Token Harbor contracts](tokenharbor.md) for its native-key integration,
cache accounting, strict continuation and write-only settings boundary.
Read [Registration profiles and workers](tokenharbor-registration-identity.md)
for saved browser identities, completion order, task chronology and1–5 workers.
Read [Mistral adaptation](mistral.md) before changing Mistral cache hints,
assistant reasoning normalization or user-authorized admin Key/proxy display.
Read [Business log retention](logs.md) before changing log cleanup, recovery,
query cutoffs or live-tail retention.

Quality gate: gofmt; go test ./...; go test -race ./...; go vet ./...;
go build ./cmd/odb. Use local mocks, never claim credentials or client tests
that were not performed. Inherited shared thinking guides are in
`../.trellis/spec/guides/` relative to the repository root.
