# Model providers, logins and the keeper — plan

Status: built (2026-10-29). Supersedes the ad-hoc `auth_json` / `oauth_json` /
`env` secrets on harness integrations. Where the build differs from this plan,
and why, is in IMPLEMENTATION_NOTES ("Model providers, logins and the keeper").

## Why

- Subscription logins (ChatGPT, Claude Pro/Max, Google, Copilot) are OAuth
  token bundles. Their refresh tokens are **single-use**: redeeming one rotates
  it and kills the old one. Today every workspace is seeded with the same
  stored bundle, the harness refreshes it inside the sandbox, nothing is
  written back, and the auth files are scrubbed at run end. The first
  workspace that refreshes silently kills the integration's copy; two
  concurrent runs race for the same token.
- The preflight auth probe cannot verify a subscription login, so a wrong
  model only surfaces when the first stage opens its session.
- A refresh token inside a sandbox that runs the repository's own
  devcontainer and a prompt-injectable agent is the largest credential we
  expose anywhere.

## Decisions

1. **The engine is the single keeper of every login.** Refresh happens in one
   place, under one lock per login. Workspaces receive **access tokens only**.
   Nothing is ever harvested back from a sandbox.
2. **No per-project default harness or model** (October decision stands). The
   flow binds, the run overrides, the run wins.
3. **A closed set of providers per harness**, for API keys as well as OAuth
   logins. The registry says which providers a harness kind can use and under
   which native id. Unknown providers are refused at save.
4. **Three layers**: model provider credential (owned by the user) → harness
   integration (a kind plus references to credentials) → project (one harness
   integration per kind, unchanged).
5. **Sign-in from the engine** for the four OAuth flows, plus import of a
   token file as the manual route. Never a password: OAuth never yields one.
6. **Apothecary owns its logins.** A credential is a separate OAuth session
   with the user's account, not a copy of the file their laptop CLI is using
   (the engine rotates it; the laptop would be logged out). The form says so.
7. **Security rules are tests, not conventions** (§ Security).
8. Unreleased: no compatibility shims and no carry-over of existing harness
   integrations. One numbered SQL migration adds the new entity and drops or
   rewrites whatever the old shape needs; users re-create their credentials.

## Layer 1 — provider registry and credentials

`internal/kinds/providers.go` — one table:

| field | meaning |
|---|---|
| `ID`, `Name` | our id (`anthropic`, `opencode-go`, `google-gemini-cli`, `antigravity`, `github-copilot`, …) and label |
| `Harnesses` | map harness kind → the provider's native id in that harness (opencode `anthropic`, omp `anthropic`; codex only `openai` / the ChatGPT login) |
| `Auth` | the kinds accepted: `api_key`, `oauth:<flow>`, or both |
| `Domains` | egress hosts |
| `Probe` | zero-token credential probe (metadata GET or empty-chat reject) |
| `Models` | model-list source: live endpoint, the harness catalog, or the account (ChatGPT) |

Starting set: opencode-go, opencode zen, anthropic (key + oauth), openai (key),
openrouter, google (key), google-gemini-cli (oauth), antigravity (oauth),
github-copilot (oauth), deepseek, groq, mistral, xai, moonshot, together; the
ChatGPT login (`openai-chatgpt`, oauth) for codex and omp. Google is two
providers because gemini-cli and Antigravity use different OAuth clients.

