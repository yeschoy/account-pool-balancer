# Token Harbor contracts

- `tokenharbor` / reserved group `tokenharbor`, `thk_` inferred keys; never send these to OpenDesign. Own import keys, groups, client keys, optional routing and error/retry rules.
- API base `tokenHarbor.baseUrl` includes `/v1`, no userinfo/query/fragment. Only native Chat, Responses, Messages POSTs + models GET; unknown paths rejected locally. No tool stripping, protocol conversion, model aliases, cache mark injection or automatic spend approval.
- Session first, cache key second. HMAC scope includes group and downstream-key ID. `cache_control.type=ephemeral` is not an identity. Default sticky/reject-busy; explicit binding cannot overflow. Full-history requests can fail over within group; stateful Responses cannot.
- `config.StrictResponses` selects the native-key pools' strict state/replay safeguards. `quota.ResponseBindingSource` preserves existing OpenCode source hashes and adds Token Harbor credential/base/proxy hashes. Confirmed Responses bindings persist; ordinary session bindings do not. Missing/expired/wrong-source continuation fails 409, busy fails 429.
- Inference/model-list hot paths never read websites. Background metadata uses the uniquely linked original imported row (exact accountId + full key), saved session through the pool account's own fixed proxy for GET `/pricing` + `/api/me/free-tier`, and native `/v1/models`. Source-bound TTL and late-store checks prevent stale permission reuse. Catalog presence/API tier never proves Pass coverage and never teaches OpenDesign funding classification.
- No credential redirects, proxy-to-direct fallback or uncertain/post-commit replay. Recognized spend-confirmation responses hard-stop before editable rules and retain the upstream approval prompt.
- Settings API projects inference base, directory sync and independent account sync intervals; do not serialize registration captcha credentials. Inference saves preserve registration settings and other pools' routing/rules. Registration saves preserve pool routing/model polling; both settings endpoints hide captcha credentials and proxy authentication. Upstream keys write-only in admin API.
- Cache usage stays nullable. Messages prompt includes cache reads AND writes; request/overview ratio uses that denominator. Token Harbor overview rates use measured-only cp/ck/ckr counters. cw/CacheWrite is the shared reported-write counter; kw/CacheCreation is a compatibility alias. Update both from one measurement without double-counting known-write requests. Price table has no write price: requests with measured writes have unknown cost, not understated bills.
- Registration checkpoints persist the session immediately after signup and the one-time key before waiting for mail. Retry preserves credentials; Resume reuses cookies/key, never repeats signup. Mail selection on resume uses the original CreatedAt. Free-model data consent is opt-in, never a registration side effect. Mailbox delivery is an explicitly separate direct HTTPS transport, not a proxy fallback for Token Harbor.
- Agent Pass checkout is monthly/one-time for a Free account only: preview /api/billing/subscribe, validate fromTier=free/kind=tier/oneTime/planName/period and observed <=$1.99 price, then create hosted checkout with expectedPayNowCents and autoRenew=false. Never confirm paid-tier upgrades, region acknowledgements, balance spending or captures. No redirect/automatic retry. Lock the row before preview, but persist the pending marker only after validated preview, immediately before hosted creation via `CreateCheckoutGuarded`; failed preview must leave a new row retryable, while legacy uncertain markers survive failed recovery previews. Block uncertain replay unless a single-account request explicitly attests `confirmNoPendingOrder:true` after the user checks the website for no payment/pending order. Never infer that attestation, persist it, send it upstream or use it for batch recovery; existing valid links always win and cannot be overwritten. Encrypted checkout URL appears only in explicit authenticated no-store GET/POST checkout responses, never row summaries/live logs. Accept only https://checkout.stripe.com/c/pay/... without userinfo.
- Existing vanilla console: provider tab/filter, account controls, key management, overview/logs, masked secrets. Local browser smoke uses disposable mock data; never claim real client/inference validation without evidence.

Required tests: native request/response/SSE bytes + headers, body/header/Claude metadata identity, busy no-overflow, stateful source/client isolation and restore, errors/Retry-After, spend consent even custom rules, no redirect/uncertain replay, unknown usage, catalog failure/source change, settings/import/group isolation and secret projection.

## Paid entitlement / weekly allowance / resource gate (user-approved strict policy)

