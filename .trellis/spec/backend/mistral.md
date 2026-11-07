# Mistral cache and reasoning adaptation

## 1. Scope / Trigger
Only Mistral Chat/Responses generation request preparation changes; use the original scheduler, account admission, replay safeguards and native response forwarding. This is a narrow user-authorized compatibility exception, not a general protocol converter.

## 2. Signatures
- `prepareMistralRequest(body []byte, path, fingerprint string) ([]byte, string, error)` returns prepared body and generated private cache key.
- `mistralReasoningMessages(raw []byte) ([]byte, bool, error)` normalizes complete Chat history, not only the leading routing messages.
- Invoke after strict JSON/model/stateless-response validation and identity selection, but before account/RPM acquisition.

## 3. Contracts
- Exact generation paths: `/v1/chat/completions` and `/v1/responses`. Add `prompt_cache_key=apb-mistral-<existing session HMAC>` only when absent and identity exists. Existing values, including explicit null/empty, are never overwritten. Scope is already group plus downstream-key ID. No random request IDs/raw identities/credentials in generated values or logs; add generated values to private-identity redaction.
- Only Chat assistant top-level `reasoning_content` is adapted. Nonempty string becomes a leading `{type:"thinking",thinking:[{type:"text",text:...}]}` chunk; preserve original string answer or append original content chunks, keep tools/tool IDs/unknown fields. Null/empty reasoning is removed without inventing content. Unsupported reasoning/content types fail locally without acquiring accounts. Responses reasoning is untouched.
- Top-level wire splicing leaves unrelated original fields (including root model/tools/numeric values) untouched; unchanged requests retain original bytes. RawMessage keeps message numbers/tool arguments lossless when a message is adapted. Do not strip other unsupported fields such as root `thinking` or Chat `store`, and do not promise all historical 422 errors share this cause.
- Responses is stateless: full input history, no previous_response_id and store omitted/false/null. Other pools/endpoints and SSE bytes do not use this preparation.
- Administrator account Key/control Key/proxy display/copy is explicitly authorized by the user, across pools. It uses already-plaintext config values, admin-session/no-store APIs and escaped vanilla UI. Do not change import isolation or log redaction, add encryption, migrate password/Cookie stores or expose credentials through runtime-key auth.

## 4. Validation & Error Matrix
| Input | Outcome |
|---|---|
| Missing cache key + known session | Stable HMAC key, caller-isolated |
| Supplied cache key / no identity | Preserve supplied value / do not generate |
| Assistant reasoning string + text/chunks/null content | Preserve reasoning and original answer/tools in native chunks |
| Null/empty reasoning | Remove only empty incompatible field |
| Object/number/array reasoning or unrepresentable content | Local400, zero upstream attempts |
| FIM/embedding/token-counting/other pool | No transformation |
| Anonymous/runtime-key admin read |401, no Key/proxy disclosure |

## 5. Good / Base / Bad Cases
Good: old DeepSeek-style assistant history is normalized before a Mistral request and receives a stable missing cache hint. Base: already-native history with a client cache key retains its bytes. Bad: remove reasoning/tools to hide a422, reuse one global cache ID, or treat NewAPI response `<think>` formatting as outbound request adaptation.

## 6. Tests Required
`mistral_request_test.go`: late history, text/chunks/tool-only/null/empty/invalid inputs, preserved model/tools/large numbers, existing/null cache keys, stable sessions and downstream isolation, excluded paths, local rejection before any upstream request. Existing Mistral native/SSE/failover/privacy tests and admin account credential middleware tests stay valid. Node tests verify escaped full display and exact copy payloads; local fake-data browser verifies UI settings. No production inference required; mock success is not proof of live model authorization or actual cache hits.

## 7. Wrong vs Correct
Wrong: overwrite a caller's prompt_cache_key, forward raw session IDs or change other providers' payloads. Correct: narrowly splice missing hints and documented assistant reasoning adaptation, leaving original state/routing/response contracts intact.

Sources: https://docs.mistral.ai/inference/conversations/advanced/prompt-caching ; https://docs.mistral.ai/api/endpoint/chat ; https://docs.mistral.ai/api/endpoint/responses ; https://docs.mistral.ai/inference/conversations/reasoning .
