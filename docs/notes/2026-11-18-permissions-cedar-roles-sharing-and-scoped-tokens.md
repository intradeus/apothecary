# Permissions: Cedar, roles, sharing and scoped tokens

Amends `2026-10-27-accounts-multi-user-no-organisation-level.md`: sharing a
workshop is now a primitive, and the admin flag is gone (an instance role
replaces it). There is still no organisation level.

Why: access was two hard-coded checks — `User.Admin`, and "the path's
resource is the caller's" — and a personal token was the whole account.
The owner asked for a real permission system on an existing library, wide
enough for scoped tokens (MCP tokens come next), workshop sharing and
instance roles (run journal, plan#1: scope `tokens_sharing_roles`, token
grain `actions_projects`, library `cedar`).

## The library

| Candidate | Verdict |
|---|---|
| **Cedar** (`github.com/cedar-policy/cedar-go`, Apache-2.0) | Chosen. In process, pure Go, typed entities, deny by default, `forbid` beats `permit`, an entity hierarchy (`in`). The rules are reviewable text we ship; the facts are entities built per request from Postgres. No new service, no second store to keep consistent. |
| Casbin | Strings for the model and the matchers; the Postgres adapters pull in gorm; ownership + workshop roles + a token intersection all need custom matchers. |
| OpenFGA, SpiceDB, Ory Keto, Permify | Zanzibar servers: a new service in the chart and dual writes of every owner column and membership. Too heavy for one instance. |
| OPA / Rego | A large dependency and a Datalog dialect for a small model. |

Gaps, and why they do not bite: cedar-go has no policy templates, so a
token's grant is request context (the actions and workshops it holds),
never a per-token policy. Its schema validator lives in `x/exp` (breaking
changes allowed); it is used only in `TestVocabularyIsClosed`, and the
policy matrix is the real guard.

## The model

- **Actions** are one closed vocabulary, `kind:verb`
  (`internal/account/authz/actions.go`): `instance:admin`; `account:read`,
  `account:manage`; `project:create|read|write|members|delete`;
  `run:read|operate|shell`; `ticket:read|write`; and `read|write` of each
  personal kind (integration, credential, channel, skill, analytics,
  notice, settings). Every user route declares exactly one (`Route.Action`,
  emitted in the spec as `x-apothecary-action`); `GET /api/permissions`
  serves the list. The terminal, the port tunnel and the SSH tunnel —
  outside the table — declare `run:shell`.
- **Instance roles** (`users.role`, migration 0010): `admin` is a member
  who also holds `instance:admin` — and nothing else: no rule grants an
  admin a view into anyone's work. `member` is today's user. `guest`
  creates no workshop, integration, credential, skill or channel (403); it
  has what is shared with it, and its own account, tokens and keys. The
  last-admin rule carries over to the role, and is now the store's: a
  demotion or a disable that may take an enabled admin away locks every
  enabled admin's row in the same transaction as its write, so of two at
  once one is refused (`auth.ErrLastAdmin`). A profile edit, a role change
  and a disable each write their own columns (`UpdateUserProfile`,
  `SetUserRole`, `SetUserDisabled`; the whole-row `UpdateUser` is gone): an
  edit in flight cannot write back the role or state it read.
  `apothecaryd user set-role` replaces `set-admin`.
