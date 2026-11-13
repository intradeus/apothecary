# The checkout is cloned at the run's source branch (2026-11-09)

- Found by a run meant to fix a repo's devcontainer: it built the broken one.
  Provision cloned with a plain `git clone` (the remote's default branch) on
  both hosts (`host_k8s.go ensureCheckout` inside the pod, `devcontainer.go
  ensureSrc` engine-side), detected and built the devcontainer from that
  checkout and ran its lifecycle hooks (`RunUserCommands`, still inside
  Provision), and only then did bootstrap's branch step `git switch -c <run>
  origin/<source>`. The source branch the engine already computed for
  bootstrap (`bootstrap.SourceBranchOr`) never reached the provisioner: the
  `EnvSpec` had no field for it (its `Branch`, the run branch, is read by no
  adapter — it does not exist on the remote when the clone happens).
- Now `EnvSpec.SourceBranch`, set by provision and rebuild from the same
  value bootstrap gets (the environment self-check sets the project's
  default branch, which its bootstrap forks from), and both clone sites pass `--branch <source>` when it
  is set — without `--single-branch`, so `origin/<source>` and the default
  branch stay available to bootstrap (`git switch -c <run> origin/<source>`
  is unchanged) and to the prompt's diffs. A source branch missing on the
  remote fails the clone, loudly, instead of silently building the default.
  The name was checked at run creation (`CheckBranch` on the ticket's source
  branch; a merge-request run's source is the target branch the host
  reported; the fallback is the repository's default branch), and `--`
  already preceded the URL and the path; `--branch`'s own argument is taken
  verbatim by git's option parser.
- Rebuild was less of a gap than it looked: the checkout (with the agent's
  commits, on the run branch) survives a rebuild on both hosts and the
  container is rebuilt from it, so an agent's devcontainer edits already got
  built. Only the clone after a reap (checkout gone) fell back to the default
  branch; it now lands on the source branch like the first one. Cloning the
  pushed run branch instead, for a re-provisioned run, is not done: the
  working tree is gone with the workspace either way, and bootstrap's
  `git switch <run>` picks the pushed branch up when it exists.
- A run created on a merge request forks from `FETCH_HEAD` (the host's head
  ref); its clone lands on the run's source branch — the target — not the
  merge request's branch, whose devcontainer it therefore still does not
  build with. Called acceptable here; it was not (next section: the
  checkout is put on the run's branch before the build, in every case).
