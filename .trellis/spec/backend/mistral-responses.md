# Mistral native Responses stream completion

## 1. Scope / Trigger
Only native Mistral POST /v1/responses SSE relay; do not enable strict server-side continuation bindings or modify other pools/Mistral Chat legacy EOF behavior.

## 2. Signatures
- `mistralResponsesTerminal(event string, data []byte) bool`: detect completed/incomplete from SSE event or root JSON type; valid object, no ambiguous duplicate type or conflicting event/type. Nested text mentioning a terminal name and [DONE] are not Responses completion.
- `mistralResponsesTruncated(res *attemptResult) *attemptResult`: precommit502 JSON, terminal=incomplete_stream, category=network, code=mistral_response_incomplete.

## 3. Contracts
- The first event and every later event use the same completion decision. completed/incomplete ends immediately after relay/usage scan, without waiting for upstream EOF. Empty output and tool-only output are valid. Incomplete is a protocol terminal, not a promise of complete generated content.
- SDK data-only frames are supported. First explicit upstream error retains existing precommit error policy. Postcommit response.failed/error is relayed and immediately ends with failed/upstream_envelope; preserve committedHTTP200, never replay.
- Without a valid terminal, clean EOF is still incomplete. Before commit return502 with the fixed diagnostic. After commit append exactly one error SSE (delimit safely even after an unterminated first event), record failure/incomplete_stream, preserve committedHTTP200 and real partial usage, never fabricate completed/DONE/usage or switch accounts. Postcommit read faults use the same explicit failure; idle timeout retains timeout classification. Missing terminal is not evidence of a bad account Key; skip account-health penalty. Failed TPS remains unknown; native details retain the incomplete outcome.
- No buffering of the whole stream, no added read/drain for logs, no production retries. Event reader cancellation/body-close behavior stays bounded to the existing relay.

## 4. Validation & Error Matrix
| Input | Outcome |
|---|---|
| First completed/incomplete + upstream connection left open | Immediate end; original bytes/usage preserved |
| Empty output/tool-only terminal | Legitimate protocol completion |
| Terminal name only in root data.type | Same immediate completion |
| response.failed/error after HTTP commitment | Failed/upstream_envelope; no EOF wait/replay |
| Empty SSE HTTP200 before real event |502 mistral_response_incomplete, one attempt, no health penalty |
| Created/delta only, [DONE] only, malformed terminal, then EOF | Committed200 + one error; failed/incomplete_stream; no measured success TPS |
| Client cancelled | Existing499 cancellation; do not replay |
| Other provider or Mistral Chat EOF | Legacy behavior unchanged |

## 5. Good / Base / Bad Cases
Good: return a first empty completed frame immediately and release the lease while the upstream socket is still open. Base: normal text deltas plus a native completed event remain byte-identical. Bad: treat any EOF as success, require nonempty text before allowing completion, or synthesize completed/output/usage to satisfy a client.

## 6. Tests Required
`mistral_responses_terminal_test.go`: gated upstream waiting for request cancellation, first/event/data-only/completed/incomplete/tool-only, created/delta/unterminated-first/[DONE] premature EOF and actual unexpectedEOF, no retry/health penalty/lease leak, actual request/attempt/detail result and failed TPS. `mistral_responses_test.go`: escaped type, event/type conflict, invalid JSON/root, nested false-positive, duplicate type and other-provider/Chat isolation. `mistral_model_compat_test.go`: four-model Chat/Responses stream/nonstream typed thoughts, Deny/apply_patch multiline arguments/escaping/large integers, cache fields/real usage and refusal without stripping/replay.

## 7. Wrong vs Correct
Wrong: immediately mark delivered200 as success and leave first-frame terminal detection only in the later loop. Correct: classify the first event using the same terminal parser and require a native terminal before treating EOF as successful; preserve the actual HTTP commitment and failure boundary.
