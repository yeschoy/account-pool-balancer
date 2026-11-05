# Account Pool Backend Contracts

Read [OpenCode integration contracts](opencode.md) before modifying config,
quota, pool, proxy, identity or admin code in this repository.
Read [Token Harbor contracts](tokenharbor.md) for its native-key integration,
cache accounting, strict continuation and write-only settings boundary.

Quality gate: gofmt; go test ./...; go test -race ./...; go vet ./...;
go build ./cmd/odb. Use local mocks, never claim credentials or client tests
that were not performed. Inherited shared thinking guides are in
`../.trellis/spec/guides/` relative to the repository root.
