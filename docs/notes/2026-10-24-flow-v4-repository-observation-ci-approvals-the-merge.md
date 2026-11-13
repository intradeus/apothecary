# Flow v4: repository observation — CI, approvals, the merge verdict, others' pushes

Second pass, step 3 (2026-10-24). Four signals, each one registry entry and
one predicate in the idle loop; the scheduler, the policies and the
occurrence keys did not change.

- **Port.** `MRStatus` stays one repository host call and gains what that call already
  carries: `MergeableNow` (GitHub `mergeable_state` clean/has_hooks and not a
  draft; GitLab `detailed_merge_status == mergeable`) and `Draft`. Checks and
  approvals are two new methods, `MRChecks(mr, head)` and `MRApproval(mr)`,
  so a flow that reads neither never pays for them (`readsOf` scans phase
  heads, wait stages and gate auto-answers; `fake.RepoHost.ChecksCalls` holds
  the line in a test). Plan deviation: no `UpdatedAt` — nothing needed it.
- **Checks.** GitHub: check runs (newest run of a name wins) plus combined
  commit statuses; GitLab: the head pipeline (pending when it is an older
  commit's), its failed jobs named when it failed, `allow_failure` jobs
  ignored. Cancelled/skipped/manual are `neutral`: no failure. The idle loop
  caches checks per head and asks again when the head moves, while they run,
  for a short while after a push when none are reported yet, and otherwise
  at the thread cadence.
- **Approval.** GitHub: each reviewer's latest decisive review (APPROVED,
  CHANGES_REQUESTED, DISMISSED); approved = someone approves and nobody
  requests changes. GitLab: `approved` and at least one `approved_by` —
  without approval rules GitLab says approved with nobody's approval.
- **Signals.** `ci_failed` [head:failed-names], `mr_mergeable` [head],
  `mr_head_moved` [head, debounced: `quiet_for` counts from when the idle
  loop first saw the head — memory only, so a restart or the end of a phase
  run restarts the quiet period], and `mr_approved`, an outcome (and
  waitable): every merge request approved or merged, impossible once one is
  closed.
- **Whose push.** `runState.heads` is what the run knows per checkout: every
  `push_branch` tip, the merge request's head at creation or adoption (new
  `mr_created.head`, read with one `MRStatus` call; best effort), and every
  phase run's starting tip. A head outside it is someone else's. A `git
  push` from a command stage is not recorded — such a flow would see its own
  push as someone else's; `push_branch` is the publishing primitive.
- **UI.** `MRLive` carries `checks`, `approval`, `mergeable_now`, `draft`
  (read for open merge requests); the MR status line says "ready to merge",
  "draft", "CI passed/running/failed: names" (the long form links the failed
  check) and "approved by …"; "mergeable" now reads "no conflicts", which is
  all it ever meant.
