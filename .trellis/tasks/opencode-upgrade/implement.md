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

## Confirmed follow-up scope

- Subsequent hardening and acceptance are limited to the paid Go subscription. Zen, anonymous lanes and free-tier models (including those advertised in Go catalogs) are out of scope for this round. Do not add cross-tier/free fallback, change existing other-pool behavior, or treat this scope confirmation as permission to mutate production. Retain free-tier fingerprint analysis as background only.
- Gemini/Google protocol adaptation is deferred by explicit user instruction. Keep unknown protocols unroutable; do not guess Chat to increase model counts.
- Recommended Go hardening and the six discouraged approaches, with reasons and legitimate alternatives, are recorded in docs/opencode-go-risk-audit.md section 10. This is analysis, not authorization to implement, mutate production, or run paid inference.

## Go pool-gateway follow-up checklist (locally implemented; not deployed)

- [x] Trace existing Codex/Claude identity, account limits/proxy, overview, request/error logs and cache stats; record G1–G7 acceptance in prd.md.
- [x] User approved per-account fixed egress with shared stable IPs allowed. Optional expected-IP checking distinguishes verified IP from configured-only route; no auto-rotation/direct fallback.
- [x] User approved local implementation of the scoped plan; production changes, deployment and paid inference remain unauthorized.
- [x] Go-only session precedence, separate cache preference, stable upstream identity and parent/child/caller isolation with mocks; response-session checkpoint continuity verified.
- [x] Optional IP pin checks through fixed account transport, shared static egress allowed, existing account concurrency/RPM/wait controls; no-direct-fallback and uncertain-submit/no-replay boundaries retained. Known shared-egress policy pauses and post-commit stream errors covered.
- [x] Provider-correct Go overview, measured cache coverage and request/error correlation in existing backend/UI. Browser verified account limits, independent Go settings, same-account repeated mock requests and log filters.
- [x] Focused tests, full Go tests/race/vet/native build, Linux amd64/arm64 cross-build, JS syntax and local browser verification with disposable fake-key/mock data passed. Additional Node behavioral checks verify Go-only filters never leak to Zen/other pools and committed Go stream failures render as failed. Mock process stopped, temporary files registered for consent-based cleanup. Live paid-inference acceptance/deployment still needs separate authorization.

Local delivery guide: docs/opencode-go-gateway.md. Full Go test and race runs passed (server 98.922s / 119.020s during parallel gates); these are test durations, not deployment or downtime. Subsequent import grouping was formatting-only and pool race tests remained cached/passing; final frontend-only filter isolation was verified with Node behavioral checks and syntax validation.

Overall task remains in_progress. Do not archive or mark the entire P0 request completed; docs/opencode.md contains the bounded deliverable and operational instructions.
