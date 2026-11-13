# System notices (TODO 51, 2026-11-02)

Problems with apothecary itself that a person must act on, as opposed to run
pings (run-scoped, unchanged). Before this, these only logged or set a
status nobody watched (a disabled credential, a ticket's `sync_error`).

- **Domain** (`internal/notices`): a `Notice` belongs to one user and stays
  until that user dismisses it (manual dismiss only). A non-empty key
  deduplicates while uncleared: a repeat bumps `count` and `last_seen_at` and
  takes the new title/body/link. A clear re-arms the key and never hides
  anything; a dismissed, uncleared notice keeps absorbing repeats, so
  dismissing a persistent failure silences it until it clears and breaks
  again. `Service.Raise` never fails its caller (store errors log) and fans
  out to owners and/or admins (`Admin && !Disabled()`, an empty owner means
  the admins), each user once. A *new* notice is mirrored to the recipient's
  channels asynchronously, 30 s per send; `channel_failing` never is (it is
  about the mirror). `catalog.go` holds the exact wording of every kind.
- **Store**: migration `0023_notices.sql`; the dedupe is one
  `INSERT … ON CONFLICT` on a partial unique index over `(user_id, key)`
  where uncleared, `xmax = 0` telling insert from update. Rows go with their
  user; nothing prunes them. `noticestest` is the contract both stores pass.
- **Producers** (each a small interface in its own package, wired in
  `cmd/apothecaryd/wire.go`): the login keeper (refused for good → sign in
  again; failed and expired → expired; refreshed clears both); the ticket
  poll (`Syncer.RunOnce` only: an unreachable tracker or unresolved
  integration raises, a pass that synced ≥1 ticket clears; rate limits do
  neither); `confstore.EnsureRemoteStatuses/Types` (every inserted row,
  first fill included, one aggregated notice per call, no key); every
  channel send (`channels.Registry.Send`); the docker host's tools-bundle
  warm-up and browser-image build; Kubernetes shim/tools hash drift (owner
  and admins; a failed read says nothing); the SSH door stopping; the reaper's
  workspace destroys (`Engine.ReapOnce`) and storage clean-up
  (`background.go`).
- **Deliberately not notices**: environment capacity (keeps its channel-only
  blocker), the automation blocker and "cannot reach the repository" (project
  setup tier), fatal boot errors, tracker rate limits, synchronous user
  actions, run failures.
- **API and UI**: `GET /api/notices`, `POST /api/notices/{id}/dismiss`
  (`ScopeNotice`), `POST /api/notices/dismiss`. The CPUI's Notices page
  (`/notices`, after Inbox in the nav, with a count badge) lists them as
  banners with an Open action when the notice links somewhere. Titles and
  bodies are server-side English, like ping titles.
