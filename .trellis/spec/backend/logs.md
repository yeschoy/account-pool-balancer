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

## Full-request average throughput (2026-11-09)

Request summaries also carry optional useTimeSec/averageTps. finish samples the end once: useTimeSec=end.Unix()-request.ts.Unix(), not floor(durationMs/1000); averageTps=actual outputTokens/useTimeSec only for successful positive-output requests and a positive whole-second window. This intentionally mirrors NewAPI's request-log formula, including whole-second boundary effects. The UI rounds once to an integer like NewAPI; the stored rate is not pre-rounded. Include this gateway's upload/admission/TTFT/retries inside its recorded request clock; observation points differ from NewAPI, so equal numbers are not guaranteed. Do not subtract TTFT or use input/cached/reasoning tokens as additional output. Keep existing upstreamTps unchanged and label streaming as first-event-after throughput, nonstreaming as upstream call average. Missing/failed/zero-window/legacy values remain unknown, with no frontend historical backfill.

## Queue bytes and Mistral sampling (2026-11-09)

- `logstore.Limits.MaxQueuedBytes<=0` defaults to64MiB. Reserve actual marshaled JSON line bytes atomically before nonblocking enqueue; keep8192-record bound. Full bytes/channel drops disk jobs, increments existing dropped, never blocks inference; writer success/error and close-drain return reservations. `QueuedBytes/QueueLength` are observations, not a process/RSS bound; memory rings retain existing separate limits.
- Mistral `performance.detailSamplePercent` samples once perrequest deterministically from request-ID inside the existing effective detail switch. 0 skips new client/upstream body capture,100 preserves switch behavior; summaries, historical details and independently opt-in cacheAudit remain. Stable immutable account-slice+Revision caching must never weaken credential/session redaction or retain unbounded config versions.
- Regression: queue byte/channel/close/concurrency/retention tests; Mistral sample0/100 and all privacy tests. See `mistral-capacity.md`.

## Mistral native exchange details (2026-11-09)

- Existing admin-only detailLog requestHeaders/requestBody remain the original client input; responseHeaders/responseBody remain the downstream adapted result. New wireVersion=1/upstreamAttempts[] correlates each actual Mistral HTTP attempt by zero-based attemptIndex/accountId; each has method/path, sent-header flag, native HTTP status/terminal, request and optional response message (headers/body/bodyBytes/bodyTruncated).
- Start only when the effective Mistral detail switch is enabled. Use the final outbound body after all model/protocol/parameter/cache/affinity changes; observe Transport automatic headers with HTTP trace. Capture response headers/body before Mistral response adaptation, at the HTTP client observation layer (not TLS/pcap bytes). Network failures have no fabricated response; local rejects have no upstream attempt. Other pools retain their existing capture policies.
- Reuse the head/tail capture and existing detail store/retention; each Mistral body is capped at min(detailBodyKB,64)KiB. Original request length and actually read upstream response bytes are reported separately, with explicit truncation/incomplete flags. Tee only bytes the relay reads, never drain an error or buffer a full stream to complete a diagnostic. A mutex protects the reader-thread capture against finish snapshots; no network I/O while holding it, and the disabled path allocates no capture or header callback.
- Credentials/cookies/custom token headers and session/cache/X-Affinity identities remain redacted. Private response-cookie values learned during capture must also redact downstream detail bodies and error summaries. Preserve log number precision with json.Decoder.UseNumber for native SSE previews. Unknown/malformed/cut JSON that cannot be privately represented is explicitly omitted, not leaked. Do not change actual forwarding bytes during redaction.
- Detail UI labels client original input, each prepared upstream request/native response, and downstream adapted output separately; show attempt status, sent-header state, byte counts and truncation. Legacy records explicitly lack native exchange data. The Mistral error-link opens the correlated request detail, not a nonexistent per-attempt parent record.
- Focused checks cover NewAPI whole-second boundaries, distinct native/adapted bodies, actual automatic headers, multi-attempt429 recovery, privacy including new cookie/header values, >64KiB bounds, off/no-response branches, real gated first-event flush and native SSE large numbers, plus Node rendering/legacy/escaping. No production inference replay is needed.
- Mistral delivered-attempt `upstreamRequestId` and downstream `X-Upstream-Request-Id` select the first valid bounded header in `X-Request-Id` > `Mistral-Correlation-Id` > `X-Kong-Request-Id` order. Do not mutate native response headers, substitute the gateway's internal req_* ID, fabricate a missing trace, change usage, or expand fallback behavior to other pools (including buffered OpenCode compatibility). A trace-capture fix is not a cache-hit improvement. `mistral_request_id_test.go` covers JSON/SSE and `upstream_request_id_test.go` covers precedence, invalid/oversized values and provider isolation.
- Audit truncated bodies as head/tail evidence only, never as a complete payload. `mistral_prefix_stability_test.go` exercises a real local HTTP upstream with49 tools in deliberately nonalphabetical order, property/required-array wire order, growing history/reasoning/images, large integers and a~7MiB history. Existing prepared messages must stay byte-identical on later turns; tool definitions remain raw bytes. This regression guard does not prove an unobserved historical production middle or guarantee provider cache hits.