- `Flow.Observe` reads signed-in current Pass + the unique public models/costs mapping from Flight, then paid `plan_used_pct`, `on_plan`, `activated`, `plan_exhausted`, `overage_billing`, reset and seven-day window from `/api/me/free-tier`. Do not use free `used_pct`, wallet, anonymous previews, catalogue tier or checkout for paid allowance. No consent/checkout/billing mutation.
- Plan proof lasts at most10min; source is bound to runtime key/base/egress. Usage must be known, matching plan and before its observed reset when activated. Missing/expired/failed data cannot become0 usage or full quota at reset. A true wallet-overage flag pauses; unknown flag is unknown quota. Never auto-toggle upstream billing. Cached observations are not an upstream atomic billing lock: external changes between reads remain a race, and must not be advertised as an absolute financial guarantee. Auth Cookie value changes may be checkpointed under source/UpdatedAt guards; renewable `session_data` changes alone must not rewrite the encrypted whole store on every poll.
- Coverage is exact native-ID intersection with website Pass mapping, ranked Agent⊂Office⊂Frontier. Free additionally requires saved explicit consent; default is off. Unknown legacy names stay denied. Hard gate is before generic catalogue/IgnoreCatalog and cannot fall through to wallet. Explicit nonempty `model` required, including Responses continuation; full JSON validity, unique lowercase root model reject ambiguity without rewriting native bytes.
- `accountSyncSec`: nil300,0off,otherwise60–300; independent of generic quota enable.0 disables periodic directory work too. `modelSyncSec`: default1800,0no periodic directory refresh, first verification still fetches directory; manual refresh forces both. Inference + downstream models read immutable snapshots only; source check occurs under poller store lock. Owner dedup uses keyed original-row fingerprint, never email/masked-key guesses.
- `/api/tokenharbor/overview` is an admin/no-store local projection: only confirmed Agent gets20 total/5weekly user-requested2× equivalents (not wallet or universal promotion). Disabled confirmed Agent counts toward total, paused/unknown/exhausted cannot count as available; each account uses its own percentage/reset. Unmeasured amount is null/unknown, not0; other tiers are separate counts. Views show age and last measurement.
- Sticky owner fast path validates source/plan/usage/model/health/capacity for that owner; must not bypass any gate or alter reject/wait-overflow/stateful behavior. Busy overflow still falls back to original full selection path. Do not treat x-client-request-id universally as session; pi must actually send a stable supported header.
- Native input budget: default2GiB, at mostGo memory soft limit/3, options/env configurable. Charge actual known-length capacity once; conservatively reserve chunked read then adjust actual capacity. Release on all errors/cancellation/completion. Refuse429 before upstream, Retry-After1; body upload deadline60s is cleared before long response. It is an input budget, not a guarantee against every large-output memory scenario.
- TokenHarbor failed attempts automatically retain a bounded detail even if later retry succeeds; successful requests remain uncollected unless general detail is on. Correlate requestId+zero-based attemptIndex. Stream callback and outer completion dedupe the attempt; never replay committed output. Metadata/request/body identity and response IDs/credentials/Cookies/custom headers/reflected values are removed. JSON preview scans before head/tail clipping without allocating whole message trees; >8MiB/unparseable/unsafe snippets are omitted. Max64KiB head/tail per body and existing retention/total-memory limits remain. Error rows offer explicit detail lookup.
- Capacity proof: local-only synthetic client+gateway+HTTP Mock, not real TPM. Compose8GiB hard/6GiB Go/2GiB input is an initial profile, never claim3500RPM means3500concurrent or that local benchmark guarantees3e8TPM. No production load or online config change without deployment authorization.

Tests must cover source/unknown/expiry/IgnoreCatalog/wallet flag, plan-ID intersection, no network on model-list, nullable/duplicate-owner/disabled/weekly reset aggregate, model omission/duplicates/casing, auth-cookie checkpoint write suppression, input reservation release/429-before-upstream, bounded large-body preview and failed-attempt details after success. Run web behavior/syntax, Go normal/race/vet/build; actual browser uses isolated fake accounts and local endpoints.

## Registration save / subscription admission contract

### 1. Scope / Trigger
Registration persistence, proxy assignment and explicit admission are separate. Successful signup, verified email, visible models and hosted checkout do NOT prove a paid subscription. No automatic registration-to-pool transition.

### 2. Signatures
- `POST /api/tokenharbor/registrations`: existing accounts/proxies/group plus `saveOnly:bool`, `proxyMode:"sequential"|"random"`; legacy `autoImport:true` rejected.
- `GET/PUT /api/tokenharbor/proxies`: PUT requires `{proxies:string[]}`; explicit `[]` clears; GET/PUT respond `{count,labels}` with credential-free labels only. Max1000 entries, validated before atomic persistence.
- `POST /api/tokenharbor/registrations/{rowId}/retry`: explicitly starts a saved row or resumes an eligible existing row; accepts solveCaptcha and optional concurrency=0..10 (0 uses setting), rejects invalid bounds before changing status.
- `POST /api/tokenharbor/registrations/{rowId}/subscription`: only checks current plan; summary contains `subscription.{status,tier,name,cycle,checkedAt,error}`.
- `Flow.CheckSubscription(ctx) (Subscription,error)`; `Server.thImport(ctx,id,group)` always rechecks before config mutation.

