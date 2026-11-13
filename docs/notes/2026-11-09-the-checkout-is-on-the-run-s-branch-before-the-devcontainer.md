# The checkout is on the run's branch before the devcontainer build (2026-11-09)

- The previous section's `--branch <source>` fixed only the new-run case. A
  run created on a merge request works on an existing branch, the merge
  request's head: its run branch is that branch, its source branch the merge
  request's target. The clone still landed on the target and built the
  target's devcontainer; the merge request's branch arrived with bootstrap,
  after the build. Found by a run opened on a merge request that fixes the
  devcontainer: it failed building main's, before bootstrap ever ran (the
  checkout's reflog held only the clone).
- Now the provisioner runs bootstrap's own branch step on the checkout,
  right after the clone and before `devcontainer up`, so the checkout is on
  exactly the commit the run works on in every case: a merge request's run
  switches to its branch (or fetches the host's head ref, the only way to a
  fork's head), a rerun re-enters the pushed branch (`git switch <branch>`
  takes the remote's up), a new run forks `origin/<source>`. The clone picks
  no branch of its own any more. Bootstrap's step, unchanged, finds the
  branch and re-enters it.
- One implementation, in a new leaf `internal/workspace/checkout`
  (`BranchScript`, `CredentialLine`; archtest row, shell only), used by both
  hosts' clone steps and by bootstrap, which had its own copies — and so had
  the adapters, of the credential line. Moving it also moved the branch
  names from Go quoting (`%q`) to shell quoting.
- `EnvSpec` gains `Fetch` beside `Branch` and `SourceBranch` (the run's
  primary checkout's fetch ref, `exec.primaryFetch` in provision; the view's
  primary checkout in rebuild, which also now passes it to its bootstrap —
  it did not, so a rebuild whose checkout was reaped would have forked a
  merge request's run off its target). An empty `Branch` (a bare caller)
  leaves the clone where it lands.
- The credential now reaches every git command of the clone script, not
  only the clone: a merge request's head is fetched with it. Both hosts name
  the store file through git's environment config
  (`GIT_CONFIG_COUNT/KEY_0/VALUE_0`, git ≥ 2.31), never argv, never the
  checkout's config; the file is removed when the script ends, as before.
  The Docker host runs the clone and the branch step as one `sh -c` script
  (the same single runner call its tests queue a response for); the
  Kubernetes host appends the step to the bash script that already carried
  the credential.
- Tests: both adapters assert the exact script for a new run, a merge
  request's run and no branch (`TestEnsureSrcEntersTheRunBranch`,
  `TestK8sEnsureCheckout`); the engine asserts the spec the provisioner
  received for a merge request's run (`TestRunOnSomeonesMergeRequest`:
  branch, source, fetch) and for a ticket run (`TestRunSourceBranch`); the
  rebuild test asserts its spec and the view helpers. A check that the
  checkout's HEAD equals the merge request's head before the build needs a
  real remote: that is the docker integration suite, CI only.
