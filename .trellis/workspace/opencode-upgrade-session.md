# OpenCode Upgrade Session

## Delivery boundary

Backend work commit: `90bf8d3` (independent Zen/Go providers in the existing
framework, native routing, strict optional buffered compatibility, account-local
models, Go usage, sticky/checkpoint/fallback safety, proxy health, drain and
regressions). A following work commit contains the existing-console integration,
README/operational delivery guide, frontend contract and this journal.

Overall task remains `in_progress`; do not archive as a full P0 completion.
Full source-by-source review, real-time/full-fidelity protocol conversion,
real keyed upstream calls, five-client tests, Docker image/runtime, sustained
scale and production migration qualification remain open.

## Evidence

- Reference pinned at 58f56676ba1438adb30604b889bf36db2a1542a1. No GPL source,
  assets, prompts or tools were copied/linked/bundled; research clone ignored by
  Git and Docker. Main repository had no LICENSE at baseline; no license changed.
- Real public catalog read: Zen/Go SDK metadata and protocol-family API addresses
  verified without credentials. Reference raw usage/docs URLs visibly returned
  404. This is not authenticated inference or usage validation.
- Final gates: gofmt clean, git diff --check, go test ./..., go test -race ./...,
  go vet ./..., native build, CGO-disabled linux amd64/arm64 cross-build,
  node --check and docker compose config --quiet passed.
- Docker daemon unavailable at the local Unix socket; no image build/run claimed,
  no Docker service started or production deployment modified.
- Real browser on disposable local data: tabs/settings, independent strategies,
  dedupe (3 lines -> 2 accounts), bulk disable 2/2, tier separation, upstream key
  not in account JSON, independent Go protocol Base override persisted. OpenCode
  stayed disabled; only fake keys were used. Local servers/browser sessions stopped.
- Existing flaky TempDir cleanup failures were traced to unclosed async log
  writers; test helpers close writers and join poller workers. Existing Perch
  behavior was not rewritten, and no tests were deleted. Three pre-existing
  formatting defects were fixed by gofmt only.

## Operational entry points

- `docs/opencode-audit.md`: evidence, compatibility differences and license review.
- `docs/opencode.md`: implemented/unfinished scope, defaults, import/client
  templates, test results, exact migration and rollback cautions, file map.
- `.trellis/tasks/opencode-upgrade/`: continuing task artifacts and public-source
  observation; `.trellis/spec/backend/opencode.md`: executable safety contracts.
- `_tmp_artifacts.md`: ignored local manifest. Ask before deleting this round's
  temporary artifacts. Never clean Trellis logs or pre-existing analysis/data.