### 3. Contracts
- `saveOnly=true` creates encrypted `saved` rows with bound proxy/password/mailbox/group; no workers, HTTP, captcha or import. Duplicate email preserves original credentials/status/job, never implicitly starts a saved row.
- Proxy priority: per-account override > current input pool > encrypted saved pool > registration default > direct. Sequential wraps; random draws with replacement. Changing/clearing pool does not alter existing bindings. New rows persist proxyBound=true, including direct routes; clearing an idle row proxy binds direct even when the default later changes. Only legacy rows without proxyBound inherit an empty-URL default. Passwords and proxy authentication are never projected into summaries/settings; no localStorage proxy secrets.
- Existing AES-GCM v1 envelope adds `proxies`; row/disk/summary preserve the plan observation. `saved` survives reopen and transitions only on explicit retry. Older binaries reject the new status: preserve a pre-upgrade backup before rollback.
- Subscription check uses saved same-account cookies and fixed egress, GET `/pricing`, no redirects/retries/mutations/inference. Decode Flight JSON strings and the unique `signedIn/free/plans/currentCycle` component; require signedIn=true, known tier/slug/name, exactly one current plan. Paid current with monthly/yearly grants current active observation; current Free denies admission. No invented expiry timestamp or inference funding claim. UI labels active as last observed, not permanently valid.
- Task-local batch UI reuses retry/subscription/checkout APIs with at most3 in-flight admin requests, no replay, and per-row success/failure/skip counts. Registration requires confirmation including captcha cost, success means queued (not completed), and never auto-imports. Selection survives polling but clears on task switch; shared per-row/batch/prompt locks prevent duplicate dispatch.
- Batch checkout only starts after explicit modal confirmation. Saved links use GET, new links use guarded POST; uncertain outcomes never fall back/retry. Single-account uncertain recovery requires a fresh unchecked-by-default “已核对账单” checkbox per attempt, consumed before dispatch even on failure; it is not permission to pay or bypass the Free/quote/price/route checks. Links remain modal-local, no automatic windows/payments; copy-all requires a click and close clears local URLs/handlers. Ordinary task polling/logs never carry checkout URLs.
- Live-log polling has exactly one in-flight request and one scheduled successor. Only the poll finally schedules the next timer; show reuses existing work. Append strictly increasing seq once (recognize server restart resets). Task refresh does not reopen a manually hidden log panel. Repeated visible log lines are not evidence of multiple upstream signup POSTs.
- IPv4 proxy ingress can provide IPv6 egress without container IPv6. Host-only success is not container evidence: test the exact account route from the service network namespace. Never replace failed proxy with direct/another egress. Firewall changes require separate explicit authorization; don't disable UFW or broaden public exposure.
- Check failure records unknown without erasing registration/key. Source/account changes during check discard the result. Manual import requires Verified=true + key + fresh active paid result; old summary/checkout never bypasses gate. APIs retain admin auth/CSRF and no-store. Existing enabled accounts and generic manual Key import are not silently modified.

### 4. Validation & Error Matrix
| Condition | Result |
|---|---|
| saveOnly valid | 201, saved rows, no external calls |
| duplicate existing emails only | 200, original job/credentials, no worker |
| autoImport=true / invalid proxyMode / missing proxies PUT | 400 before work or pool replacement |
| free/expired/unsubscribed / missing session / schema drift / HTTP failure | no import; free or unknown observation |
| stale source during check | discard late result, no config mutation |
| cancelled batch registration | zero upstream/admin mutation requests |
| mixed batch / uncertain checkout | per-row partial result; no retry or automatic payment |
| duplicate log poll/show or row click | one in-flight chain/dispatch; no duplicate seq |
| verified + current paid + explicit import | original pool dedupe, one enabled account |

### 5. Good / Base / Bad Cases
- Good: save3 accounts against2 saved proxies, explicitly register later, pay manually, check plan, explicitly import.
- Base: old encrypted rows without subscription/proxies remain readable and unknown; no implicit migration/enablement.
- Bad: treating Checkout.URL, /models, wallet balance, paid badge or cached active summary as an entitlement.

### 6. Tests Required
Assert saved-only zero HTTP/imports; sequential/random reuse and explicit overrides; encrypted reopen/clones/no secret projection; explicit start/duplicate no restart; proxy auth+CSRF/no-store; all known paid tiers/Free, expired-to-Free, unauth/ambiguous/drift/redirect/oversize/error; source-change discard; registration completion never imports even paid; manual admission rechecks against cached paid/checkout. Local browser uses fake data and mocks only. Run `node internal/server/tokenharbor_web_test.js` for log single-flight/dedupe/restart, bounded batch partial failures/no replay, saved-link reuse, cancellation, row locks and selection/escaping. Retry tests must hold one row running and prove a duplicate start returns409 with one upstream GET.

