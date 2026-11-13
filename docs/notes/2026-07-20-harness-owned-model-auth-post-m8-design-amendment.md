# Harness-owned model auth (post-M8 design amendment)

Lu's call after using the M8 UI: the harness/provider split read as
redundant (his openrouter key would exist twice — as a provider integration
AND inside opencode's auth_json). Amendment over DESIGN2 §4/§4.1, applied
in two steps (UI first, then full backend removal on his follow-up call):

- **The harness owns model auth.** opencode's `auth_json` (required,
  relabeled) is THE place for model credentials — API keys and/or
  subscription logins, in the harness's native format, written verbatim by
  SeedAuth (which now takes no provider argument). Future harness kinds
  (codex, M10) follow the same shape.
- **The provider port is REMOVED end to end**: `Provider`/`ProviderKind`/
  `ProviderCreds` interfaces, the anthropic/openai/openrouter kinds, the
  fake provider, resolver provider resolution, `projects.provider_ids`
  (migration `0003_drop_providers.sql` drops the column and deletes
  `port='provider'` rows), confstore `PortProvider`, and the UI section.
  Valid ports are repository|provisioner|harness.
- `Resolution.ProviderEnv` / bootstrap's provider.env **mechanism stays**
  (unfed, writes an empty file): it is the natural slot for a future
  harness kind that authenticates via env var (codex + OPENAI_API_KEY) —
  the harness would feed it, not a provider port.
- **Egress derivation coarsens by design**: the opencode harness declares
  the union of provider APIs it can route to (api.anthropic.com,
  api.openai.com, openrouter.ai) unconditionally, so credentials inside
  auth.json are never blocked by the workspace's own firewall. Broader
  than strictly needed — accepted residual, precision explicitly deferred
  ("update later"); `workspace.extra_domains` remains the manual lever.
- `import-config` no longer imports provider env keys; it prints a note
  telling the operator to fold them into the harness auth_json.
