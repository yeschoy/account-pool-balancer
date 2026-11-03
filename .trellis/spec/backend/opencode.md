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
- Account view: masked runtimeKey/proxyUrl; optional proxyHealth and
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