### 7. Wrong vs Correct
Wrong: register → verified → enabled pool account, or create checkout → mark paid.
Correct: save/register → separate current-plan observation → user payment → fresh active-plan check on explicit import; failures deny admission without deleting credentials.

## Observed weekly allowance and monthly-total display

### 1. Scope / Trigger
Extend per-account balance display without changing Pass eligibility or the overview's user-approved2× equivalents.

### 2. Signatures
`PassUsage.weeklyLimitUsd` comes only from authenticated `/api/me/free-tier` `plan.window_allowance_usd`; `monthlyLimitUsd` comes from `plan.allowance_usd`. Both are optional finite positive numbers, consistent with window_days=7 and the matched plan.

### 3. Contracts
- The observed response gives weekly used percentage and monthly nominal allowance (`windows_per_month=4`), not an independently measured monthly balance. The observed dashboard likewise renders weekly usage. Never infer month remaining from one week, multiply weekly remaining by4 or substitute wallet/checkout price.
- UI shows remaining percent, approximate actual USD weekly balance (`limit × (100-usedPct)/100`), weekly reset, and `月限余额：未知（官网未提供月剩余）` with the known monthly total. Clone optional amount pointers in quota snapshots; optional display fields must not change the weekly eligibility gate.

### 4. Validation & Error Matrix
| Condition | Display / behavior |
|---|---|
| Valid week limit + fresh percentage | Approximate USD remainder and remaining percentage |
| Missing/invalid/inconsistent money fields | Amount unknown; established weekly gate unchanged |
| Expired/failed/unknown usage | Current balance unknown; old percentage clearly labelled |
| Monthly total only | Show total separately; monthly remaining stays unknown |

### 5. Good / Base / Bad Cases
Good: observed weekly2.5/monthly-total10 and29% weekly usage ⇒ approximate weekly1.78, month remaining unknown. Base: old cached observations retain percentages with unknown amounts. Bad: presenting10 or `10×(1−29%)` as monthly remaining.

### 6. Tests Required
Cover observed2.5/10 amounts, absent/invalid money fields with unchanged weekly gate, exhausted/expired/unknown views, rounding/escaping and detached snapshots. Run `node internal/server/tokenharbor_quota_web_test.js` and Go normal/race tests.

### 7. Wrong vs Correct
Wrong: fabricate a monthly balance or reuse the overview2× equivalent as actual per-account USD.
Correct: expose only independently measured fields, label weekly estimates, and keep unsupported monthly remainder unknown.

## Read-only checkout diagnostics and first-failure preservation

### 1. Scope / Trigger
An uncertain creation marker can legitimately block later checkout attempts. Its protection is not proof of the first failure's cause or of whether an upstream session exists.

### 2. Signatures
- `POST /api/tokenharbor/registrations/{rowId}/checkout/preview`, body `{kind:"pass",product:"starter_pass_monthly"}`.
- Response `{quote:{...},createsOrder:false,provesNoPendingOrder:false}`; standard administrator auth/CSRF/no-store.
- `Flow.PreviewCheckout` issues exactly one preview=true request; creation reuses the same quote validation before its existing guarded boundary.
- `Checkout.failure` persists credential-free `CheckoutFailure`; row summaries expose `checkoutPending` and optional `checkoutFailure`, never a checkout URL.

### 3. Contracts
- Only a Free account's Agent Pass monthly one-time quote with verified positive cents<=199 is accepted. No upgrade, region acknowledgement, balance debit, capture, automatic renewal, redirect or mutation retry. Preview never creates/clears a reservation; successful preview does not attest to unpaid/no-pending status.
- Use the row's fixed egress and saved session; discard results on row/base/route changes. Persist genuinely rotated auth cookies only under source guards; no account lifecycle, checkout or admission changes.
- Allowlist business error codes (including nested region errors), phase (`preview`/`create`), HTTP status and safe messages. Never retain raw responses/headers/URLs/Cookies/reflected arbitrary error strings. Handle business errors even on HTTP200. Preview network failure is not described as a newly created uncertain order.
- After a creation reservation, persist the first safe creation failure and log its stage. Subsequent replay protection must retain/show it rather than replace it with only the generic protection message. Legacy markers without diagnostics remain unknown; never invent the lost cause or clear their guard.
- Single-account UI offers read-only session/quote inspection and an unchecked-by-default one-shot manual recovery attestation. Known pending attempts are blocked locally until explicitly attested; batches skip them. Preview cannot check/consume the checkbox or unlock creation. The payment form is one column, overriding the generic modal form's more-specific CSS.

