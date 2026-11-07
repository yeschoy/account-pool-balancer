# Account Pool Frontend Contracts

The console is vanilla JavaScript/CSS/HTML embedded by internal/server/admin.go.
Extend app.js/index.html and existing components; do not replace the console or
introduce a new frontend build system. Keep node/pnpm artifacts ignored.

- Escape dynamic HTML with esc(); use textContent for diagnostic strings.
- User explicitly authorizes full saved API Key/control Key/proxy display and copy in authenticated administrator account lists/editors for all pools. These are already plaintext config values; do not add encryption or migrate password/Cookie/session stores. Escape values, preserve no-store/admin boundaries, copy exact full values without extra key-read requests, and clear displayed account/modal credentials on logout. Logs/import responses remain redacted; no credentials in localStorage.
- Preserve separate pool filters and explicit provider declarations on import.
- Batch controls call the existing account APIs, keep provider/selection scope,
  report partial failures and require explicit confirmation for deletion.
- Clearly label unknown/stale quota, subscription-wide percentages and buffered
  compatibility. Do not display missing measurements as zero balances.
- Validate with node --check and a real local browser when changing flows.
  Use disposable data and fake credentials; keep OpenCode disabled or point
  all upstream sources to local mocks. Verify backend postconditions, not just
  successful click dispatch.
- Browser/UI temporary files belong in the artifact manifest, not Git. Trellis
  records are permanent task evidence, not temporary cleanup targets.
