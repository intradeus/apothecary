# The README, rewritten short (2026-11-13)

- **Why:** it had grown to about 1,000 lines and drifted from the code. It
  claimed humans at exactly two touchpoints, a library of five compiled-in
  agents (agents live in each flow), toolchain presets and a Dockerfile
  fragment (gone: the image is the repo's devcontainer), a *default* flow
  template (the built-ins are builder, reviewer, conflict-resolver,
  hands-on, workspace-only), a five-variable boot, `APOTHECARY_BASE_IMAGE`
  (no longer read) and `docs/DESIGN.md`/`DESIGN2.md` (gone). It said
  nothing about workshops, automations, merge requests as work items,
  providers, skills, analytics or the run desk.
- **Now** the README holds what the product is, its features, how a run
  works, security, install, configure and development, as summaries. The
  reference moved, nearly verbatim, into `docs/`:
  - `docs/kubernetes.md`: cluster objects and requirements (sysbox,
    AppArmor, EKS), a local k3d cluster.
  - `docs/operations.md`: both CLIs (now with `apothecary version`), the
    environment (corrected), probes and telemetry, reaching a run, SSH and
    VS Code, the image cache (its three caches named), releases.
  - `docs/ticket-sources.md`: the trackers, Jira tokens and scopes.
- The headings code and messages point at stay in the README: "Unprivileged
  sandboxes" (the daemon door's refusal), "Cluster requirements" (the
  Kubernetes integration's help), "Docker in workspaces", "Developing
  Apothecary with Apothecary".