## Opt-in Mistral cache fingerprints

- `server.Options.MistralCacheAuditKeyID`, CLI `-mistral-cache-audit-key-id` / `ODB_MISTRAL_CACHE_AUDIT_KEY_ID` explicitly selects one enabled Mistral downstream Key ID; default off and invalid targets fail startup without echoing values. No config/metadata migration or automatic enablement. Only exact Chat/Responses generation paths, authenticated target requests and natural traffic; never trigger inference to collect diagnostics.
- Add optional `requestLog.cacheAudit` to the existing admin-only/no-store request stream, independently of body-detail switches. Preserve retention, usage, MarkSent, account admission, redirects/replay and other pools. Original input → prepared → actual attempt are distinct; request Body/GetBody wrappers observe bytes without altering Read/Close/error semantics or response flushing. A full reader hash is not proof of complete network transmission.
- Use an independent per-process random HMAC/runId, never routing secrets or raw routing/session/cache identifiers. Full-body, ordered raw message/tool/schema bytes, root-order and other-field tags retain number precision; individual message/tool digests stay RAM-only. Derived metadata contains no raw body/identity/credentials/address. Native attempt traces matching known credentials/current request identities are omitted with traceRedacted; original response headers remain untouched.
- Bound to30min/128 requests,16 per-phase history baselines,256 opaque Go net.Conn IDs,8 diagnostic attempts/request and4 read/connection events/attempt. Existing maintenance tick releases expired references. At most2048 message/input and256 tool item digests per baseline and128 root fields; retain full-body HMAC on structural overflow, expose unknown/truncated comparisons rather than asserting complete unchanged history. Old/new records remain interoperable.
- Previous comparisons link actual request/attempt/account and mark possible overlap; observation order is not guaranteed provider order. Same-multiset permutations only diagnose ordering, not cache causality. Connection reuse/handshake metadata is the client view, not an identified model cache node or visibility into other Keys/proxy-internal reuse. Compare only the same runId and appropriate identity/model/path/account/source; valid compatibility rewrites and JSON whitespace can also change tags.
- Required tests: `mistral_cache_audit*_test.go` large-middle/append/permutation/property order/large numbers, default-off and key/provider/path scope, TTL/concurrent budget/memory bounds, Body/GetBody/Close/partial/late callbacks, HTTP/2 TLS and normal keep-alive, retry/native-detail hook composition, zero-cache usage/no-response privacy and gated first-event SSE. Node plus a real local fake-data browser must verify the request-row entry, escaping, admin/no-store round-trip and detail-off behavior. See `docs/mistral-cache-diagnostics.md`; this is evidence collection, not a cache improvement or a claim of production deployment.

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
