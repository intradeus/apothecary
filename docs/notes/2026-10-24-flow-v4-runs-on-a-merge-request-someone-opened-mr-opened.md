# Flow v4: runs on a merge request someone opened — `mr_opened`

Second pass, step 5 (first half), 2026-10-24: what the reviewer flow needs.

- **An entrypoint signal, not an option.** The plan had `RunInput.MR` on any
  entrypoint; a second Entry signal, `mr_opened` (`Brings: merge_request`),
  says it in the document instead. Parse time then knows the entry phase
  has a subject (`HasSubject`, so `post_review` there warns of nothing) and
  that the run has a merge request without a `create_mr` (`OnMR` in
  `definitelyHasMR` and `phaseShapeWarnings`); run creation holds the kinds
  apart (`entrypointOf(fl, name, onMR)`: an MR at a `run_start` phase, or
  none at an `mr_opened` one, is refused; the default entrypoint is the
  first of the kind asked for). Preflight checks its repository host like an observed
  signal's. `HasSubject` now also excludes `mr_approved` (it holds for every
  merge request, like `mr_merged` — a slip from step 3).
- **Port.** `GetMR`, `ListMRs` (open, newest first, ≤ 300), `Whoami` (for
  the automation). `MRInfo.HeadRef` is the repository host's own ref for the head
  (`refs/pull/<n>/head`, `refs/merge-requests/<iid>/head`), which works for
  forks too; `Fork` = head repository ≠ base.
- **Creation** (`adoptMR`): the merge request must be open; its checkout's
  branch is its source branch (a fork's, or a name `CheckBranch` refuses,
  becomes `mr-<iid>`), its source the target, `Checkout.Fetch` the head
  ref, `Checkout.Fork` the flag. `run_created.mr` records it (repo, iid,
  url, head, author, title); the projection adds it to `mrs` (with its
  author) and `adopted`; replay adds the merge request record — with its
  head known, so `mr_head_moved` fires only on the author's later pushes.
- **Bootstrap** (`branchScript`): an existing branch is re-entered, else
  `git fetch origin <ref> && git switch -c <branch> FETCH_HEAD`. For a
  same-repository branch `git switch <branch>` already succeeds by creating
  it from `origin/<branch>` — the same commit.
- **Subject from the start.** `beginRun` begins an `mr_opened` phase with
  the merge request as subject and its creation head as tip. `followAdopted`
  runs before a phase run's first workspace stage: `git fetch <ref> && git
  merge --ff-only FETCH_HEAD` once per starting tip — the author's branch
  moves under the run; a checkout that diverged (the run committed) is left
  alone. `push_branch` refuses a fork's checkout (routable).
- **API/UI.** `mr` on the start-run and rerun bodies; `GET
  /api/projects/{id}/mrs?repo=`; the start form shows a picker of open merge
  requests when the chosen entrypoint is on `mr_opened`; the editor offers
  the entrypoint; a rerun stays on the merge request (refused once it is no
  longer open).
