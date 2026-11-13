# Automations: merge_request — a run on each merge request others open

Second pass, step 5 (second half), 2026-10-24.

- **Kind `merge_request`** watches one repository (`repo`, "" = the primary;
  migration 0006 adds the column and widens the kind check). Each pass lists
  the open merge requests (newest first, the watcher's limit) and, for each
  one that is not a draft and not opened by the integration's own account
  (`Engine.RepoHostUser` → `Whoami`: the factory's own runs' merge requests),
  makes a ticket — title "<title> (!<iid> by <author>)", goal the
  description and a line naming the merge request, `source_kind
  merge_request`, key and URL the merge request's, no source id, so no
  tracker: tracker actions skip — and creates one run on it
  (`RunInput.MR`) at the automation's `mr_opened` entrypoint. Once each:
  a ticket with that key and runs is skipped; this automation's ticket
  without a run (a crash between the two) is adopted; a person's ticket for
  the same merge request is theirs. A draft is picked once it is marked
  ready.
- **Entrypoint kinds are validated per automation kind**: merge_request needs an
  `mr_opened` entrypoint, tracker_query and cron a `run_start` one (the
  first of the kind when `phase` is empty). The workshop form offers only
  those (`StartRunOptions entries=…`) and never asks for a merge request
  (`askMR=false`).
- **Migrations 0003/0004 were not re-runnable** (`CREATE TABLE` without `IF
  NOT EXISTS`), against the rule that a post-squash migration is a no-op
  where its result exists. `TestMigrateCatchesUpAnInitEditedInPlace`
  re-applies everything after 0001 and caught it — but it only runs with
  `TEST_POSTGRES_DSN`, so nobody had. Fixed in place (an applied migration
  is never re-run, so the edit reaches only databases that need it); the
  pgstore suite passes on a throwaway Postgres 16.
