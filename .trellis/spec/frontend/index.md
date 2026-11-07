# Account Pool Frontend Contracts

The console is vanilla JavaScript/CSS/HTML embedded by internal/server/admin.go.
Extend app.js/index.html and existing components; do not replace the console or
introduce a new frontend build system. Keep node/pnpm artifacts ignored.

- Escape dynamic HTML with esc(); use textContent for diagnostic strings.
- OpenCode upstream credentials are write-only. Only masked suffixes may be
  rendered. Never fetch /keys for these providers or include raw config secrets.
- TokenHarbor explicit saved-Key reveal/copy follows `../backend/tokenharbor-keys.md`;
  ordinary lists remain masked, modal close clears secrets, OpenCode remains write-only.
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
