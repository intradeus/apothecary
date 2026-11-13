# Workspace composability & dynamic envcheck (M9, DESIGN2 §8–§9)

- **Single-copy embeds via `factory/workspace`**: the base-image context
  (Dockerfile + shim + firewall) and the workspace template are `go:embed`ed by
  a package living IN workspace/ — no duplicated files, no cross-directory
  embed gymnastics. Base tag = `apothecary/base:<sha16 of embedded tree>`;
  template version = `v<sha16 of template tree>`.
- **The base image lost Go, opencode and build-essential** — toolchains are
  fragments/presets, the harness is `InstallLayer()`. Node stays (shim
  runtime + npm-installed harnesses). Existing fragments referencing the
  old base ARGs (Lu's `${GO_VERSION}`) must pin versions explicitly — the
  presets do.
- **Composition & tags**: `wsimage.Compose` renders FROM base → USER root →
  fragment → harness layer → USER factory; `Tag` hashes the composed file
  (which embeds the base ref), so every input change moves the tag and
  identical config is a docker-side no-op. Builds run one-at-a-time
  (`docker build` via the socket, temp-dir context — no tar-stdin), logs to
  `runs_dir/builds/<tag>.log`.
- **image ensure is engine-side** (§8.3): between env_requested and
  provision; failure ⇒ deterministic `env_failed{image build: …}`; the new
  `image_built{tag, cached}` event may repeat on recovery re-entries
  (append-only log; projection ignores it).
- **Template push**: `EnsureTemplate` compares the provisioner's ACTIVE
  version name against `v<sha16>` (CLI `templates versions list -o json`)
  and pushes on drift, once per process. New params `workspace_image` /
  `extra_allowed_domains` carry defaults, so pre-M9 creates (custom path:
  v1 params only) keep working against the new active version. Fresh
  deployments fall back to `templates create` when push reports the
  template missing.
- **Firewall names removed from the script**: init-firewall.sh keeps only
  mechanics; the allowlist arrives fully derived via APOTHECARY_EXTRA_DOMAINS
  (repo host kept as belt-and-braces for custom templates).
- **workspace.template is optional again** (the M7 stopgap requirement is
  gone): empty = managed template + built image; set = custom-template
  escape hatch, no pipeline, no push, v1 params.
- **Dynamic envcheck**: `engine.RunEnvCheck` derives the §9 catalog from
  the Resolution (one egress step per domain, harness binary, image step
  on the pipeline path) and is shared by the CLI (`envcheck -project`) and
  the API (`POST /api/projects/{id}/envcheck` → 202 + poll
  `GET /api/envchecks/{id}`; in-memory last 20). **Deviations from the §9/
  §10 sketch, recorded**: the UI POLLS instead of SSE (2 s; check runs are
  minutes long — SSE machinery wasn't worth it yet), and the manual
  `build-image` endpoint is deferred (the envcheck image step and the run
  path cover builds; logs land in runs_dir/builds).
- UI: preset insert buttons (append visible fragment lines + their egress
  domains), the template field demoted to "advanced, usually empty", and
  an Envcheck panel (timeline of steps, keep-workspace toggle) on the
  project page.
- **Firewall fail-open bug (found by the acceptance run, fixed)**: an
  unresolvable allowlist name aborted init-firewall.sh at the `getent`
  pipeline (set -e) BEFORE any rule existed — the workspace came up with
  open egress, which envcheck loudly caught (three failing deny steps).
  Two fixes: `|| true` on the resolution pipeline (a typo'd domain is
  simply not allowed; its egress step fails by name), and an ERR trap that
  slams egress shut (loopback + established only, no lock
  file) on any unexpected script failure — fail-closed by construction.
- **M9 acceptance evidence** (live, Lu's machine, deployed stack):
  greeter-factory-test (Go+make fragment) full envcheck PASS incl.
  `verify` (`make verify` on the real repo) — first pass built
  apothecary/base:e6830826… + ws:f9f248db… (cached=false), re-check
  cached=true; py-smoke (python preset) PASS on its own image
  (ws:987b4243…, base layer shared); the engine pushed template version
  vc101fd0f… and provisioned real workspaces from the built tags through
  the new parameters; induced failures produced named steps —
  `egress:doesnotexist.invalid` (deny checks green, firewall held) and a
  wrong provisioner token surfacing its 401 through the Test endpoint. The make-not-found first attempt was itself the composability
  working: the stripped base genuinely lacks what fragments don't add.
