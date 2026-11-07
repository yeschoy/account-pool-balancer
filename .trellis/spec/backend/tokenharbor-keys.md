# TokenHarbor explicit saved API-key reads and copy

## 1. Scope / Trigger
User explicitly requests registration batch Key retrieval and pool single/batch copy. Registration retrieval remains a narrow saved-Key read, never permission to create replacement upstream keys or reveal passwords/Cookies. Later explicit user authorization additionally permits full saved Key/control Key/proxy display/copy in authenticated administrator account pages for all pools.

## 2. Signatures
- Admin GET `/api/tokenharbor/registrations/{rowId}/keys` → `{runtimeKey:string}` from saved Row.APIKey.
- Admin GET `/api/accounts/{accountId}/keys` → `{runtimeKey:string,controlKey:string,proxyUrl:string}` for all pools, per the newer direct-display policy.
- Console `showStoredAPIKeys(entries, title?)`, entries contain only captured display name and local API path (null means no saved Key).

## 3. Contracts
Both endpoints require administrator authentication and no-store. The registration route returns only its saved API Key, never password, TOTP, cookies, proxy authentication, mailbox or checkout URL. The administrator account route/list deliberately exposes saved Key/control Key/proxy values under the newer user authorization, but never registration passwords/Cookies/mailbox/checkout data. Zero upstream calls/store mutations regardless of verification/subscription/status; absence or uncertain creation must not resume/create/replay. Registration summaries and logs stay masked.

UI shares a maximum3-read worker pool, captured visible provider/group/task/tab selection, deterministic selection order and full-Key dedupe; report partial failure/skip counts, never auto-retry. Only an explicit copy click changes clipboard. Registration-retrieved Key data stays modal-local, never in registration state, localStorage or diagnostics; account cache may contain authorized full account values and is cleared on logout. Modal close clears DOM/closures, stops scheduling and discards late reads (clipboard remains user's responsibility).

## 4. Validation & Error Matrix
| Condition | Result |
|---|---|
| No admin session | 401, no Key |
| Unknown account/row | 404/no-store |
| Known registration without saved Key | 409/no-store, no upstream action |
| Explicit registration read | 200/no-store, only runtimeKey |
| Administrator account read, including OpenCode | 200/no-store, saved runtimeKey/controlKey/proxyUrl |
| Close during reads | No later display or queued read |
| Per-row failure | Partial report, no retry |

## 5. Good/Base/Bad Cases
Good: select current-task saved Keys, fetch once and click copy. Base: incomplete registration with an already checkpointed Key is readable. Bad: using a failed reveal to create a Key or allowing selection from hidden groups.

## 6. Tests Required
`TestTHKeysExplicitReadOnlyAndPrivate` / `TestAdminSavedKeyReadIncludesOpenCode` cover auth, separate registration/account response scopes, no-store, registration projection secrecy, authorized account display, absent/uncertain Keys and unchanged source/config with zero mock upstream calls. Node key-copy tests cover bound3/order/dedupe/partial/no-retry/close and provider/group/task/tab isolation. Real browser uses local fake Keys only and verifies clipboard, DOM cleanup and backend counters.

## 7. Wrong vs Correct
Wrong: attach complete Keys to registration polling/logs, reveal registration passwords/Cookies through an account route, or silently recreate a missing Key.
Correct: preserve registration saved-Key-only reads and modal cleanup while allowing the newer user-authorized full administrator account display.
