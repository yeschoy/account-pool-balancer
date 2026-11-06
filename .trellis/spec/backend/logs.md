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
