# Accounts: multi-user, no organisation level (2026-10-27, TODO 25)

The plan started as guilds — organisations with members and roles, the
shape the UI redesign had sketched. Lu dropped it on reflection: a solo
install wants one account, a team wants one account per person, and the
"shared" things a team would have (one project row per repo, a review
automation that fires once per MR, covering each other's pings) are worth
less than roles, invites and a cross-guild desk cost. So the product is
multi-USER. There is no org table, no role matrix; `user_id` goes on almost
every row, every list is the caller's, every item route is the owner's or
a 404. "Add a collaborator to a project" is the cheap primitive if sharing
is ever wanted — and ownership is an explicit column, not an implicit
"everything is mine", so that door stays open.

### What landed

- **`internal/auth`**: the profile (`users`), a pluggable method table
  (`auth_methods`: kind = password today; OAuth and passkeys are more rows
  of the same shape with a `Verifier` per kind), browser `sessions` and
  personal `api_tokens`, both stored as sha256 of the secret. Passwords are
  argon2id in PHC format (parameters travel with the hash). `Setup` creates
  the first account as admin. Migration `0013_users.sql` — new installs
  only: the product is not shipped anywhere, so nothing in the code claims
  rows made before accounts existed. Lu's own database was assigned to his
  account by hand, once, with the same statements an adopter would have run
  (every `user_id=''` row, plus the `run_created` payloads so a projection
  rebuild keeps the owner — precedent 0012).
- **Ownership**: `user_id` on integrations, projects, tickets (copied from
  the project on every write), channels, attachments (the uploader), the
  runs and pings projections (from `run_created.user_id`, which the engine
  fills from `Resolution.UserID` = the project's owner — so automations'
  runs are the project owner's too). Names are unique per owner, not per
  instance. Settings are per user; the capacity caps left the table for the
  boot env (`APOTHECARY_MAX_CONCURRENT_RUNS`, `APOTHECARY_MAX_ENVIRONMENTS`,
  `-1` = no cap) because they protect the host, not a preference.
- **The guard** (`httpapi/auth.go`): every route in the table declares its
  `Auth` (user by default, admin, run token, public) and `Scope` (which
  resource its path names). One wrapper resolves the principal (bearer
  `apo_…` token, else the session cookie), refuses cookie writes from
  another origin (`Sec-Fetch-Site`, then `Origin`), and answers 404 for a
  resource whose owner is not the caller. `TestEveryRouteDeclaresItsAccess`
  keeps a new endpoint from being open by accident; the public allowlist is
  three routes. This also closes the old gap where the devcontainer bridge
  pumped the whole unauthenticated API into every workspace: the run token
  still reaches only its own run's four routes, and nothing else answers.
- **Reaper and retention per owner**: `ReapOnce` takes a window-per-user
  function, orphan GC runs per owner, and the blob sweep's mark set takes a
  cutoff per owner (`LiveBlobSHAs(map)` via `unnest`).
- **Notifications go to the owner's channels**: `ports.Notification.UserID`,
  filled by `Engine.notify` from the run when the caller did not; a
  channel with no owner (tests, dev mode) receives everything.
- **Host resources keyed by project id** (not name): gateway container,
  policy dir, cache volumes, browser container, docker labels
  (`apothecary.project=<id>` + `apothecary.project.name=<name>` for
  `docker ps`), the gwproject marker and the build-cache registry path —
  through one helper, `ports.ProjectHostKey`. Two users with a project
  called "api" no longer share a gateway; a rename no longer moves one. The
  name is deliberately NOT a `--id-label` on the devcontainer container: the
  CLI matches on every id-label, so a renamed project would stop matching
  its own workspace.
- **CLI**: `apothecaryd user list | reset-password | set-admin` — the way
  back in after a lockout.
- **UI**: the `/g/[guild]` segment is gone (old links redirect); `$lib/urls`
  builds paths; `$lib/session.svelte` gates the shell on the boot check
  (setup page → sign-in page → app) and a 401 interceptor drops back to
  sign-in in place. The sidebar's guild switcher is a user menu (Account,
  Users for admins, Sign out); System gained Account (profile, password,
  tokens) and Users (admin), Limits is read-only, Members is gone.

### Deliberate residuals

- One master key for everyone's secrets: per-user keys would be envelope
  encryption while one process holds every key in memory anyway.
- The engine speaks plain HTTP and the workspace-port relay opens raw host
  ports with no auth: fine on loopback, a leak on a network. TLS in front
  and an authenticated app proxy (wildcard subdomain, session cookie on the
  parent domain) are the next step before remote users get exposed ports.
- `apothecaryd user reset-password` echoes the typed password (no raw-mode
  dependency); `-password` exists for scripts.
- Any user may answer any of their own pings; there is no assignment, and
  no one can answer another person's — the run waits for its owner.
- The desk layout stays in localStorage per browser, not per user.