### 4. Validation & Error Matrix
| Condition | Result |
|---|---|
| Missing admin / CSRF | 401 / 403, zero upstream requests |
| Other product / unknown quote / unsafe price | Stop before creation |
| Preview valid | Safe quote only; pending record unchanged |
| Row/route changes while previewing | Discard with409 |
| Known pending without explicit recovery attestation | 409, zero new creation requests; preserve original diagnostic |
| Creation rejected/unknown/incomplete | Keep marker and safe first failure; no automatic retry |
| Existing trusted saved link | Reuse it; never overwrite/create another |

### 5. Good / Base / Bad Cases
- Good: inspect a pending row's current quote, keep its original failure visible, then require actual manual billing review before a separately authorized recovery.
- Base: an old marker with no first error says the historical response is unavailable, not that the account or payment provider is definitely at fault.
- Bad: interpreting HTTP200 preview as proof of no pending order, automatically checking recovery, or reproducing an uncertain financial mutation to obtain a better error message.

### 6. Tests Required
Unit tests assert phases/status/allowlisted errors, no secret reflection, preview-only payload and zero mutation replay. API tests cover admin/CSRF, source isolation, unchanged pending/full rows, no reservation on new preview, persisted first failure and detached summary pointers. Web tests cover local blocking, batch skip, preview without approval consumption, one-shot recovery and CSS specificity. Real-browser acceptance uses only local fake accounts/upstreams and verifies preview count>0, create count=0 and a single computed form column.

### 7. Wrong vs Correct
Wrong: clear a legacy marker because the saved session or a new quote works.
Correct: report exactly what was observed; retain financial uncertainty until the user independently checks the matching account's billing state and explicitly attests recovery.

## Per-task registration notes

### 1. Scope / Trigger
Administrator-authored annotations belong to a registration task, not its account rows; editing metadata must not interrupt active work.

### 2. Signatures
- `PUT /api/tokenharbor/registrations/{taskId}/note`: request `{"note":"batch label"}`, response `{"id":"taskId","note":"batch label"}`.
- `Store.AttemptNote(attempt string) string`; `Store.SetAttemptNote(attempt, note string) (string, error)`.
- List/detail task projections add optional `note`.

### 3. Contracts
- Require an explicitly present string; trim surrounding whitespace, permit at most200 Unicode characters, and clear on an empty value. Standard admin authentication, CSRF and no-store apply.
- Keep notes in the existing encrypted v1 signup payload's optional `attemptNotes` map, including notes for plaintext-opted-in GitHub tasks. Older files remain readable. Older binaries can discard this metadata when saving: back up before downgrade if notes must be retained.
- Share the store lock and atomic publication with row/proxy writes, but never alter row UpdatedAt, status, proxy, credentials, session, checkout or admission state. Running tasks may be annotated; failed saves preserve memory and disk state. Deleting one row retains the task note; deleting its last row removes only that task's note.
- Each list entry has an independent editor. Render with esc()/textContent, keep the immutable task ID visible, and capture that ID even if polling replaces objects or selection changes. Opening/cancelling makes no mutation; acknowledgement precedes visible updates; duplicate saves are guarded. Notes never schedule registration or upstream/account actions.

### 4. Validation & Error Matrix
| Condition | Result |
|---|---|
| Missing/null/non-string/overlong note | 400, existing metadata unchanged |
| Unknown task or individual row ID | 404, no orphan metadata |
| No administrator session / missing CSRF | 401 / 403, no write |
| Trimmed empty string | 200, only that task note cleared |
| Persistence failure | Error, previous in-memory and on-disk metadata retained |

### 5. Good / Base / Bad Cases
- Good: annotate two existing tasks independently, refresh, then clear one.
- Base: legacy file without attemptNotes loads with blank notes.
- Bad: attaching notes to every account row or using its Update method, which invalidates worker source guards.

### 6. Tests Required
Store tests cover encrypted reopen, Unicode bounds, isolation, failed-save atomicity, concurrent checkpoint/proxy writes and last-row cleanup. API tests assert admin/CSRF/no-store, unchanged full rows and zero upstream calls. Web tests cover escaping, polling/selection changes, cancel/clear/failure and one-shot saves. Real-browser acceptance uses saveOnly fake tasks and verifies refresh retention plus backend statuses.

### 7. Wrong vs Correct
Wrong: save against the currently selected task after the modal opens, or update row timestamps to persist a label.
Correct: save against the captured task ID in the store's independent encrypted metadata map.

## Independent per-account proxies

