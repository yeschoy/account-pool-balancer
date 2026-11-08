# Account Pool Frontend Contracts

The console is vanilla JavaScript/CSS/HTML embedded by internal/server/admin.go.
Extend app.js/index.html and existing components; do not replace the console or
introduce a new frontend build system. Keep node/pnpm artifacts ignored.

- Escape dynamic HTML with esc(); use textContent for diagnostic strings.
- User explicitly authorizes full saved API Key/control Key/proxy display and copy in authenticated administrator account lists/editors for all pools. These are already plaintext config values; do not add encryption or migrate password/Cookie/session stores. Escape values, preserve no-store/admin boundaries, copy exact full values without extra key-read requests, and clear displayed account/modal credentials on logout. Logs/import responses remain redacted except for the explicitly authorized Mistral administrator clientRequestHeaders projection: original session/cache/header identifiers are displayed, while authentication credentials stay hidden. Explain that old REDACTED values are unrecoverable and length bounds still apply; escape original values. No credentials or captured headers in localStorage.
- TokenHarbor registration saved-Key reads and existing bounded batch-copy flows still follow `../backend/tokenharbor-keys.md`; registration summaries/logs stay masked and modal close clears secrets. Administrator account lists use the newer explicit full-display policy.
- Preserve separate pool filters and explicit provider declarations on import.
- Batch controls call the existing account APIs, keep provider/selection scope,
  report partial failures and require explicit confirmation for deletion.
- OpenDesign uses the backend diagnosis projection, not status-code guessing.
  Show explicit bans separately from generic403, outage and missing quota;
  wallet0 is not plan exhaustion. Keep historical values/times clearly labeled,
  never display an expired window as measured replenishment, and honor refresh
  ok/quotaRefreshed instead of treating HTTP200 as upstream success.
- Clearly label unknown/stale quota, subscription-wide percentages and buffered
  compatibility. Do not display missing measurements as zero balances.
- Validate with node --check and a real local browser when changing flows.
  Use disposable data and fake credentials; keep OpenCode disabled or point
  all upstream sources to local mocks. Verify backend postconditions, not just
  successful click dispatch.
- Browser/UI temporary files belong in the artifact manifest, not Git. Trellis
  records are permanent task evidence, not temporary cleanup targets.
