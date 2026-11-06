# Validation — local only

## Workspace / scope
- Orca workspace/branch: `tokenharbor-ua-order-concurrency`, base `9ac4ce7ff90c58f41d7c26807d1513353877cc4a`.
- Path: `/Users/lyh_god/orca/workspaces/account-pool-balancer/tokenharbor-ua-order-concurrency`.
- All task writes are isolated here. Original checkout's initial two HTML/JS drafts and temporary folders were not copied. Other concurrent work later added changes to its go.mod/go.sum/Perch/TLS/TH files; none were touched, reset, merged or committed by this task.
- No push, merge, deployment, real account signup/email/captcha, payment, admission or live configuration changes.

## Automated gates: PASS
- `gofmt` on all changed Go sources; `git diff --check`.
- `go test ./...` (final full pass).
- `go test -race ./...` (final full pass; server package88.264s).
- `go vet ./...`.
- `go build -o .tmp-tokenharbor/odb-final ./cmd/odb`.
- `node internal/server/tokenharbor_web_test.js`.
- `node internal/server/tokenharbor_checkout_url_web_test.js`.
- `node internal/server/tokenharbor_quota_web_test.js`.
- `node internal/server/perch_fingerprint_web_test.js`.
- `node --check` for every `internal/server/web/*.js`.

## Assertions covered
- 24 unique named desktop UA configurations, coherent Chrome/Edge version/brand/platform/reduced shape, no unconditional high-entropy hint; stable profile names and seed selection, frozen6-profile legacy mapping; 200-row encrypted save/reopen/duplicate distribution and progress retention.
- Password and OAuth: key→verify and verify→key exact event order; session/Key/verified checkpoints; attempted send and attempted Key markers persisted BEFORE POST; no replay after uncertain result or existing Key; no Key after verify-first mailbox rejection/timeout/missing mailbox; no one-time URL/network-secret reflection; checkpoint failure stops the next mutation. Explicit manual resend can unblock a failed verify-first row without pretending a missing Key was created.
- Real mocked HTTP worker concurrency for each1–5, default3 through API0, global5 across tasks, pinned live task budget cannot be rebuilt from1 to5, cancellation/shutdown and slot cleanup, invalid(-1,6,10,fraction) register/retry/settings fail before mutation. Existing rate-limit isolation/no automatic replay regressions remain.
- 55 jobs: chronological sort before cap50, random IDs deliberately reverse chronology; stable equal-time tie, consistent list/detail, earliest row creation, changed old note/running state cannot promote an old task. Frontend sorting does not alter selected task or response arrays.
- Settings/form actual1–5 persistence and validation; unrelated subscription/checkout operations do not depend on validity of the registration concurrency input.

## Actual browser + local backend
Only disposable fake data and local endpoints: application `127.0.0.1:18796`, upstream Mock `127.0.0.1:18797`. OpenCode disabled, no pool accounts, periodic TH directory/account sync off; no real credentials.

1. Log in, select TH pool and registration page: default concurrency3, min1/max5/readOnly=false. Four seeded tasks rendered newest→oldest despite reversed random-looking IDs. Details showed saved profiles and both completion orders.
2. Submit6 fake mailbox-only accounts with concurrency5 and captcha unchecked. Mock rejects signup-page GET with429 after a short bounded hold. Its counters independently reported `maxActive=5`, `loginGets=6`, `signupPosts=0`, `keyPosts=0`, `sendPosts=0`, active0. Thus five execute concurrently, sixth subsequently proceeds, no refused account is auto-replayed, and no real mutation/fee occurs.
3. Authenticated read-back: exactly6 failed rows under the newly created latest task; profiles persisted; original16 fake rows remain saved; UI order equals API order.
4. In settings, set5 and save: UI “已保存”, authenticated GET settings returned registerConcurrency5, registration page showed5. No inference/admission settings were enabled.
5. Captured verified full-page screenshot `registration-verified.png` (1280×1905,519066bytes), plus structured browser settings/registration/counter proofs. Tool artifact verification passed.
6. Logged out and confirmed login form. Both owned fixture processes were terminated and `ps` confirmed neither remains. Data/logs/scripts/screenshots remain for user-approved cleanup.

Browser artifact caveat: a `read` of the JSON mock/status endpoint failed decoding and reset that managed browser. No mutation was retried; re-opened status normally and obtained counters, then reauthenticated and verified saved state. The first blank `registration.png` is NOT acceptance evidence; only `registration-verified.png` is used.

## Known limits / rollout
- HTTP UA profiles do not emulate TLS, header order, JS, Canvas, real browser activity or guarantee provider acceptance. Versions are the verified 2026-11 published major cohort; no invented patch numbers.
- New mailbox-less and legacy records remain key-first. New records with mailbox receive both orders; existing partial attempts are not randomly reordered.
- “verify-first” may wait for mail before acquiring a Key; its row remains recoverable if mail is late. An uncertain Key response stops for manual website reconciliation rather than creating a second Key.
- Active task limits stay pinned until no executing/waiting worker references them. Global cap5 is not five per task; no rate-limit bypass or local automatic replay.
- Optional new persistence fields can be discarded by older binaries. Any future deployment/rollback must separately follow deployment docs and retain a full pre-upgrade backup. This task is not deployed.
- Temporary artifacts tracked by ignored `_tmp_artifacts.md`; Trellis evidence is permanent and committed.
