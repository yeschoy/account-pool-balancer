# OpenCode Integration Invariants

- Provider IDs are opencode-zen and opencode-go. An explicit declaration wins
  over overlapping key prefixes. Existing providers retain prefix semantics.
- Accounts/groups/client keys/import keys use the original config/store and
  atomic pool. Never introduce a second scheduler. Group isolation includes
  empty provider-reserved groups. OpenCode import keys are tier-scoped.
- Account selection, admission, RPM reservation/commit, binding and release
  stay under Pool.mu, without network I/O. Read-only Snapshot values are
  immutable. Late transport callbacks cannot revive a released lease.
- Never feed OpenCode model catalogs to OpenDesign funding classification or
  preflight. OpenDesign's Go plan is unrelated to OpenCode Go.
- Runtime bases and optional zenProtocolBases/goProtocolBases include /v1.
  Config.InferenceBase(Account, protocol string) reads only validated admin
  overrides; public per-model API addresses are never credential dial targets.
- Explicit FreeTierError/network-abuse gates stop before editable retry rules;
  do not turn policy refusal into account or egress rotation.
- Runtime bases include /v1. Messages uses x-api-key + anthropic-version;
  Go Messages includes beta=true. Chat/Responses use Bearer. No redirects on
  credential-bearing requests; no proxy-to-direct fallback.
- Catalog visibility is not proof of inference authorization. Keep account
  listings isolated and join only tier-scoped public capabilities. Unknown
  SDK/protocol requires an explicit override, not a Chat guess. Cached model
  permissions expire and are bound to credential/base/proxy/source hashes.
- Go /usage contains rolling/weekly/monthly percentages and reset times, not
  currency. Validate complete windows. Failure retains prior values/timestamps
  and sets unknown/stale; it is never zero. Never sum shared subscription keys.
- Native bytes are not re-encoded. Compatibility is disabled by default and
  explicitly limited/buffered. Reject fields/content that cannot be represented;
  never inject fake reasoning, drop tools, remove previous_response_id, rename
  tool IDs, or fabricate missing measured token usage.
- Responses bindings are downstream-key scoped. Server-side continuation cannot
  overflow a busy binding or fail over after any failure. Missing/expired/
  credential-mismatched state fails closed. Persist confirmed native Responses
  bindings only; ordinary session bindings remain in-memory.
- Cross-tier routing requires a configured opposite-tier group. No recursive
  fallback and no expansion of the original request attempt budget. Re-encode
  each attempted tier from the original client body. Preserve the original
  downstream key scope across an authorized fallback.
- HTTP/SSE commitment forbids replay. A partially written upstream request is
  uncertain, not automatically retryable. Proxy faults do not quarantine or
  degrade OpenCode accounts. Unknown outcomes must be inspected, not retried.
- Config changes validate and persist before atomic swap. Account/base/proxy/
  capability source changes invalidate derived state. OpenCode enabled=false
  prohibits upstream inference, metadata, quota and diagnostic calls.
- Drain admission and cancel/finish registered requests before persisting final
  state or closing logs. Join quota workers. Tests must close asynchronous log
  writers before TempDir cleanup; do not fix races by deleting tests/sleeping.
- No reference GPL implementation/assets/prompts are copied or bundled. Keep
  research checkouts, credentials and caches out of Git and Docker contexts.

## Paid Go gateway refinements

- Go explicit conversation/parent identity outranks `prompt_cache_key`; model-scoped cache preference is separate from response-chain state and caller-isolated. Other providers keep their existing identity order.
- Optional `least-requests-1h` ranks only fresh selections below session/cache preference and inside existing priority/eligibility gates. Compare sent attempts in current+previous59 minute buckets plus unsent reservations; account-level sent usage includes binding hits and failed sends, not metadata/diagnostics/local rejects. Pending leases are atomic and ephemeral, MarkSent is idempotent, and detached leases cannot recreate reset/deleted counter history. Sparse <=60 sorted positive buckets use existing version-1 counters with validated overflow, deep-copy and expiry, not a second scheduler or fake backfill. Do not alter existing strategies/defaults or claim exact-second windows.
- `least-requests-1h` must exclude disabled/quarantined/cooled/policy-paused/not-entitled/confirmed-exhausted accounts and known health below the configured minimum before affinity/ranking; health enforcement is mandatory for this strategy even when `healthFilter` is false. Preview must share the same health gate. Other strategies retain their health switch; fewer than20 samples and unknown/stale quota are not fabricated as unhealthy/empty. An exhausted fresh Go window blocks; no eligible account means an error, not fallback to a low-count bad account. Strict stateful/policy bindings cannot escape an unavailable owner.
- Cache preference is optional bounded in-memory LRU, source-validated, and must not evict session/response bindings or take ownership merely because a preferred account was busy. Ordinary session/cache bindings do not persist; confirmed Responses bindings persist their opaque `apb-` session for continuity.
- Preserve real client UA; only absent/recognized generic library UA uses the gateway's honest product name. Native cache-control/body bytes stay unchanged. Do not log raw session/cache IDs, including optional Go detail capture.
- `Account.egressIp` is optional and Go-only. Shared static IPs are allowed. A configured proxy URL is not proof of a fixed public IP. Pinned inference and metadata require a fresh successful no-credential IP check through the same fixed account transport; failures block without direct fallback or inference RPM/health charges. Empty pin is configured-only/unverified. Checks detect drift and cannot make a dynamic network static.
- IPv6 proxy literals use bracketed authorities for HTTP/HTTPS/SOCKS5/SOCKS5H; egress IP pins are bare canonical addresses. Keep the standard shared Transport, validate malformed literals without echoing credentials, and test IPv6 proxy/target/auth/TLS, IPv4 pin mismatch and no-direct-fallback on local IPv6 sockets. IPv6 proxy ingress does not prove IPv6 egress; IPv4-mapped remote addresses are not native external IPv6 evidence.
- Go IP check URL is admin-validated and opt-in; no implicit external reflector. Coalesce checks outside pool locks, bound cache/response size/time, refuse redirects and discard stale-source quota writes. Changing check freshness must expire the corresponding check identity.
- Go known policy refusal pauses persisted account state; explicit network abuse also pauses Go peers on the same configured route or expected IP, even when one peer has no pin. Do not affect Zen/other pools or replay after committed output.
- Go overview must scope binding/key/strategy values correctly. Cache token reads, cache affinity, session-binding hits and measurement coverage are different metrics; missing cache writes/input/output/timing remain unknown and historical data is not backfilled as measured zero. Price-table estimates are not Go subscription bills.
- Request/error records distinguish local/upstream/transport, correlate client/selection/cache/egress outcomes without raw identities, and preserve original HTTP status for errors occurring inside a committed stream.

