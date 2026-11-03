# Public OpenCode catalog observation

Observed through a fresh real browser session during this task; no credentials,
no inference and no anonymous free-model requests were used.

`https://models.opencode.ai/api.json` returned a JSON document with 5,951,997
JavaScript text characters at observation time. Model counts are a snapshot,
not constants embedded in implementation:

- `opencode`: 132 models, provider default SDK `@ai-sdk/openai-compatible`,
  default API `https://opencode.ai/inference/openai/v1`.
- `opencode-go`: 37 models, default SDK `@ai-sdk/openai-compatible`,
  default API `https://opencode.ai/inference/go/openai/v1`.

Per-model SDK/API pairs observed:

| Tier | SDK | Declared API | Count |
|---|---|---|---:|
| Zen | anthropic | /inference/anthropic/v1 | 24 |
| Zen | google | /inference/google/v1beta | 9 |
| Zen | mistral | /inference/openai/v1 | 1 |
| Zen | openai | /inference/openai/v1 | 33 |
| Zen | openai-compatible | /inference/openai/v1 | 65 |
| Go | anthropic | /inference/go/anthropic/v1 | 6 |
| Go | google | /inference/go/google/v1beta | 2 |
| Go | openai | /inference/go/openai/v1 | 5 |
| Go | openai-compatible | /inference/go/openai/v1 | 24 |

SDK package names above have the `@ai-sdk/` prefix. Example limits were real
metadata: glm-4.7 context 204800/output 131072; gpt-5-nano context 400000,
input 272000/output 128000. These are observations, not model name defaults.

The reference's raw GitHub usage.ts, zen.mdx and go.mdx links each visibly
returned `404: Not Found` in the browser. Their contents cannot establish a
current official usage API independently of the reference source.

Consequences:

- Public SDK data validates tier/per-model protocol discovery, but metadata
  addresses are not equivalent to successful authenticated API calls.
- Do not infer all models speak Chat from provider default API or an endpoint
  name. Google/Mistral SDKs remain unsupported/unknown without an explicit,
  independently verified override.
- Metadata API URLs are not dial targets. Never let a public catalog redirect
  credential-bearing traffic. Optional administrator-validated per-protocol
  inference Base maps express the observed family routes, separately from
  catalog/usage bases. Existing requested /zen and /zen/go defaults remain
  unchanged and unverified with real credentials.
- No real Zen/Go authorization, legacy alias compatibility, usage, client gate,
  billing or official client behavior was tested.
