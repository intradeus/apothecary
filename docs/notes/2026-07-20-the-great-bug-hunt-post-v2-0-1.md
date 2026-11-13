# The great bug hunt (post-v2.0.1)

A four-subsystem adversarial review (~40 findings) followed by a fix-all
pass. The load-bearing fixes, by area:

**Confirmed-broken (would have bitten immediately)**
- CPUI `req()` discarded 202 bodies → the envcheck button destructured
  `undefined` and §9 was unusable from the UI. 202s now parse their body.
- `GET /api/channels` returned channel config verbatim — including the
  Matrix `access_token`. Responses now mask token/secret/password/key-
  shaped config values (storage stays the documented plaintext residual).
- Fresh-install keygen was a chicken-and-egg: compose's `:?` guard on
  APOTHECARY_MASTER_KEY refused to run keygen itself. Docs now use plain
  `docker run … keygen` (also avoids the orphan `deploy_` postgres the
  compose-run path spawned).
- Crash mid-bootstrap bricked the run: no env id is recorded before
  env_ready, so recovery re-provisioned (same workspace) and re-ran
  `git clone` → exit 128 → terminal env_failed. Bootstrap is now
  idempotent (clone-if-absent, switch-or-create branch, dedup'd exclude
  lines), a transient Status error no longer triggers re-provision, and
  `env_failed` carries the env id so capacity counting / archive / the
  reaper finally see (and can destroy) workspaces of failed bootstraps.

**Engine lifecycle**
- RaisePing/AnswerPing no longer block forever when no agent stage is
  draining the mailbox (bounded by run-done + 30s timeout); a finished
  run's pings are pruned (no permanently unanswerable inbox entries);
  processAnswer rolls back its claim if the event append fails.
- Flow drift: a recovered run whose current stage no longer exists in the
  flow now fails honestly instead of reporting run_completed (Index -1 was
  read as "past the end"). Transient flow-source errors leave the run
  recoverable instead of terminal (`flow.ErrNotFound` distinguishes).
- Stage-timeout path collects the cancelled attempt's output for the log
  and abandons a ctx-ignoring adapter after a 30s grace instead of
  wedging; the suspend/resume timer drains stale ticks (no spurious
  instant timeout after a human answers).
- `command` stages with a deleted run_ref fail loudly (empty script used
  to exit 0 and pass gates). Shutdown sets a closing flag so a finishing
  run can't admit a queued run nobody will stop.
- Envcheck: destroy failures now count (named return), the job registry
  never evicts running jobs and the duplicate guard is race-free; engine
  port parses via net/url (scheme defaults) instead of LastIndex(":").
- Provisioner cache is keyed by (integration, template) — two projects
  sharing an integration no longer thrash/close each other's adapters —
  and a stale entry is removed on eviction (double-Close fixed).

**Config plane**
- confstore gained `ErrInvalid`; API statuses are now honest (validation
  400, not-found 404, conflict 409, infra 500 — DB outages were 400s).
  Settings PUT merges over stored values, not compile-time defaults.
- Masked-secret round-trip (`••••last4` written back as the new secret)
  is rejected at validation.
- Fragment validation also rejects SHELL/ONBUILD and trailing `\`
  continuations (which would splice into engine-owned lines, e.g. the
  final `USER apothecary`).
- Migration 0004 adds a NOT VALID FK projects.default_flow → flows(name)
  as the delete/create race backstop; migrations run under a pg advisory
  lock; SSE fan-out drops slow subscribers instead of stalling every
  append while holding the store mutex.

**Adapters/kinds**
- All shell interpolation into workspace scripts is single-quoted
  (`%q` left `$`/backticks live inside double quotes): model strings,
  session ids, agent/role paths, provider.env values (sourced file), and
  provisioner ReadFile/WriteFile paths. Belt-and-braces: agent-spec names must
  be lowercase slugs, flow model strings a strict charset.
- opencode: EgressDomains are now DERIVED from auth_json's provider keys
  (same keys Models() filters by — google/xai/mistral/… map to their API
  hosts) so the firewall can't block a model the UI itself offered; Test
  fails on the missing-but-Required auth_json; first session id wins
  (child sessions no longer hijack Resume); cache-read tokens excluded
  from input accounting; Resume guards empty sessions. Spec listing is
  line-anchored (`===APOTHECARY-SPEC` marker, renamed) and includes .yml.
- codex: same quoting/marker/.yml/Resume-guard fixes.
- github: per-segment path escaping in FileExists (%2F was GitLab's
  convention), draft-PR 422 degrades to a "Draft: "-prefixed regular PR,
  rate-limit 403s are named as such, locator validation rejects URLs and
  strips .git.
- workspace firewall: IPv6 is fail-closed (kernel-has-v6 + failing drop
  now trips the ERR trap; fail_closed also drops v6); release.yml checks
  the REMOTE for the version tag (re-runs were always red).

**Known-and-deliberately-left**
- opencode prompt rides argv (`"$(cat …)"`) with the ~2 MB ARG_MAX
  ceiling; codex uses stdin. Switching opencode to stdin is untested
  against the CLI — revisit with a live run.
- Env-capacity admission is still check-then-create (single-user risk).
