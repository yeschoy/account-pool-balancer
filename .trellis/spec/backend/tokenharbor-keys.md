# TokenHarbor explicit saved API-key reads and copy

## 1. Scope / Trigger
User explicitly requests registration batch Key retrieval and pool single/batch copy. This is a narrow exception to TokenHarbor's original write-only Key boundary, not permission to reveal other credentials or create replacement upstream keys.

## 2. Signatures
- Admin GET `/api/tokenharbor/registrations/{rowId}/keys` → `{runtimeKey:string}` from saved Row.APIKey.
- Admin GET `/api/accounts/{accountId}/keys` → `{runtimeKey:string}` for TokenHarbor; OpenCode remains403.
- Console `showStoredAPIKeys(entries, title?)`, entries contain only captured display name and local API path (null means no saved Key).

## 3. Contracts
Both endpoints are authenticated, explicit and no-store; reveal only the saved API Key, never password, TOTP, cookies, proxy authentication, mailbox or checkout URL. Zero upstream calls/store mutations regardless of verification/subscription/status; absence or uncertain creation must not resume/create/replay. Ordinary summaries/logs stay masked.

UI shares a maximum3-read worker pool, captured visible provider/group/task/tab selection, deterministic selection order and full-Key dedupe; report partial failure/skip counts, never auto-retry. Only an explicit copy click changes clipboard. Keep Key data modal-local, never in account/registration state, localStorage or diagnostics; close clears DOM/closures, stops scheduling and discards late reads (clipboard remains user's responsibility).

## 4. Validation & Error Matrix
| Condition | Result |
|---|---|
| No admin session | 401, no Key |
| Unknown account/row | 404/no-store |
| Known registration without saved Key | 409/no-store, no upstream action |
| Explicit TokenHarbor read | 200/no-store, only runtimeKey |
| OpenCode read | 403/no-store |
| Close during reads | No later display or queued read |
| Per-row failure | Partial report, no retry |

## 5. Good/Base/Bad Cases
Good: select current-task saved Keys, fetch once and click copy. Base: incomplete registration with an already checkpointed Key is readable. Bad: using a failed reveal to create a Key or allowing selection from hidden groups.

## 6. Tests Required
`TestTHKeysExplicitReadOnlyAndPrivate` / `TestTHKeyReadKeepsOpenCodeWriteOnly` cover auth, minimal response, no-store, ordinary projection secrecy, absent/uncertain Keys and unchanged source/config with zero mock upstream calls. Node key-copy tests cover bound3/order/dedupe/partial/no-retry/close and provider/group/task/tab isolation. Real browser uses local fake Keys only and verifies clipboard, DOM cleanup and backend counters.

## 7. Wrong vs Correct
Wrong: attach complete Keys to polling summaries or silently recreate a missing one.
Correct: read only an already stored Key in an explicit authenticated modal and clear it on close.