## Executable cross-layer contract

### 1. Scope / Trigger
Authenticated OpenCode integration changes config, account management, routing,
metadata persistence and console projections.

### 2. Signatures
- Config.InferenceBase(a Account, protocol string) string
- Config.ForProvider(provider string) *Config
- Pool.Acquire(ctx, cfg, Request{RequireBinding, PreserveBinding, Group, Model,
  RequestID, Affinity, Explicit, Excluded, Wait}) (*Lease, error)
- POST /open/v1/accounts/import (existing import-key auth)
- POST /api/accounts/{id}/test-proxy (existing admin session/CSRF guard)
- GET/PUT /api/settings?provider=opencode-zen|opencode-go
- POST /v1/chat/completions, /v1/responses, /v1/messages; GET /v1/models

### 3. Contracts
- Import: {accounts:[{provider, runtimeKey|apiKey|key, name, group, proxyUrl}]}.
  Max 200 items. New providers require the matching OpenCode import key.
- Settings: optional opencode object. Absent means preserve it, not reset it.
  Protocol names are chat/responses/messages. Protocol base maps contain URLs
  ending /v1; no userinfo/query/fragment; max 4096 characters per URL.
- User-authorized administrator account view: full saved runtimeKey/controlKey/proxyUrl with admin-session/no-store boundaries; no new encryption or password/Cookie migration. Logs and non-admin/import projections remain redacted. The legacy WriteOnlyCredentials helper remains an import isolation/dedup marker, not display permission. Optional proxyHealth and
  routableModels. Do not equate Catalog size with supported protocol count.
- Snapshot.opencode: Source, Models/ModelsAt, Usage/UsageAt, CheckedAt,
  CapabilityError, UsageError. Source is a hash, never a credential.
- Go /usage: usage.{rolling,weekly,monthly}.{status,percent,resetsAt}; percent
  must exist and be 0..100, status ok/rate-limited, reset time nonzero.
- metadata.pool.responseBindings: key/accountId/source/expiresAt. key/source
  are hashes and only confirmed native Responses bindings are persisted.
- No new environment variables or databases; original ODB_* settings remain.

### 4. Validation & Error Matrix
| Condition | Outcome |
|---|---|
| bad config/protocol base | reject; keep prior config pointer/file |
| mixed provider group / cross-tier import key | reject |
| native protocol | original request/response bytes; original provider fields |
| unrepresentable request | local 400; no RPM commit or health degradation |
| unsupported generated output | 502; no replay and no account blame |
| unknown/expired stateful binding | 409; no random selection |
| busy bound account | bounded wait/429; preserve binding |
| known policy gate | 403; no key/proxy walk even with custom retry rules |
| 429 | account cooldown; default honors full Retry-After |
| quota read failure | keep old data/timestamps; unknown/stale, never zero |
| proxy connection/407 | separate proxy health; never direct fallback |
| disabled OpenCode | no upstream inference, refresh or diagnostics |

### 5. Good / Base / Bad Cases
- Good: Messages client selects a Messages model, or administrator explicitly
  enables limited compatible Chat-to-Messages conversion with representable
  tools and a token limit.
- Base: old version-1 config has no opencode object; old providers remain usable
  and the new integration is off.
- Bad: using a Responses previous_response_id with a Chat model, or honoring
  arbitrary provider.api from public metadata as a credential-bearing target.

### 6. Tests Required
Assert native byte equality, all protocol pairs' tool IDs/history, native and
buffered SSE commitment boundaries, missing usage not zero, unknown protocol
not advertised, source changes/late writes, cross-key state isolation, fixed
proxy failure/recovery, feature-disable no HTTP calls, explicit protocol bases,
original total attempt budget after sticky fallback, config rollback, restored
checkpoints, drain cancellation and exactly-once RPM/release. Use mocks for
credentials and clearly separate real public metadata reads from inference.

### 7. Wrong vs Correct
Wrong: concatenate one OpenAI base for every SDK; retry a policy 403 across keys;
clone all quota maps under the poller hot-path mutex; close logs before requests
and quota workers finish.

Correct: native protocol + explicit admin Base + protocol-specific auth; stop
policy refusals; copy immutable pointers under the poller lock and deep-clone
outside it; drain admission/cancel/join before final persistence and log close.
