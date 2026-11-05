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
or Git. User subsequently approved cleanup of the temporary preflight container,
data copy and log; these are now removed. Production was not restarted and the
complete rollback backup/image remain. Do not automatically restore old data
over post-deployment writes.

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

## User-authorized intermediate artifact cleanup

Removed 25 allowlisted local temporary entries: reference clone/symbol index,
build/test logs, compiled binaries, smoke fixture/data/screenshot and one-off
deployment scripts/receipt. Removed two owned browser snapshots and one owned
truncated tool log, plus the consumed ignored temporary manifest. Remote cleanup
removed only the stopped network-none preflight container, its copied data and
log. About 50 MiB of measured file payload was removed. Existing unrelated
analysis/cache, real data, deployed source, images/rollback archives, business
docs and all Trellis records were retained. Running container ID/image/restart
count unchanged; health remained healthy. Cleanup recorded in remote deploy.json.

## Operational entry points

- `docs/opencode-audit.md`: evidence, compatibility differences and license review.
- `docs/opencode.md`: implemented/unfinished scope, defaults, import/client
  templates, test results, exact migration and rollback cautions, file map.
- `.trellis/tasks/opencode-upgrade/`: continuing task artifacts and public-source
  observation; `.trellis/spec/backend/opencode.md`: executable safety contracts.
- `_tmp_artifacts.md`: recreate this ignored manifest when generating new
  temporary artifacts; the completed manifest was removed after user-approved
  cleanup. Never clean Trellis logs or pre-existing analysis/data.

## Paid Go gateway hardening — 2026-11-05

Work commit: `c1adadd` (local only; not pushed or deployed).

User approved local paid-Go-only session/cache affinity, fixed account egress
with shared static IPs allowed, existing concurrency/RPM controls, and scoped
overview/request/error logs. Implemented separate source-validated cache
preference below explicit conversations; opaque upstream sessions and persisted
Responses continuity; truthful UA fallback; optional credential-free IP pin
checks through fixed account transport; persistent account/shared-egress policy
pauses including blocked metadata/diagnostics; scoped metrics and log/privacy
fields. New Go UI filters do not leak to Zen/other pools. No identity fabrication,
free-lane shaping, automatic exit rotation or new cross-tier fallback was added.

Full Go tests, race tests, vet, native and CGO-disabled Linux amd64/arm64 builds,
JS syntax and diff checks passed. Real browser used fake keys and local mocks:
limits and independent Go settings saved and were verified by API; repeated
sessions retained an account; cache coverage, scoped key/strategy/bindings,
observed IP and client/local-error filters rendered correctly. Additional Node
behavioral checks covered cross-pool filter isolation and failed HTTP-200 stream
status badges. Mock cache percentages are not production cache-benefit evidence.

No production access/configuration change, deployment or paid inference in this
implementation stage. Actual client workflows, keyed inference, scale and
production qualification remain open; the overall task stays `in_progress` and
is not archived. Delivery/limits: `docs/opencode-go-gateway.md`.

Owned Mock process stopped and test listener verified closed. Mock files,
screenshot and four owned browser snapshots are recorded in ignored
`_tmp_artifacts.md`; retain pending consent. Earlier reference/debug artifacts,
Trellis records and unrelated pre-existing `.worktrees/` were not deleted or
included in the work commit.

## IPv6 proxy follow-up — 2026-11-05

User requested OpenCode IPv6 support and IPv6 proxy compatibility. Public
opencode.ai AAAA records exist; forced native AAAA connection failed locally.
A curl -6 HTTP 200 used IPv4-mapped addresses, so it is not native IPv6 evidence.
No actual proxy address supplied, no production change/deployment/keyed inference.

The existing standard Transport already supports bracketed IPv6 HTTP/HTTPS and
SOCKS5/SOCKS5H. Added safe malformed-literal validation, Go form examples and
local authenticated IPv6 proxy + IPv6 HTTPS target tests covering metadata,
usage, no-credential probe, native inference, IPv6 log/pin canonicalization,
IPv4 mismatch blocking and no direct fallback after proxy loss. All four IPv6
protocol subtests ran, none skipped. Operator guide distinguishes proxy ingress
from actual static egress and host/container routing prerequisites. No custom
network stack, global tcp6 forcing or exit rotation. Overall task stays in_progress.

Final gates passed: full Go tests (server 43.285s), full race tests (server
66.236s), go vet, native and CGO-disabled Linux amd64/arm64 builds, JS syntax,
JSON and diff checks. Form changes are help text only, not a new UI flow.
Temporary public issue-read spill was registered in _tmp_artifacts.md and retained
pending consent; no new persistent Mock process or third-party dependency.

## Recent-hour request balancing — 2026-11-05

User requested recent-one-hour least-used selection when a conversation has no
binding. Added optional `least-requests-1h`, not a silent default replacement:
existing priority/eligibility and session/cache preference remain ahead of new
selection. Score uses current+previous59 minute buckets of sent attempts plus
atomic unsent reservations, not quota ratios, decayed Tokens or lifetime counts.
Tie-breaks use active count, prior fresh-selection sequence and rotating ties.
Sent sticky hits and upstream failures count once; metadata, diagnostics,
cancelled-before-send and local rejects do not. Sparse <=60 buckets reuse v1
selection-counters persistence, validate/copy/expire at restore/export, and keep
pending leases ephemeral. Detached old leases cannot revive reset/deleted
history. Old files start empty, without invented recent-hour backfill.

Full tests (server 53.574s), race (server 80.673s), vet, native/Linux amd64+arm64
builds, JS syntax/JSON/diff passed. Unit coverage includes 80 concurrent pending
leases, cancellation/idempotency, minute expiry/rollback, copies/corruption,
legacy snapshots and Go session/cache/failure accounting. Real headless browser
saved the strategy independently from Zen/Token Harbor, showed 4/2 sends and 0
pending, and verified a true Mock restart retained setting/counts. Only fake keys
and local upstreams were used; no deployment, production edits or paid inference.

Old ignored Mock data had obsolete Token Harbor rule defaults, so kept it
untouched and used registered `recent-data/` instead. Owned Mock process/listener
and QA browser stopped. New data and two owned snapshot paths are in ignored
_tmp_artifacts.md; retain until consent. No dependency, separate scheduler or
new task directory. Overall task remains in_progress; unrelated .worktrees/
was not changed or staged. Operator guide: docs/opencode-go-gateway.md.