- User-approved rule: registration Row.ProxyURL/ProxyBound and pool Account.ProxyURL are independent after admission. New admission copies row.Egress(default); existing full-Key deduplication never overwrites the pool's separately configured proxy. No bidirectional sync or migration.
- PUT `/api/tokenharbor/registrations/{id}/proxy` requires explicit proxyUrl (empty binds direct), validates in Store.SetProxy, permits saved/registered/unverified/imported/failed/uncertain only; queued/running returns409. Binding changes clear only registration Subscription, preserve credentials/cookies/checkout/lifecycle and never mutate pool config.
- Pool console menu “修改代理” uses existing PUT `/api/accounts/{id}`. Nonempty proxyUrl replaces, explicit clearProxy clears, blank proxyUrl preserves. TokenHarbor edits use the same strict proxy validator. Both entrypoints require admin/CSRF/no-store and share a write-only input dialog with clear scope hints; no proxy credentials in responses or DOM.
- Registration checks/Reverify/checkout use the row route. Pool website observation, native directory and inference use Account.ProxyURL, even when different from the original row. A matching verified original session is still mandatory; no original row/ambiguous key/bad session remains unknown. Changing a route never grants eligibility or enables accounts.
- Pool proxy changes retain existing source-bound snapshot/Responses invalidation and background recheck. Registration changes do not clear completed pool proofs, but any changed row UpdatedAt discards an in-flight observation. Reverify final write and checkout reservation verify UpdatedAt/base/effective route to avoid late writes or an external mutation through a replaced source. Never proxy-to-direct fallback or silently reauthenticate/rotate egress on failure.
- Tests: every editable/busy state, encrypted reopen and secret preservation, independent replace/clear, input/auth/CSRF/no-store, new admission inherits bound direct/proxy and re-import preserves override, two local proxy endpoints prove independent read routes, source changes discard late results and failed pool proxy does not use registration/direct. Actual browser needs horizontal scroll into table actions before considering a click dispatched; inspect backend postconditions.

## Registration archive / return

- Console task-local pending/imported tabs filter before deriving selection; pending is the initial view. Switching clears selection, polling retains its visible scope, and operation locks prevent switching during dispatch/confirmation.
- `POST /api/accounts/{id}/return-to-registration` requires admin/CSRF/no-store and a unique imported row linked by exact accountId; an existing pool account must still have the same provider and full key. Never infer a session by email or masked key, and reject Key-only accounts with no original registration.
- Explicit return removes only that account from config and restores the encrypted row to registered/unverified, clearing accountId but preserving credentials, fixed egress, mailbox, checkout and plan observation. No signup, upstream calls, payment or key revocation. Re-admission still checks the current plan.
- Admission/return/delete-row local commits share thAdmissionMu; never hold it across network checks. Admission revalidates row UpdatedAt and lifecycle association after a subscription check, so a late result cannot undo return.
- Config and signup persistence are two files, not an atomic transaction: remove pool config first while holding the row update lock. If row persistence fails, keep the imported/accountId row recoverable, report the partial outcome, and allow return to finish from the imported tab even when the pool account is already absent. Never auto-restore config or overwrite new writes.
- TokenHarbor API model tier is not a Pass entitlement. The user explicitly approved strict unknown-deny in the entitlements/capacity task: management shows covered/catalog counts, downstream lists only validated coverage, and unknown plans/models do not schedule. Historical aliases are not guessed and no wallet fallback applies. This changes runtime eligibility, not account enabled/configuration or registration admission.
- Targeted checks: `node internal/server/tokenharbor_web_test.js`; `go test -race ./internal/server -run '^TestTH'`. Include archive selection, return confirmation, secret preservation, auth/CSRF, mismatched links, partial persistence recovery and a late import check; validate the actual account menu with the floating log panel present.

## Registration authentication and recovery

