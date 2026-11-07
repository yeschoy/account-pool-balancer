# Mistral cache, reasoning and Chat content adaptation

## 1. Scope / Trigger
Mistral Chat/Responses generation request preparation and the narrow Mistral Chat response content-list compatibility boundary. Use the original scheduler, account admission and replay safeguards. Responses/FIM/other providers remain native; this is not a general protocol converter.

## 2. Signatures
- `prepareMistralRequest(body []byte, path, fingerprint string) ([]byte, string, error)` returns prepared body and generated private cache key.
- `mistralReasoningMessages(raw []byte) ([]byte, bool, error)` normalizes complete Chat history, not only the leading routing messages.
- Invoke request preparation after strict JSON/model/stateless-response validation and identity selection, but before account/RPM acquisition.
- `mistralChatResponse(body []byte, stream bool) ([]byte, error)` maps Chat `message`/`delta` content lists to OpenAI string content and reasoning_content.
- `mistralChatEvent(event []byte) ([]byte, error)` adapts exactly one complete SSE event before its immediate downstream flush.

## 3. Contracts
- Exact generation paths: `/v1/chat/completions` and `/v1/responses`. Add `prompt_cache_key=apb-mistral-<existing session HMAC>` only when absent and identity exists. Existing values, including explicit null/empty, are never overwritten. Scope is already group plus downstream-key ID. No random request IDs/raw identities/credentials in generated values or logs; add generated values to private-identity redaction.
- Only Chat assistant top-level `reasoning_content` is adapted. Nonempty string becomes a leading `{type:"thinking",thinking:[{type:"text",text:...}]}` chunk; preserve original string answer or append original content chunks, keep tools/tool IDs/unknown fields. Null/empty reasoning is removed without inventing content. Unsupported reasoning/content types fail locally without acquiring accounts. Responses reasoning is untouched.
- Narrow model-specific effort exception: only Mistral Chat model `deepseek-v4-1-flash-1017` maps generic OpenAI `reasoning_effort:"medium"` to supported `"high"`. Production upstream error3051 explicitly lists none/low/high/xhigh/max; do not disable/lower reasoning, rewrite other tiers/models, or apply this assumption to Responses/FIM. High may consume more reasoning tokens; disclose this compatibility decision to the operator. This exception does not promise improved cache hits.
- Top-level wire splicing leaves unrelated original fields (including root model/tools/numeric values) untouched; unchanged requests retain original bytes. RawMessage keeps message numbers/tool arguments lossless when a message is adapted. Except for the exact-model stateless Chat store exception below, do not strip unsupported root fields such as `thinking` or other-model Chat `store`, and do not promise all historical 422 errors share this cause.
- Exact-model stateless exception: Mistral Chat `deepseek-v4-1-flash-1017` rejects even `store:false/null` (production422 loc body.store). Remove that single root field only; preserve its neighboring fields, original content/tools/cache key and nested metadata.store. Reject true/non-bool/non-null/duplicate store locally before any account acquisition; never pretend server-side persistence was requested successfully. Responses/FIM/other models retain their contracts. Omitting this unsupported flag is not a guarantee about provider-level data retention.
- Responses is stateless: full input history, no previous_response_id and store omitted/false/null. Other pools/endpoints do not use request preparation.
- Chat response arrays containing `text` and unsigned `thinking` (nested text chunks) map to string `content` and `reasoning_content`; retain tools, IDs, finish reasons, usage, unknown message/root fields and exact JSON numbers with RawMessage. Existing string/null content keeps original bytes. Empty thinking/closed transition markers produce no invented text. Merge an existing string reasoning_content without discarding it.
- Only Mistral `/v1/chat/completions` responses are adapted, both JSON and SSE. Each event is emitted/flushed immediately; never buffer the whole generation. Preserve SSE id/event/retry/comments, delimiters and `[DONE]`; do not fabricate completion. Responses, FIM and other providers keep native bytes.
- Unknown chunk types/metadata, reference chunks, malformed text or nonempty signatures fail explicitly instead of silently losing content. Before commit:502 `mistral_content_unsupported`, category compatibility, no replay/account health penalty. After commit:one error SSE, failed request outcome, no fake `[DONE]`, no replay/account health penalty. This does not disable reasoning or fix unsupported content by dropping it.
- Administrator account Key/control Key/proxy display/copy is explicitly authorized by the user, across pools. It uses already-plaintext config values, admin-session/no-store APIs and escaped vanilla UI. Do not change import isolation or log redaction, add encryption, migrate password/Cookie stores or expose credentials through runtime-key auth.

