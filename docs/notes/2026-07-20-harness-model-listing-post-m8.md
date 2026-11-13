# Harness model listing (post-M8)

- `Harness.Models(ctx)` lists the exact `provider/model` strings flows may
  use, given the harness's configured auth. Engine-side (no workspace):
  the opencode kind fetches models.dev's api.json — the SAME catalog the
  opencode CLI bundles — cached 1 h in-process, URL stubbed in tests, and
  filters it to the top-level keys of auth_json (opencode's provider ids).
  Unknown auth keys (bespoke subscription logins) are skipped silently; no
  auth configured is a named error, not a 167-provider avalanche.
- Served as `GET /api/integrations/{id}/models` (harness port only); the
  flow editor gained an "Available models" panel — harness picker, filter,
  click-to-copy. Discoverability only: model strings in flows remain
  UNvalidated at save/preflight (the §6.3 rationale stands — harnesses
  route prefixes the engine cannot second-guess).
- Verified live: Lu's real opencode integration lists 343 openrouter
  models through the deployed endpoint.
- **Failed-invocation logs (found live)**: a plan stage died with
  `harness_error` (model `anthropic/claude-sonnet-4.6` unroutable under
  openrouter-only auth) and its stage log was EMPTY — the adapter's
  `Output` carried only parsed text events, and startup failures produce
  none. On nonzero exit the adapter now returns the RAW output, so the
  stage log and the on_fail feed show the real error. Model strings under
  router auth are `openrouter/<vendor>/<model>` — the first slash splits
  provider/model, the rest is the model id (validModel already allowed
  this); the models panel emits exactly these strings.
