# TokenHarbor registration profiles, ordering and workers

## 1. Scope / Trigger
User-authorized 2026-11 update: named desktop identities, chronological tasks, editable1–5 registration workers, and random per-account Key/email completion order. These are HTTP compatibility profiles, not real browser/TLS impersonation or permission to bypass provider restrictions.

## 2. Signatures
- `egressid.PickProfile(seed) string`; `ForProfile(seed, profile) Identity`; `For(seed)` keeps the frozen6-profile mapping. `Len()` counts6 legacy +24 named current profiles.
- New Row/diskRow optional fields: `browserProfile:string`, `completionOrder:"key-then-verify"|"verify-then-key"`, `verificationMailAttempted:bool`, `apiKeyCreationAttempted:bool`. Summary exposes only profile/order, not mutation markers or credentials.
- `thregister.Config.{BrowserProfile,CompletionOrder}`; Result carries order and attempted markers. `thSaveResult`/`thStoredResult` round-trip them; independent TH session refreshes cannot erase them.
- Register/retry: `concurrency:0..5` (0=default). Registration settings: `tokenHarbor.registerConcurrency:1..5`, legacy0 becomes1. Shared max: `config.TokenHarborRegistrationMax=5`.
- Registrar `acquire(ctx, attempt, limit) (release,error)`; `thJobView` uses earliest row CreatedAt consistently in list/detail.

## 3. Contracts
- New saved/queued rows get a profile from a crypto-random row ID; duplicate submissions retain the original. Persist an immutable name, never reinterpret a saved slot when extending the pool. Existing empty profile uses original For(seed); new cohort is Chrome/Edge × Windows/macOS/Linux × majors154–157. Reduced major.0.0.0/macOS10_15_7 tokens are deliberate; brand/platform/hints agree. No random patch numbers, per-request identity changes or unnegotiated high-entropy hints. Keep OAuth authorization's honest product identity and native inference's real-UA/fallback rules unchanged.
- Restore the profile for signup/resume, key/mail, plan/checkout/read-only preview/entitlement/reverify/manual resend, captcha and independent mailbox requests. Proxy/region language/timezone bindings remain unchanged; no failure-driven identity or egress rotation.
- New rows with a mailbox get a separately salted ~50/50 completion order, persisted once; no mailbox => key-first. Legacy empty order => key-first. Resume uses saved progress/order, not a fresh random draw or changed default.
- Session checkpoint precedes further work. Key creation reserves apiKeyCreationAttempted durably BEFORE POST, then checkpoints the revealed Key immediately; an attempted creation with no saved Key stops for manual reconciliation, never auto-replays. Existing Keys are never replaced.
- OAuth email-send reserves verificationMailAttempted BEFORE POST. Key-first sends after its newly saved Key; verify-first sends before verification/Key. Existing-key resumes never invent a new send. Confirmation is checkpointed before any verify-first Key request. Timeout/rejection/invalid link leaves verify-first without a Key and retains its session/order. Do not expose a one-time verification URL or raw network error in details/logs.
- Failed/uncertain verify-first rows with genuine saved session, no Key and no verification may explicitly use the existing manual resend action. It is not automatic recovery. An alreadyVerified send response does not mark a Key-less row fully registered; user continues the saved flow.
- A task's concurrency limit is shared by all rows and concurrent retries. Pin its budget while any worker/waiter references it; reclaim only after all references leave. All tasks combined use the same fixed five-slot channel. Never allocate independent global channels for higher requests. Default1; do not add a local signup interval or replay refusals.
- Task order is createdAt DESC, ID ASC for equal timestamps. Build and sort all task projections BEFORE taking latest50. UpdatedAt, note, running state, selected task and random ID do not promote old jobs. Frontend uses the same chronological order without mutating selection/response arrays.
- New fields remain in the existing AES-GCM email envelope / explicitly plaintext GitHub rows. No migration, production config change, payment or auto-import. Older binaries can discard new fields: back up before downgrade.

## 4. Validation / Error Matrix
| Input/state | Result |
|---|---|
| concurrency<0, >5 or fractional | 400 before rows/settings/status mutation |
| concurrency0 | saved default; task/global limits still apply |
| task limit1 plus concurrent same-task request5 | original live limit retained; no rebuild |
| five active across tasks | sixth waits; cancellation/shutdown releases both budgets |
| legacy row / duplicate save | same identity/order/assets |
| verify-first timeout/rejection | no key GET/POST; saved session/order retained |
| key/send reservation save failure | zero corresponding external mutation |
| uncertain Key response / attempted email send | no automatic repeat on resume |
| same-time task creation | stable ID tie-break, no flicker |
| >50 tasks and old recently edited/running | only latest50 by creation |

## 5. Good / Base / Bad
Good: persist chrome-157-macos + verify-then-key, confirm email, checkpoint, create/checkpoint one Key. Base: old account resumes original6-profile identity/key-first flow. Bad: regenerate UA/order on every retry, create a Key after verification timed out, or sort random IDs and then truncate.

## 6. Required Tests
Named profile uniqueness/coherence/stability/spread, legacy mapping; encrypted save/reopen/duplicate and metadata round-trip; password+OAuth both completion sequences, verification/Key/send checkpoint boundaries, uncertain creation/send no replay, timeout/no-mailbox no Key fallback, safe errors; per-task1–5 actual HTTP worker counts, cross-task global5, settings/API bounds, cancellation/leak cleanup, continued unrelated rows after refusal; >50 chronology/ties/detail/min time and note/state changes; Node sorting/settings/form/escaping; real browser with local fake store and upstream counters. Full Go normal/race/vet/build and all Node behavior/syntax gates. Do not describe mock registration as real website acceptance.

## 7. Wrong vs Correct
Wrong: randomize a UA on each request, use a new semaphore when concurrency changes, clear a reservation because a preview succeeded, or treat verified-without-Key as completed registration.
Correct: save immutable per-account choices and mutation markers, share bounded task/global budgets, preserve uncertainty, and explicitly resume only the remaining stage.
