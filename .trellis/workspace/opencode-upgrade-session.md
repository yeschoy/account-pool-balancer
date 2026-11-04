# OpenCode Upgrade Session

## Delivery boundary

Backend work commit: `90bf8d3` (independent Zen/Go providers in the existing
framework, native routing, strict optional buffered compatibility, account-local
models, Go usage, sticky/checkpoint/fallback safety, proxy health, drain and
regressions). A following work commit contains the existing-console integration,
README/operational delivery guide, frontend contract and this journal.

Overall task remains `in_progress`; do not archive as a full P0 completion.
Full source-by-source review, real-time/full-fidelity protocol conversion,
real keyed upstream calls, five-client tests, sustained scale and full production
migration qualification remain open. Docker image/runtime was subsequently
verified during the user-authorized OVH deployment below.

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
- Initial local Docker daemon was unavailable. A subsequent explicitly authorized
  OVH deployment supplied actual image build/runtime evidence; local Docker was
  not started or reconfigured.
- Real browser on disposable local data: tabs/settings, independent strategies,
  dedupe (3 lines -> 2 accounts), bulk disable 2/2, tier separation, upstream key
  not in account JSON, independent Go protocol Base override persisted. OpenCode
  stayed disabled; only fake keys were used. Local servers/browser sessions stopped.
- Existing flaky TempDir cleanup failures were traced to unclosed async log
  writers; test helpers close writers and join poller workers. Existing Perch
  behavior was not rewritten, and no tests were deleted. Three pre-existing
  formatting defects were fixed by gofmt only.

## Subsequent OVH deployment

User explicitly requested deployment after push. Built image from a clean archive
of `1c7a3b8` on OVH, with OCI revision label and a preserved rollback image.
Remote old master has three divergent commits containing accidental Go caches;
its business tree matches GitHub f10fa53 excluding caches. Preserved that tree
and history; did not reset, rebase or delete pre-existing caches. Runtime source
snapshot, not old checkout, is the authoritative build context.

A network-none container with remote-only copied data passed startup/health and
normal shutdown. Then stopped old service, archived the entire quiescent data
tree, validated gzip, and recreated only the target container with original
Compose, bind mount, user and network/legacy alias. Health passed, restartCount
zero. Preserved 47 account IDs/credentials/routing, 8 client keys, 2 import keys,
admin password and existing routing/quota/provider/error/retry settings. Metadata
and session JSON readable. OpenCode and compatibility stayed off. Real public
browser fetched the new 95375-byte JS with matching SHA-256 and Zen/Go tab DOM;
no production admin login or actual Zen/Go inference was attempted.

Full path, image IDs and recovery cautions: docs/ovh-deployment.md. Sensitive
backup/preflight data stayed under the remote 0700 backup directory, never local
or Git. Temporary preflight container is stopped and retained pending cleanup
consent. Do not automatically restore old data over post-deployment writes.

## User-confirmed deployment retrospective (2026-11-04)

User asked why deployment was slow and explicitly requested durable guardrails.
Actual build/preflight took about 10 seconds; end-to-end delivery took about
20 minutes. Avoidable delays were ad hoc script design, repeated inspection,
wrong static asset path and delayed reporting while writing deployment docs.

New root AGENTS.md makes docs/ovh-deployment.md mandatory before deployment.
That runbook defines one preflight -> build -> backup/switch -> one acceptance
pass, conditional compatibility preflight, a 2–5 minute normal-case target,
proactive blocker updates after 5 minutes, immediate verified-live feedback,
and separate build/switch/health timestamps. Keep backups and safety boundaries;
never claim all operation time was downtime. Documentation-only changes do not
trigger redeployment. No new Trellis task or deployment framework is needed.

## Operational entry points

- `docs/opencode-audit.md`: evidence, compatibility differences and license review.
- `docs/opencode.md`: implemented/unfinished scope, defaults, import/client
  templates, test results, exact migration and rollback cautions, file map.
- `.trellis/tasks/opencode-upgrade/`: continuing task artifacts and public-source
  observation; `.trellis/spec/backend/opencode.md`: executable safety contracts.
- `_tmp_artifacts.md`: ignored local manifest. Ask before deleting this round's
  temporary artifacts. Never clean Trellis logs or pre-existing analysis/data.
