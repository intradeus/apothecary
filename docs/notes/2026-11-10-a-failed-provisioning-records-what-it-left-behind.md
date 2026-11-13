# A failed provisioning records what it left behind (2026-11-10)

- Runs whose workspace failed to provision could never be cleaned up. Both
  hosts create the sandbox (Kubernetes: the pod, its PVC, two secrets, a
  NetworkPolicy; Docker: the sandbox container and its volume) and the
  checkout before `devcontainer up`; when a later step failed, `Provision`
  returned no id, `env_failed` carried none, and archive (`v.EnvID != ""`)
  destroyed nothing. Found with twelve archived runs whose sandboxes still
  ran their full compose stacks and exhausted the host's async I/O pool.
- The port's contract now says it: a failed `Provision` returns, with the
  error, the id of what it left behind, or "" when it created nothing. The
  devcontainer adapter returns the workspace name on every failure past the
  name (Destroy tolerates a name with nothing behind it: Kubernetes deletes
  with `--ignore-not-found`, Docker finds no sandbox and removes the
  folder). The engine records it rather than guessing it from `spec.Name`:
  the name is an adapter convention the fake provisioner does not follow.
- `env_failed` gains `partial`: the id names leftovers, never a ready
  workspace. The run view keeps it (archive destroys it, the capacity count
  sees it, the run page shows it for inspection until then); the run's own
  state ignores it, so a retry provisions afresh under the same name
  instead of running in a container that was never bootstrapped. The
  bootstrap and egress-probe failures keep recording a non-partial id, as
  before.
- The same leak had a second door: a run aborted mid-provisioning ended
  abandoned with nothing recorded. An abort now records the leftovers the
  same way (`provisionStopped`); a pause or a shutdown records nothing, since
  the run provisions again under the same name when it resumes and the
  provisioner picks them up.
- The reaper is unchanged: it destroys completed runs' workspaces only, and
  failed or abandoned runs are never auto-reaped (they are evidence).
  Archive is what reclaims them.
- Tests: every Kubernetes failure stage returns the name and destroying it
  deletes the pod, the claim, the policy and the secret; every Docker
  failure stage returns the name; the engine records a partial leftover and
  archive destroys it (`TestFailedProvisionLeftoversAreArchived`), likewise
  after an abort (`TestAbortDuringProvisioningRecordsTheLeftovers`); the
  run-state fold ignores a partial id and keeps a bootstrapped one.
- Runs that failed before this release have no id on record. Their
  workspaces are removed by hand (`kubectl delete pod,pvc,secret,networkpolicy`
  by the workspace name, or `docker rm -f` on the sandbox).
