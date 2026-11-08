# Business log retention

## 1. Scope / Trigger
Request, error, detailed-body, Perch event and TokenHarbor event logs under DATA_DIR/logs, plus the TokenHarbor live tail. Registration credentials/sessions, checkout records, metadata, backups, system/Docker logs and Trellis journals are not cleanup targets.

## 2. Signatures
- `config.Logs.RetentionDays` / JSON `logs.retentionDays`: default7, range1–365.
- `logstore.Limits.RetentionDays` supplies the current configured value.
- `GET /api/tokenharbor/log` also returns `retentionDays` so client-side cached lines expire consistently.

## 3. Contracts
- Missing/default retention becomes7; preserve explicitly configured values such as30 across upgrades. Never silently rewrite online settings.
- Expiry is based on each record's timestamp, not just file modification time. Query applies the current cutoff immediately, recovery excludes expired records, and Append refuses already-expired timestamps.
- One writer owns recovery, segment-number initialization, startup cleanup, periodic cleanup (10min), append, rotation and rewrites. Queries remain not-ready until startup recovery/cleanup completes.
- Prune expired entries from rings and mixed-age/current segments without deleting fresh entries. Atomically replace filtered segment files and reopen live append handles after rename; never truncate fresh logs before a successful replacement. Unknown/missing record times use mtime only as a legacy disk-cleanup fallback.
- Keep record sequence cursors stable during pruning. Removal identifies exact JSON record bytes rather than treating a sequence number as the current line offset, which changes after pruning.
- Clear expired live-tail entries on access/write and filter browser cache with the returned retention setting. Cleanup does not reset sequence cursors or start extra polling chains.

## 4. Validation & Error Matrix
| Condition | Result |
|---|---|
| Config absent/zero | Default7 |
| Explicit valid retention | Preserve value |
| Negative/>365 retention | Reject config |
| Old entry in a recently modified/current segment | Remove entry, preserve fresh lines |
| Fresh entry in old-mtime file | Preserve entry |
| Unknown filename prefix / unrelated file | Leave untouched |
| Failed flush or atomic replacement | Report dropped/error counter; do not truncate fresh retained data |

## 5. Good / Base / Bad Cases
- Good: shrink7→1; old entries disappear from queries immediately and from disk within the next maintenance pass.
- Base: new installation defaults to7; an existing explicitly configured30 remains30.
- Bad: deleting the entire logs directory, exempting a live segment forever, or removing only disk files while serving expired in-memory entries.

## 6. Tests Required
`internal/logstore/retention_test.go`: every managed kind, startup, timestamp-vs-mtime, mixed/current/all-expired segments, live append, removal and reopen after rewrite, immediate query filtering, unrelated-file preservation. Config tests assert default/bounds/explicit-value preservation. Server/web tests assert live-tail retention and unchanged cursors. Run normal/race/vet/build and both TokenHarbor JS behavior suites.

## 7. Wrong vs Correct
Wrong: file.mtime older than retention ⇒ delete it, and trust the in-memory ring forever.
Correct: prune by record timestamps in one writer, atomically preserve retained bytes, and enforce the same cutoff at query boundaries.

## Mistral upstream throughput

Request summaries add optional upstreamDurationMs, upstreamOutputMs, upstreamTps and upstreamTpsBasis. They describe only the delivered actual attempt, never total request upload/admission/selection time or earlier retries. Timing begins at the upstream HTTP call and ends when response/event bytes are read, before decoding/downstream flush. Streaming post-first-event basis divides provider completion_tokens by the read interval from the first real event through completion; this is observed average output throughput, not provider-internal decode speed, and can include network/backpressure. Nonstreaming request-average includes upstream first-response wait and must be labeled separately. Do not double-count reasoning tokens already included by the upstream completion count.

Only successful requests with real output usage and a positive timing window get TPS. Missing usage, failures, zero windows and legacy logs show unknown, not estimated characters or fabricated zero. A genuinely reported zero remains zero. UI shows tok/s with the basis explained; logs retain raw final-attempt usage and timings. Timing tests use deterministic synthetic windows plus existing streaming usage/flush integration; no production inference or load testing is required.

## TokenHarbor independent detail capture

### 1. Scope / Trigger
Only TokenHarbor inference detail collection changes; shared log retention and other pools are unchanged.

### 2. Signatures
- Config boolean `tokenHarbor.detailLog`, default false including legacy configs.
- Existing admin `GET/PUT /api/settings?provider=tokenharbor` projects/accepts `tokenHarbor.detailLog`; the PUT projection uses `*bool` to distinguish omission.

### 3. Contracts
Off overrides shared `logs.detail`, skips response-body capture and failed-attempt detail append, but keeps request/error summaries and prior details. On captures successes and bounded failed attempts (including failed attempts before successful failover), with existing privacy/64KiB limits and terminal-failure deduplication. Registration/other-provider saves and legacy requests omitting the field preserve the value. The dedicated checkbox appears only in TokenHarbor routing settings; its generic shared-detail checkbox is hidden without changing the shared value on save.

### 4. Validation & Error Matrix
| Condition | Result |
|---|---|
| Legacy config / off, even shared detail on | No TokenHarbor details; summaries remain |
| On, shared detail off | TokenHarbor successes/failures collected |
| Failed attempt then successful retry | Failure detail plus completion, original request/attempt correlation |
| Registration / other pool / omitted field save | Preserve independent value |
| Disable after collection | Stop new capture; do not delete existing records |

### 5. Good / Base / Bad Cases
Good: temporarily enable only TokenHarbor and explicitly turn it off after inspection. Base: upgrades default off. Bad: shared detail unexpectedly enables TokenHarbor body capture or a registration save clears its switch.

### 6. Tests Required
`TestTokenHarborDetailDefaultsAndPersistence`, `TestTokenHarborDetailSwitch`, `TestTokenHarborDetailSettingsIsolation`, existing privacy/failover detail tests with explicit opt-in; preserve OpenDesign/Go detail tests. Local zero-account browser verifies unchecked default and on/off persistence without changing global detail. No real upstream calls needed.

### 7. Wrong vs Correct
Wrong: capture all TokenHarbor failures regardless of the switch, or replace its setting from a registration form. Correct: gate both completion and per-attempt capture on the independent persisted value; preserve unrelated settings.