## 4. Validation & Error Matrix
| Input | Outcome |
|---|---|
| Missing cache key + known session | Stable HMAC key, caller-isolated |
| Supplied cache key / no identity | Preserve supplied value / do not generate |
| Assistant reasoning string + text/chunks/null content | Preserve reasoning and original answer/tools in native chunks |
| Null/empty reasoning | Remove only empty incompatible field |
| Exact DeepSeek model / Chat / generic medium effort | Supported high; all other tiers/models/protocols unchanged |
| Exact DeepSeek Chat root store=false/null | Omit unsupported root field only |
| Exact DeepSeek Chat store=true/invalid/duplicate | Local400, no upstream request or fake persistence |
| Object/number/array reasoning or unrepresentable content | Local400, zero upstream attempts |
| FIM/embedding/token-counting/other pool | No transformation |
| Chat text/thinking response arrays | Per-frame string content/reasoning_content; tools/usage preserved |
| Chat native strings, usage frame, DONE/error/comments | Original event bytes |
| Unrepresentable response chunk before/after SSE commit | Explicit502/error SSE; no replay or account penalty |
| Anonymous/runtime-key admin read |401, no Key/proxy disclosure |

## 5. Good / Base / Bad Cases
Good: old DeepSeek-style assistant history is normalized before a Mistral request and receives a stable missing cache hint; returned thinking/text arrays become string fields NewAPI can parse. Base: already-native string responses and history with a client cache key retain their bytes. Bad: remove reasoning/tools to hide a422, reuse one global cache ID, treat NewAPI `<think>` formatting as outbound request adaptation, or hide parser errors by marking an unsupported response complete.

## 6. Tests Required
`mistral_request_test.go`: late history, text/chunks/tool-only/null/empty/invalid inputs, preserved model/tools/large numbers, existing/null cache keys, stable sessions and downstream isolation, excluded paths, local rejection before any upstream request. `mistral_store_test.go`: root store first/middle/last, whitespace/comma removal, combined effort/reasoning/cache rewrites, nested-store and numeric/tool preservation, zero-attempt refusal of true/invalid/duplicate, other-model/protocol equality. `mistral_effort_test.go`: exact-model medium compatibility, fields before/after model/messages, cache/tool/large-number/reasoning preservation, one upstream attempt, and unchanged native effort tiers/other models/protocols. `mistral_response_test.go`: thinking/text/mixed transition/empty arrays, signed/reference/unknown-content refusal, native string/null/error/usage/DONE equality, SSE metadata and CRLF/multiline framing, tools/large numbers, JSON and SSE route isolation, no replay before/after commit, real HTTP first-event flush before a gated mock upstream finishes, and measured usage. Existing Mistral native/SSE/failover/privacy tests and admin account credential middleware tests stay valid. Node tests verify escaped full display and exact copy payloads; local fake-data browser verifies UI settings. No production inference required; mock success is not proof of live model authorization or actual cache hits.

## 7. Wrong vs Correct
Wrong: overwrite a caller's prompt_cache_key, forward raw session IDs, change other providers' payloads, or conclude `stream_status.status=error` with `end_reason=done` proves a transport interruption. Correct: inspect structured parser errors, adapt only documented Mistral Chat content chunks and missing cache hints, preserving state/routing/accounting and actual per-frame streaming.

Sources: https://docs.mistral.ai/inference/conversations/advanced/prompt-caching ; https://docs.mistral.ai/api/endpoint/chat ; https://docs.mistral.ai/api/endpoint/responses ; https://docs.mistral.ai/inference/conversations/reasoning .
