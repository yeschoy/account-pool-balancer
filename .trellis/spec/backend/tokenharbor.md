# Token Harbor contracts

- `tokenharbor` / reserved group `tokenharbor`, `thk_` inferred keys; never send these to OpenDesign. Own import keys, groups, client keys, optional routing and error/retry rules.
- API base `tokenHarbor.baseUrl` includes `/v1`, no userinfo/query/fragment. Only native Chat, Responses, Messages POSTs + models GET; unknown paths rejected locally. No tool stripping, protocol conversion, model aliases, cache mark injection or automatic spend approval.
- Session first, cache key second. HMAC scope includes group and downstream-key ID. `cache_control.type=ephemeral` is not an identity. Default sticky/reject-busy; explicit binding cannot overflow. Full-history requests can fail over within group; stateful Responses cannot.
- `config.StrictResponses` selects the native-key pools' strict state/replay safeguards. `quota.ResponseBindingSource` preserves existing OpenCode source hashes and adds Token Harbor credential/base/proxy hashes. Confirmed Responses bindings persist; ordinary session bindings do not. Missing/expired/wrong-source continuation fails 409, busy fails 429.
- Inference metadata reads only `/v1/models`, never private dashboard balance/session APIs. `CatalogSource` binds data to credential/base/proxy. Manual refresh failures retain prior timestamps/data. Late stores recheck source under poller mutex; restart drops mismatched source. Token Harbor catalogs never teach OpenDesign funding classification.
- No credential redirects, proxy-to-direct fallback or uncertain/post-commit replay. Recognized spend-confirmation responses hard-stop before editable rules and retain the upstream approval prompt.
- Settings API projects only inference base/sync interval; do not serialize registration captcha credentials. Inference saves preserve registration settings and other pools' routing/rules. Registration saves preserve pool routing/model polling; both settings endpoints hide captcha credentials and proxy authentication. Upstream keys write-only in admin API.
- Cache usage stays nullable. Messages prompt includes cache reads AND writes; request/overview ratio uses that denominator. Token Harbor overview rates use measured-only cp/ck/ckr counters. cw/CacheWrite is the shared reported-write counter; kw/CacheCreation is a compatibility alias. Update both from one measurement without double-counting known-write requests. Price table has no write price: requests with measured writes have unknown cost, not understated bills.
- Registration checkpoints persist the session immediately after signup and the one-time key before waiting for mail. Retry preserves credentials; Resume reuses cookies/key, never repeats signup. Mail selection on resume uses the original CreatedAt. Free-model data consent is opt-in, never a registration side effect. Mailbox delivery is an explicitly separate direct HTTPS transport, not a proxy fallback for Token Harbor.
- Agent Pass checkout is monthly/one-time for a Free account only: preview /api/billing/subscribe, validate fromTier=free/kind=tier/oneTime/planName/period and observed <=$1.99 price, then create hosted checkout with expectedPayNowCents and autoRenew=false. Never confirm paid-tier upgrades, region acknowledgements, balance spending or captures. No redirect/retry; persist pending marker before call, block uncertain replay. Encrypted checkout URL appears only in explicit authenticated no-store GET/POST checkout responses, never row summaries/live logs. Accept only https://checkout.stripe.com/c/pay/... without userinfo.
- Existing vanilla console: provider tab/filter, account controls, key management, overview/logs, masked secrets. Local browser smoke uses disposable mock data; never claim real client/inference validation without evidence.

Required tests: native request/response/SSE bytes + headers, body/header/Claude metadata identity, busy no-overflow, stateful source/client isolation and restore, errors/Retry-After, spend consent even custom rules, no redirect/uncertain replay, unknown usage, catalog failure/source change, settings/import/group isolation and secret projection.

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
- Proxy priority: per-account override > current input pool > encrypted saved pool > registration default > direct. Sequential wraps; random draws with replacement. Changing/clearing pool does not alter existing bindings. New rows persist proxyBound=true, including direct routes; clearing a saved row proxy binds direct even when the default later changes. Only legacy rows without proxyBound inherit an empty-URL default. Passwords and proxy authentication are never projected into summaries/settings; no localStorage proxy secrets.
- Existing AES-GCM v1 envelope adds `proxies`; row/disk/summary preserve the plan observation. `saved` survives reopen and transitions only on explicit retry. Older binaries reject the new status: preserve a pre-upgrade backup before rollback.
- Subscription check uses saved same-account cookies and fixed egress, GET `/pricing`, no redirects/retries/mutations/inference. Decode Flight JSON strings and the unique `signedIn/free/plans/currentCycle` component; require signedIn=true, known tier/slug/name, exactly one current plan. Paid current with monthly/yearly grants current active observation; current Free denies admission. No invented expiry timestamp or inference funding claim. UI labels active as last observed, not permanently valid.
- Task-local batch UI reuses retry/subscription/checkout APIs with at most3 in-flight admin requests, no replay, and per-row success/failure/skip counts. Registration requires confirmation including captcha cost, success means queued (not completed), and never auto-imports. Selection survives polling but clears on task switch; shared per-row/batch/prompt locks prevent duplicate dispatch.
- Batch checkout only starts after explicit modal confirmation. Saved links use GET, new links use guarded POST; uncertain outcomes never fall back/retry. Links remain modal-local, no automatic windows/payments; copy-all requires a click and close clears local URLs/handlers. Ordinary task polling/logs never carry checkout URLs.
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