The catalog (models.dev, omp's bundled catalog) remains the source of **model
names** for a provider, never of which providers exist.

**OAuth flows** (closed set of four): `openai-chatgpt`, `anthropic`,
`google` (parametrised by client), `github-copilot`. A login outside the set
cannot be refreshed and is refused with a message naming the four and
suggesting an API key.

**Provider credential** (`confstore`, new entity, migration `00NN_provider_credentials.sql`):

- `id`, `user_id`, `provider`, `kind` (`api_key` | `oauth`), `created_at`.
- sealed: the key, or `access`, `refresh`, provider extras (ChatGPT
  `id_token` + `account_id`, Gemini `project_id`, Copilot's GitHub token).
- metadata (not secret): `expires_at`, `last_refresh`, `status`
  (`ok` | `disabled`), `error`, `email`/`account` label when the flow gives one.

**Harness integration**: loses `auth_json` / `oauth_json` / `env`; gains
`credential_ids`. Validation: each referenced credential exists, is owned by
the integration's user (`sameOwner`), and its provider lists this harness kind
with a compatible auth kind. Project validation is unchanged.

**Import**: pasting a native file still works — codex `auth.json`, opencode
`auth.json`, CLIProxyAPI JSON — as a parser into credentials, reporting every
rejected entry. Existing harness integrations are not migrated: the migration
drops their old secrets (or the rows) and users set them up again.

**Sign-in** (`internal/logins/signin.go`): a pending record per user
(`flow`, PKCE verifier, state, expiry ~15 min), two API calls — start, complete
— and a credential sealed at the end exactly as an imported one.

| flow | engine does | user does |
|---|---|---|
| openai-chatgpt | codex device code: `POST {issuer}/api/accounts/deviceauth/usercode {client_id}` → `device_auth_id`, `user_code`; poll `POST …/deviceauth/token {device_auth_id, user_code}` (403/404 = pending, 15 min) → `authorization_code` + PKCE pair → token exchange | opens `auth.openai.com/codex/device`, types the code |
| anthropic | Claude Code PKCE with `code=true`: build the authorize URL with our verifier/state; exchange the pasted `code#state` at the token endpoint | opens the URL, approves, pastes the displayed code |
| google (gemini-cli / antigravity) | installed-app PKCE, headless variant: build the URL; read the code from the loopback address the user pastes; exchange | opens the URL, approves, pastes the address the browser landed on |
| github-copilot | GitHub device flow (`login/device/code` → poll `login/oauth/access_token`), then the Copilot token exchange | opens `github.com/login/device`, types the code |

Each flow's endpoints and client ids are taken from the pinned CLI's source
(codex-rs `login/`, opencode's anthropic + copilot auth, gemini-cli
`oauth2.ts`) and guarded by a conformance test. They are vendor CLI clients,
not public APIs: a change fails loudly, never falls back silently.

## Layer 2 — the keeper (`internal/logins`)

- **Refreshers**, one per flow, with injectable endpoints and httptest
  coverage; each knows its rotation semantics (OpenAI and Anthropic rotate,
  Google and Copilot do not).
- **Tick**: refresh any login within 30 min of expiry; a ChatGPT login whose
  token carries no expiry when `last_refresh` is older than 5 days (codex's
  own rule is 8). Single-flight per credential; persisted before anything
  reads it; audit event with credential id, provider, outcome — never a token.
- **Pre-seed**: ensure fresh, then render (§ Faces).
- **Push**: on every refresh, rewrite the native file in each live workspace of
  each run whose project's harness integration references that credential id,
  **registering the new token with that run's redactor first**
  (`redactor.add`).
- **Permanent failure** (`invalid_grant`, revoked): disable that one
  credential, surface on its owner's page; a run on it fails preflight with
  the reason.
- **Why push works for codex** (verified in codex-rs `auth/manager.rs`): codex
  refreshes on its own only when the access token's `exp` is within 5 minutes,
  and before any refresh it re-reads `auth.json`; a changed file is used
  without calling the token endpoint. Pushing ahead of the window means no
  rotation ever happens inside a workspace while the engine is healthy.
- Live refresh against a real account only behind the existing `live` build
  tag.

## Layer 3 — the faces (how each harness consumes credentials)

| | codex | opencode | omp |
|---|---|---|---|
| render at provisioning | `auth.json` from the ChatGPT login, `refresh_token: ""` (parses: required `String`), `last_refresh` = now; API key → `OPENAI_API_KEY` | `auth.json`: key entries + OAuth entries with `refresh: ""` | API keys → env lines; logins via the broker protocol face |
| on refresh | file rewritten in place | file rewritten in place | omp pulls its snapshot |
| probe / models | the account's model catalog `GET chatgpt.com/backend-api/codex/models?client_version=<pinned>` (bearer + `chatgpt-account-id`): credential verdict **and** the plan's model list | per provider from the registry | per provider from the registry |
| open point | — | confirm in the tools image that the pinned release re-reads `auth.json` (else: full bundle + lineage-checked harvest, opencode only) | conformance test: pinned omp against the engine's broker endpoint reaches a model |

**omp broker protocol face** (served by the engine under the engine URL, run
token in place of the broker token; from omp `packages/ai/src/auth-broker`):
`GET /v1/healthz`; `GET /v1/snapshot` with `wait` long-poll and an ETag
generation honouring `If-None-Match`; `GET /v1/snapshot/stream` → 404 so the
client long-polls; `POST /v1/credential/{id}/refresh`. Snapshot entries:
`{id, provider, identityKey, rotatesInMs, credential}` where `credential` is
`{type: "api_key", key}` or `{type: "oauth", refresh: <sentinel>, access,
expires, accountId?, email?, projectId?}`; schemas are strict — field sets must
match the pinned omp exactly. `provider.env` carries `OMP_AUTH_BROKER_URL` and
`OMP_AUTH_BROKER_TOKEN`; `oauth_json` is no longer seeded.

## Security (multi-user) — rules that ship as tests

1. **Scope by credential id, never by provider or kind.** A run token obtains
   snapshots, file builds and refreshes only for credentials referenced by its
   run's project's harness integrations. *Test: a run token asking for another
   integration's credential gets nothing.*
2. **Push by credential id.** A refreshed token is written only into workspaces
   of runs whose project references that credential.
3. **No refresh token in any workspace, on any provisioner; no harvest.**
   *Test: the rendered files and env of a provisioned workspace contain no
   refresh token.* (Exception only if the opencode check forces the full
   bundle; then harvest accepts a bundle only when it proves the same lineage
   — same account subject in the token — and the stored refresh token is
   confirmed dead.)
4. **Redactor first.** Every pushed token and every snapshot response is
   registered with the live run's redactor before it leaves the engine. *Test:
   a pushed token appears nowhere in the run's events.*
5. **Transient decrypt, token-free audit.** Credentials are opened only for the
   refresh call and the render; audit and metadata never carry tokens.
6. Credential routes are owner-scoped like every integration route; admins
   gain no new visibility. Two users importing the same vendor account would
   rotate each other's lineage: detect by comparing account ids in metadata
   and warn.

What gets safer: a compromised workspace yields an access token usable until
expiry and nothing that can extend it. What stays: a compromised engine host
exposes every credential (already true of the sealed store); access-token
lifetimes are the provider's.

## API and UI

- `GET /api/kinds/{harness}/providers` — the closed set with auth kinds.
- Provider credentials: list / create (key, or sign-in start+complete, or
  import) / delete / test; owner-scoped; metadata only in responses.
- Harness integration form: a picker over the user's credentials filtered by
  the harness's set; no secret fields.
- "Model providers" page under the user's settings: each credential with
  provider, kind, account label, `valid until`, `last refreshed`, status; Test
  runs the probe; Sign in opens the flow.
- Model dropdowns: models per credential (live list where the provider has
  one, the account catalog for ChatGPT, else the harness catalog), tagged
  `live` or `catalog`; a harness filters to the providers it supports.
- Preflight's auth step gives a verdict for every login (the engine knows
  expiry); "unverified" disappears except where a provider has no probe.

## Order

1. Registry, credentials entity + migration (dropping the old harness secret
   shape), import parsers, API, validation. Everything else reads from this.
2. Keeper: refreshers, tick, pre-seed, push + redactor, audit. In parallel:
   the omp protocol face with its conformance test.
3. Faces: codex (file + account model list), opencode (after the re-read
   check), omp wiring (`provider.env`, no `oauth_json`).
4. Sign-in flows, one at a time, each with a conformance test; the ChatGPT
   device flow first (endpoints confirmed).
5. Security tests (rules 1, 3, 4) — written with the code they guard, listed
   here so they are not skipped.
6. UI: providers page, integration picker, dropdown tags, preflight verdicts.
7. Docs + IMPLEMENTATION_NOTES; update the harness/model memory.

## Out of scope, on purpose

- codex's externally-managed `ChatgptAuthTokens` mode and an opencode auth
  plugin (each would retire a file face) — revisit once the faces exist.
- Revoking a login at the provider when a credential is deleted.
- Running omp's own broker binary engine-side (replaced by the protocol face).
- Any per-project default harness or model.

## Facts verified while designing (sources in IMPLEMENTATION_NOTES when built)

- OpenAI refresh tokens rotate and the old one dies; OpenAI's CI guide says not
  to share `auth.json` across concurrent jobs and to write back the refreshed
  file.
- codex: `refresh_token` is a required `String` (empty parses); proactive
  refresh only within 5 min of `exp` (8-day rule only without `exp`); re-reads
  `auth.json` before any refresh and uses a changed file without calling the
  token endpoint; permanent refresh failures are recorded per bundle until the
  file changes; `GET …/codex/models?client_version=` lists the account's
  models.
- omp broker protocol: endpoints and strict snapshot schema as above; clients
  send only the bearer and `If-None-Match`, no version negotiation.
- Apothecary today: projects may reference only their owner's integrations;
  secrets sealed with the master key; API never returns secret values; run
  token bound to its run; redactor accepts runtime values.