- **Workshop roles** (`project_members`, migration 0011): viewer <
  operator < maintainer; the owner (`projects.user_id`) is implicit and
  above all three. A role covers the workshop and what lives in it: its
  runs, their pings, snapshots and claimed uploads, its tickets and their
  files, envchecks.
  viewer reads; operator also runs (start, abort, retry, archive, answer,
  ask, take over, egress allow) and writes tickets; maintainer also
  changes settings, flows, automations and secrets, shares (up to
  maintainer) and opens a shell. Only the owner deletes the workshop and
  adds, changes or removes a maintainer. Personal configuration is never
  shared. A ticket or a run a collaborator writes or starts is still the
  owner's; the files they attach are their own uploads, claimed as theirs
  (never as the owner's on their behalf), and a ticket's files are the
  ticket's to read.
- **Tokens** (`api_tokens.actions`, `.project_ids`, migration 0012): NULL
  actions is a full token — every token minted before, and the CLI's
  sign-in. A scoped token holds a non-empty action list and, optionally,
  some workshops. Each request is the intersection of what the account may
  do now and what the token holds: a role or a membership lost narrows the
  token at once. A scoped token never holds `account:manage`, so it can
  neither mint a wider token nor change the password. A token limited to
  workshops reaches a workshop's kind of resource (workshop, run, ticket)
  only inside them — one in no workshop (a run from before workshops, an
  upload not claimed yet) is outside every limit; personal configuration is
  in no workshop and stays the token's to the extent of its actions.
- **Answers**: a resource the caller may not read (its account's reach and
  the token's workshops) is 404, as before; one it may read but not act on
  is 403 naming the action, as is a route its role or token does not hold.
- **Where each check lives**: Cedar decides point requests in the guard
  (`authz.Decide`, one resource), the SSH door and every request an
  exposed app's cookie makes (`run:shell` for the account the cookie was
  issued to, so a member removed, demoted or disabled loses the app on its
  next request). Lists stay
  SQL: `authz.Visibility` asks the same policies, workshop by workshop,
  which workshops a principal may read a kind in, and every list filters
  on the caller's own rows plus those workshops (`run.Seen`, `ListPings`,
  `ListVisibleProjects`, the flow overview, analytics over project ids).

## Deviations from the plan

- `auth` cannot import `authz` (authz imports auth), so `auth.Service`
  validates a grant through `CheckGrant` (wired to `authz.ValidateGrant`);
  nil refuses every scoped token.
- Leaving a workshop is its own route, `DELETE
  /api/projects/{id}/members/me` (`project:read`), so removing someone
  else stays `project:members`.
- `GET /api/tickets/{id}` resolves a ticket's workshop as the first of its
  owner's workshops on the ticket's source that the caller is a member of.
- Analytics takes the visible project ids only (as planned): a run whose
  workshop was deleted no longer counts in its owner's analytics.

## Residuals

- A run on a shared workshop stays the owner's: their concurrency budget,
  notifications, retention window, integrations and credentials. A
  collaborator's activity counts against the owner, and disabling the
  owner stands down runs collaborators started.
- A workspace holds the owner's injected repository and model
  credentials, so `run:shell` (terminal, editor, exposed ports, port
  tunnel, SSH) is a maintainer's. Ask and take-over can read workspace
  files too and sit at operator: "operator" means trusted with the
  workshop's credentials in practice.
- Adding a member by handle or email tells anyone holding
  `project:members` whether an account exists. Acceptable inside one
  instance.
- The SSH tunnel's door authorizes the key's account, not the HTTP token
  that opened the tunnel: a scoped token holding `run:shell` reaches every
  workspace its account's keys may.

## Tests that hold it

`internal/account/authz`: `TestPolicyMatrix` (the spec: instance role ×
relation × action, with the 404/403 split), `TestAdminSeesNoOneElsesWork`,
`TestTokenIsAnIntersection`, `TestVisibility`, `TestVocabularyIsClosed`
(schema validation of the policies, entities and requests). `httpapi`:
`TestEveryRouteDeclaresItsAccess`, `TestSharedWorkshop`,
`TestMembersRoutes`, `TestGuestRole`, `TestScopedTokenLifecycle`,
`TestListsEqualTheGuard`, and the existing guard walks unchanged in what
they assert; `TestAppGrantFollowsTheMembership`,
`TestOperatorsAttachTheirOwnUploads`. Stores: `authtest` (role and grant
round-trips, each user write writing its own columns, the last enabled
admin under two simultaneous demotions), `conftest` (members),
`TestListsSeeByWorkshop`, the three migration tests; `auth`:
`TestAProfileEditCannotUndoARoleChange`. `sshdoor`:
`TestDoorSharedWorkshop`.
