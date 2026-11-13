# Model providers, logins and the keeper (2026-10-29)

Built from docs/model-providers-plan.md. The why is there; this records what
shipped, the facts it rests on, and where it differs from the plan.

**Shape.** `internal/providers` is the closed set (17 providers: keys, the
four OAuth flows ChatGPT / Anthropic / Google gemini-cli + Antigravity /
Copilot) with each harness's native mapping, plus the credential type and the
importers. `confstore` holds `provider_credentials` (migration 0016, which
also deletes old-shape harness integrations and clears projects' harness
sets — unreleased, no carry-over). A harness integration's config is
`credential_ids`; validation checks owner, provider/harness/kind
compatibility and sinks (two credentials that would overwrite each other in
one harness — an OpenAI key and a ChatGPT login in opencode's auth.json, the
two OpenCode gateways on omp's `OPENCODE_API_KEY`). `internal/logins` is the
keeper (refreshers, single-flight `Fresh`, `Sweep`/`Run`, `OnRefresh`) and
the sign-in flows. The resolver opens every harness credential through
`Keeper.Fresh`; harnesses render from them (`kinds/credentials.go`).

**Facts verified in the pinned sources, which the design leans on.**
- codex 0.160.1 (codex-rs `login/`): `TokenData.refresh_token` is a required
  `String`, so `""` parses; `id_token` must be a parseable JWT (the keeper
  keeps it); proactive refresh only within 5 min of `exp` (the 8-day rule
  only without one); before any refresh codex re-reads auth.json and uses a
  changed file without calling the token endpoint. Hence push works.
- opencode 1.17.8: `Auth.all()` reads auth.json on every call (no cache) —
  the plan's open check; no harvest branch was needed. OAuth entry
  `{type, refresh, access, expires(ms), accountId?, enterpriseUrl?}`. It has
  ChatGPT and Copilot logins; Claude Pro/Max is not in 1.17.8 (key only).
  Copilot sends `refresh` as the bearer with `expires: 0`.
- omp 18.13.1: endpoints, client ids and request shapes for all four flows
  (rules/auth/*.kdl, registry/oauth/*.ts); the broker wire (strict ArkType
  schemas, `__remote__` sentinel, ETag generation, 404 on the stream →
  long-poll). Copilot's GitHub token is long-lived and is itself the bearer:
  nothing to refresh, nothing withheld.

**Differences from the plan.**
- *Antigravity sign-in* is not offered: its project discovery depends on a
  client fingerprint (version + changelist) omp discovers at runtime.
  Import (CLIProxyAPI with `project_id`) and refresh work. Gemini CLI
  sign-in discovers an existing Code Assist project or onboards the free
  tier; an account that needs `GOOGLE_CLOUD_PROJECT` is told to import.
- *Moonshot* is not in the table (no verified native mapping); Cerebras and
  Fireworks are.
- *The broker token* is not the run token (minted per boot, so a long-lived
  omp would 401 after an engine restart) but HMAC(instance secret, run id):
  `confstore.InstanceSecret("auth-broker")` — 32 random bytes sealed in the
  instance's settings (owner ""). Served at `/api/runs/{id}/auth-broker/…`,
  access kind `AccessBrokerToken`.
- *No harvest anywhere.* Every harness gets access tokens only (codex
  `refresh_token: ""`, opencode `refresh: ""`, omp the sentinel), except
  Copilot whose single token is the bearer.
- *omp API keys* stay env lines; the broker serves only logins.
- The integration *Test* for a login does not probe: a fresh token from the
  keeper is the proof. ChatGPT logins are additionally asked for their
  account's model list (`GET chatgpt.com/backend-api/codex/models`), which is
  also codex's model dropdown and refuses a flow model outside the plan.

**Push.** `Engine.PushCredential` (keeper `OnRefresh`) rewrites the harness
files of live runs whose resolution's `CredentialIDs` holds the credential,
registering each token with that run's redactor first (`SeedAuth` now takes a
`register` callback that is called before any write). Sinks register at
bootstrap and re-entry and drop when the run loop ends.

**Tests that hold the security rules.** `resolve/TestNoRefreshTokenReaches
AWorkspace` (rule 3, real codex/opencode/omp harnesses), `engine/
TestPushCredentialScopesAndRedactsFirst` (rules 2 and 4), `httpapi/
TestCredentialsAPIIsOwnerScopedAndNeverEchoes` and `TestAuthBrokerTakesOnly
ItsRunsToken` (rule 1), `kinds/TestOmpBrokerWire` (schema + sentinel).
`task test:integration:harness` runs the real pinned omp from the tools bundle
against the broker face (`kinds/TestOmpConformance`): it fetched the snapshot,
fell back from the stream, and listed the served logins' models — and none
against an empty broker. The same task holds opencode and codex to what the
engine generates for them (`TestOpencodeConformance`, `TestCodexConformance`,
build tag `conformance`; see "Harness conformance tests").

**Found while running it.** The settings table stores JSON; the instance
secret was first written as a bare string and Postgres refused it at boot.
`Mem.PutSetting` now rejects non-JSON too, and pgstore has a round-trip test.

**Known limits.** The keeper is single-process (one engine; a second
replica would need a DB lease per credential). Signing out of a provider when
a credential is deleted is not done. Pending sign-ins live in memory (an
engine restart loses one in progress).
