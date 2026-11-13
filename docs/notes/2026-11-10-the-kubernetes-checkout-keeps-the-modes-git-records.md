# The Kubernetes checkout keeps the modes git records (2026-11-10)

- Every file of a Kubernetes run's checkout was 0600 or 0700: `ensureCheckout`
  (`host_k8s.go`) set `umask 077` for the credential file as a plain
  statement of its script, so it held for `git clone` and the branch step
  too. A compose sibling running as a non-root user (a seed container
  bind-mounting the repo's `entrypoint.sh`, recorded 100755) failed with
  "Permission denied". There since Kubernetes support; runs failed earlier
  before. The Docker host never had it: it writes the credential file with
  an explicit 0600 from Go, and git runs under the engine's umask.
- Now only the credential write runs under 077, in a subshell; git writes
  under the pod's umask. `TestK8sCheckoutKeepsTrackedModes` runs the
  generated script for real (local bash and git, a `file://localhost`
  remote, a `git` wrapper on PATH that records the credential file's mode
  mid-script): 0755 for the executable, 0644 for the rest, 0600 for the
  credential, which is gone afterwards. It failed with 0700/0600 before the
  fix.
- Existing checkouts stay as they were: the clone is skipped when one
  exists, git does not repair the modes of files it already wrote, and a
  rebuild keeps the checkout. An affected run needs its workspace destroyed
  and provisioned again, or a one-time chmod of the checkout.
