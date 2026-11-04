# Implementation / validation plan

1. Audit real source: pool leases, identity, retries, config store, models/poller, import, admin/UI; compare reference routing/discovery/protocol/quota/security/license. Persist evidence and limitations in docs/opencode-audit.md.
2. Add explicit Zen/Go providers, isolated rules/settings and migration tests. Reuse CRUD/import/groups/client keys.
3. Add tier discovery and Go usage using existing poller/snapshots, proxy and cache. Test malformed responses, unknown/stale values, account permission separation.
4. Route native protocols and add conservative conversions with tooling IDs, streaming boundaries and explicit unsupported errors. Test native no-rewrite, errors/retry, cancellation, stateful continuation.
5. Extend existing WebUI. Document supported operations/defaults, examples, client setup and rollback.
6. Run gofmt, go test ./..., go test -race ./..., go vet ./..., go build. Review full diff and record actual outcomes. Commit coherent code and Trellis artifacts per requested staged delivery; do not push.

Do not claim full-repository exhaustive human review from symbol indexing. Track uncovered requirement scenarios explicitly.

## This-round completion evidence

- [x] Core-path source comparison and GPL review persisted; public catalog and stale reference links verified in a real browser.
- [x] Existing-framework Zen/Go backend, isolated configuration/import/permissions and explicit fallback implemented.
- [x] Native routing and conservative compatibility bridge, JSON/SSE/tools and error boundaries covered by mocks.
- [x] Go usage, fixed proxy health, scoped sticky state, checkpoint restoration and graceful drain tested.
- [x] Existing UI extended; real local browser verified settings, dedupe, bulk disable, provider separation, write-only key projections and protocol Base overrides.
- [x] gofmt, go test ./..., go test -race ./..., go vet ./..., native build, Linux amd64/arm64 cross-build, JS syntax and Compose config gates passed.
- [x] Subsequent user-authorized OVH deployment: actual Docker build, offline data-copy preflight, original data mount live restart, credentials/settings preservation and public browser asset verification passed. Local Docker daemon remains untouched. See docs/ovh-deployment.md.
- [ ] Exhaustive reading/review of every source file in both repositories.
- [ ] Low-latency, full-fidelity cross-protocol streaming and unsupported advanced field/content combinations.
- [x] Subsequent user-authorized production Go enablement and real model/usage query: current account ok, 28 catalog / 27 protocol-routable models, fresh rolling/weekly/monthly windows; no inference. See docs/ovh-deployment.md.
- [x] Read-only Go settings/fingerprint/risk comparison against pinned reference and current official policy; findings and unimplemented recommendations in docs/opencode-go-risk-audit.md and research/go-settings-fingerprint-risk.md.
- [ ] Real keyed inference, five official client flows, sustained scale and production upgrade/rollback qualification.

Overall task remains in_progress. Do not archive or mark the entire P0 request completed; docs/opencode.md contains the bounded deliverable and operational instructions.