- Next/React authentication actions can return business rejection in HTTP200 Flight records (`1:{"error":"..."}`). Parse the structured action error/needCaptcha before proceeding; never treat HTTP200 or NEXT_REDIRECT alone as a successful signup. Preserve partial cookies for diagnosis, but never log a complete Flight/HTML response or reflected passwords, captcha tokens, form keys or cookies.
- Only nonempty `sb-<project>-auth-token` or numbered chunk cookies indicate a saved login token. `auth-token-code-verifier` is PKCE input, not a session; exclude it from summaries and session identity decoding even when genuine auth cookies coexist. Cookie presence still does not prove upstream acceptance: /api/keys authentication must succeed before key creation.
- An explicitly retried row with only intermediate cookies attempts the original password sign-in once on its fixed route. User-approved exception: an exact structured “That email and password don't match.” (optionally followed by “Give it another try.”) on HTTP200/400/401 may enter the ordinary signup flow once, only if the row has no key, verification, linked account or checkout and sign-in issued no genuine auth cookie. Preserve the row ID/attempt/CreatedAt/password/mailbox/group/fixed route; share the single worker, cancellation/shutdown checks and existing solveCaptcha consent, without a local signup interval. This does not establish account absence. Other refusals, captcha/rate-limit/network/uncertain results and GitHub OAuth failures never trigger this exception. Login success checkpoints cookies and resumes the original key/mail flow; signup refusal/uncertainty stops without another login/signup loop, consent change, proxy rotation, key replacement, import or payment.
- /api/keys 401/403 proves rejected session authentication, not a revoked key or account_cap. The key may not exist yet. If the original signup response was discarded, record the missing evidence rather than inventing an upstream risk rule. A new signup attempt after uncertain/rejected registration requires an explicit decision, except for the narrowly user-approved sign-in-mismatch policy above; never manufacture eligibility by deleting partial cookies.
- Regression coverage: HTTP200 Flight/JSON rejection, needCaptcha escalation without auto-solve, intermediate-only cookie rejection, real-session-plus-PKCE identity decoding, ordinary rejected/accepted single-login recovery with zero signup requests; approved mismatch with one gated signup; protected existing assets and HTTP403/500/429/captcha/HTML rejection never enter signup; secret reflection, preserved original credentials/time, and no auto-import or consent changes.

## GitHub OAuth registration (explicit plaintext opt-in)

- GitHub input accepts legacy four fields and `login----password----TOTP secret----bound email----mailbox URL`; structured input adds optional githubEmail. Persist a supplied email in Row.Email without changing the immutable GitHub-login dedup identity, including within one batch. Validate the plain email and match it to the code-API path email. A supplied email is not upstream identity/verification proof: compare callback/resumed-session email, retain same-login cookies/grants before stopping a mismatch, and never proceed to finish/key/mail for a mismatched identity. Save-only makes zero external calls; legacy formats/encryption remain compatible.
- JSON code endpoints are HTTPS `/api/v1/mailboxes/<email>/code?key=...`, with unique nonempty key and no userinfo/fragment. Parse the observed envelope independently: success=false + code=no_code/no_mail + retryable=true is pending, not a message. Permanent rejection/unsupported success stops mail polling via ErrMailboxRejected while preserving the already-checkpointed key/session. Success needs a genuine TokenHarbor verification URL and valid received_at; numeric code alone is not email verification, and missing times must not be invented. Use a separate direct transport, no redirects, no raw provider messages/errors/query keys in logs. Do not infer the live positive format from a no_code response. The observed positive reply has code/link, email, message_id, subject and received_at; it can be a GitHub notification linking to github.com. Ignore explicitly GitHub-only messages, never follow their links or declare the mailbox broken. Default code requests to keyword=tokenharbor only when the caller supplied no keyword; preserve explicit filters.
- Official OAuth URL authority includes the observed custom Supabase domain `auth.tokenharbor.ai` as well as `*.supabase.co`; require HTTPS, no userinfo/port/fragment, `/auth/v1/authorize` and provider=github. Pin subsequent identity-service hops to that exact returned origin, never arbitrary TokenHarbor subdomains or lookalikes. Cover full custom-domain mock flow and negative authority cases.
- Authorization, Supabase callback and dynamic `/oauth/finish` stay on one saved GitHub route, locally tcp4; a proxy itself must supply IPv4 egress. Independent post-OAuth TH route may be IPv6 and activates only after confirmed completion. Changing the row proxy affects TH only. No fallback, identity spoofing or automatic POST replay; device verification/extra scopes/unknown pages require manual handling.
- GitHub consent POST may commit successfully with HTTP200 and no Location. Reconcile once with GET of the original authorization URL on the same route; require an actual allowlisted redirect before continuing. Never replay the consent/password/TOTP POST or label missing Location as proven rejection; a second nonredirect result remains unknown/manual.
- OAuth finish does not send the separate site verification email. After checkpointing the newly created API key, call the observed dashboard POST `/api/me/send-verification-email` once, with the site's session/origin and no redirects. Existing-key resume never automatically resends an uncertain email action. `ok:true` only proves sending accepted; only an authenticated `alreadyVerified:true` or a confirmed verification link proves verification. Preserve the key/session on send failure; no signup/key replacement.
- Finish uses live form Action ID/Key, current URL/query and the observed multipart fields `_1_timezone`, `_1_next`, `_1_invite_code` plus React action metadata. Never copy captured Cookie, fixed Action ID, fingerprint, tracing or deployment headers. HTTP200 alone does not prove success; require a same-origin dashboard redirect. Auth is honestly identified as account-pool-balancer/tokenharbor-oauth. Domain-scoped GitHub and TH cookies plus GitHub provider_token/provider_refresh_token are checkpointed independently before subsequent mutations; TH-only cookie rotation must preserve the original GitHub grant; completed resume never repeats OAuth/finish or replaces an existing key.
- OAuth is not TH email verification or paid admission. Continue explicit mail verification and the existing subscription gates; no privacy/billing/inference mutation. MailCat shared HTML mailbox must retain verification href and usable received timestamp; unknown-time old messages are not used.
- User explicitly requested new GitHub credentials and both sessions be plaintext. `tokenharbor-signup.json.githubRows` holds full new GitHub rows, atomically with legacy encrypted email rows/proxy pool in the same 0600 file; no full-store decryption/migration. New directories0700. Never project password/TOTP/Cookies/full Key/proxy authentication in summaries, UI or logs. Preserve GitHub cookies/completion across TH-only refresh/reverify. Older binaries may drop unknown plaintext rows on save: backup required before downgrade.
- OAuth transport failures report an allowlisted service stage and safe category (DNS/timeout/refused/reset/SOCKS5 receiving HTTP/CONNECT denial/TLS), never raw errors, query/state/code or proxy auth. Unwrap request URL errors before classification so query text cannot masquerade as the cause. SOCKS version72 was an HTTP403 from the proxy entrance, not a GitHub account ban; do not infer account rejection, auto-change protocol/egress or replay login. Retain the single row and distinguish zero-session blocked attempts from successful authorization.
- Focused tests cover the complete mocked auth/finish/mail chain, separate transports/cookie scopes, failure no-replay/resume, RFC6238, mixed persistence/clone/dedupe/secrecy and four-field API/UI. Include safe network categories and reflected-secret/query redaction. A generic request timeout may be during connection, TLS or response wait; do not claim the exact phase without evidence. Per-request httptrace records only fixed DNS/TCP/proxy-negotiation/TLS/write/response-header labels under a lock; attach the last observed phase, not a claim of which party caused failure. Classify EOF/unexpected EOF/cancellation/HTTP2 without raw errors, preserve stage on HTTP rejection/body-read failure, and never automatically replay POST. Unknown GitHub forms explicitly identify a local parsing stop on a GitHub response, with allowlisted media label, form count and password/2FA/grant-submitted booleans; no query/state/raw header/body/form values or invented credential/risk verdict. Log each known GitHub submission stage without secrets. Public OAuth-entry/GET probes are not live account login validation, and later HTTP200 cannot establish the cause of an earlier transient failure. Do not claim live registration or egress validation from mocks.

