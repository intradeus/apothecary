# Several repositories (multi-repo tasks)

One ticket sometimes needs changes in more than one repository — an API and
the client that consumes it, a library and the service that pins it. The
engine's unit of safety is the run, so the question was how one run owns
several branches and several merge requests without the agent taking over
any of the publication.

### The model

A project may list **additional repositories** beside its primary
(`Project.repos`: name, URL, default branch, optional repository host locator;
migration `0024_project_repos`). A run records a **checkout** per repository
in `run_created` — the primary at `~/repo`, the extras at `~/repos/<name>` —
all on the same branch name, each forked from its own default branch. Runs
older than this carry no checkout list and replay as exactly the primary.
From there:

- **bootstrap** clones and branches each one (`git switch <branch> ||
  git switch -c <branch> origin/<source>`, so a branch that already exists on
  the remote is tracked, as the primary's is), with a credential line per
  distinct host and the same git identity; the egress allowlist derives every
  host;
- the **prompt** carries a `# REPOSITORIES` section when there is more than
  one checkout, naming each one's path, branch and source, and `$CHANGED` is
  grouped per repository;
- a **command stage** marked `each_repo: true` (command stages only; the
  parser refuses it elsewhere) runs its script once per checkout, primary
  first, failing on the first non-zero exit with the repository named — the
  default template's `commit` and `push` say it, and the commit message file
  is addressed in the primary (`$HOME/repo/artifacts/commit.md`) since that is
  where the committer wrote it;
- **`create_mr`** opens one draft merge request per repository the run
  **changed** (`git rev-list --count origin/<source>..HEAD > 0`; an untouched
  extra gets none), each onto the branch that checkout forked from. It
  reconciles per checkout through the repository host (`FindMR`) *before* reading the
  body, so a crash in the record window never duplicates one; `mr_created`
  now carries `repo` and `target`. A repository listed **without a locator**
  has no repository host and is **context-only**: cloned for the agent to read, never
  published;
- the **gate** lists every merge request in its context (`$MR_URL` stays the
  first); `mr_merged` fires when *all* are merged and `mr_closed` when *any*
  is closed unmerged;
- the **sidecar** takes `--repos` and every workspace route takes `?repo=`
  (`GET /api/runs/{id}/workspace/repos` lists the checkouts), so the run
  page's *Changes*, *Files* and *Terminal* tabs switch repository.

### What the engine owns

Nothing changed in the division of labour: the agent writes code in whatever
checkouts the task needs; committing and pushing are command stages, the
merge requests are an action. The push is a plain `git push -u origin HEAD`
per checkout — the branch is the run's own, so a remote that moved is a
refused push to look at, never something to overwrite.

### API and UI

`Run.mrs` (repo, branch, target, url, iid) and `Run.checkouts` join
`Run.mr_url` (kept: the first one); `Project.repos`; `DocStage.each_repo`;
`mr_created` shows its target in the timeline. The run page shows
`MR 1/2 ↗ MR 2/2 ↗` instead of one link when there are several, and a
repository picker on the workspace tabs. The project form has an
*Additional repositories* section, with a "+ extra" shortcut on the
repository browser.

### Stacked changes: deliberately not in the main flow

An engine-owned stacked-change mode (layers planned by the planner, a branch
and merge request per layer, restack on rework) was built and then removed
before shipping. The reason is product shape, not feasibility: it put a loop
and a second git discipline inside the flow that every run follows, and made
the common case (one branch, one merge request per repository) carry the
machinery of the rare one. The direction instead is a **separate flow applied
to a finished branch** — split it into reviewable merge requests after the
fact, as its own run with its own gates — which keeps the per-run pipeline
simple and lets the split itself be reviewed. Pieces that the stack needed and
that stayed because multi-repo needed them too: `mr_created.repo/target`, the
run's merge-request list, the gate listing every merge request, and the
all-merged / any-closed signal rules.

### Residuals

- The extra repositories share the primary's devcontainer, so its image must
  carry their toolchains; the project form says so.
- One merge request description (`artifacts/mr.md`) serves every repository's
  merge request; per-repository descriptions are not supported.
- Stacked changes and several repositories were never combined; a later
  "split this branch" flow starts from one repository.
