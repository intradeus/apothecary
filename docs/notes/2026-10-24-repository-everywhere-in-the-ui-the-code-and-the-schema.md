# "Repository" everywhere — in the UI, the code and the schema

2026-10-24: the old name of the repository port meant nothing to anyone, said
the user, first for the UI and then "a full rewrite — migrations, internal…".
It is gone from the repo:

- **Vocabulary.** People read *repository*: the workshop's **Repositories**
  tab (`?tab=repos`, `ReposTab.svelte`), the Integrations group, the project's
  *repository account*, run-page and engine messages ("no integration reaches
  repository …", "needs a repository integration (GitLab, GitHub)", health
  step `repo_access`). `portLabel()` (`format.ts`) prints ports, never raw.
- **Code.** The port is `ports.RepoHost` (`res.RepoHost`, `RepoHosts` per
  extra repo, `repoHostFor`); kinds are `RepoHostKind` / `RepoAccount` /
  `RegisterRepoHost`; fakes `fake.RepoHost`, `kinds.FakeRepoHost(Of)`; the
  account's user `RepoHostUser`. A host-neutral word where *repo* alone would
  be ambiguous: `RepoRef` is one repository, `RepoHost` the service holding
  them.
- **Stored and API values.** Port value `repository` (integrations.port, a
  spec's `requires`); the project column and field `repo_account_id`
  (`repoAccountId` in the UI); the automation kind `merge_request` (its
  constant `AutomationMergeRequest`). The parser no longer names the retired
  stage type that opened a merge request — a stray one gets "unknown type".
- **Migrations edited in place.** `0001_init.sql` (the column, comments) and
  `0006_merge_request_automations.sql` (renamed; the kind check) carry the new
  names and `TestInitMigrationIsFrozen` holds the new hash. That breaks the
  freeze's promise on purpose (noted at `preSquashHead`): a database created
  before this edit does not match the code. The project is unreleased, so such
  a database is reset (`task dev:db:reset`) or upgraded once by hand — renaming
  the column and its foreign key, rewriting the port and kind values, and
  re-adding the kind check — never by a migration that would keep the old
  names in the repo.