## Honest inference identity and single-worker registration

- Native TokenHarbor inference preserves supplied real client UA. Only absent/recognized generic HTTP library UA gets `account-pool-balancer/tokenharbor-gateway`; share the honest fallback helper with Go without changing its product name. Do not invent browser/official-client identity, device fingerprints, activity, or rotate IPs to circumvent registration restrictions. This does not promise how the upstream APP column classifies a custom product.
- Registration uses one immutable global slot. Accept legacy concurrency0..10 but never raise the actual budget above1; settings projection/save and console controls show1. Normal queue wait is bounded24h, shutdown cancels it; inference/account concurrency is unchanged.
- User explicitly removed the local60s signup interval. Do not reserve submission timestamps or add a fixed gap between different accounts; the next queued account may begin when the previous worker releases. Before site/captcha work and immediately before submission via BeforeSignup, still check context cancellation/shutdown. Existing-account Login/Resume does not submit signup or invoke that hook.
- Registration flow page/precheck/auth/key429 or503, rejected responses carrying Retry-After, and the observed HTTP200 “unusually high number of sign-ups” error raise typed RateLimitError. Preserve full delta/date Retry-After (including multi-day waits); absent/invalid values default5min, integer overflow uses the longest representable pause instead of a short fallback. Never auto-replay, solve a new challenge in response to this refusal, or change identity/egress.
- User-approved scope: a RateLimitError ends only the affected account's current attempt, preserving credentials and reporting its own upstream reason/full Retry-After. Do not propagate it into global cooldown, queued-row invalidation, or local429 rejection of unrelated register/retry API requests. Other explicitly queued rows continue through the single worker without an extra local submission delay. No automatic replay of the rejected account.
- Removing the local interval does not remove provider limits or assure acceptance. Restart must not auto-replay old failed/interrupted work. Preserve the fixed route, existing captcha consent and uncertain-result boundaries; Retry-After is an account-scoped manual-retry instruction, not a shared timer.
- Mock coverage includes all three inference protocols' UA preservation/fallback, immutable single-worker cap, cancellation/shutdown checks, two consecutive queued accounts completing without a local60s delay or test-only interval override, HTTP200 busy and429/503 at each flow stage, complete long/date/overflow delays, continued unrelated queued rows and accepted register/retry APIs after a refusal, zero automatic signup replay, and read-only UI concurrency controls without obsolete interval copy.
