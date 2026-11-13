# Skills and skillsets (TODO 38, 2026-10-29)

A user's library of Agent Skills folders (`SKILL.md` + support files),
grouped into versioned skillsets a run picks at creation and the engine
installs into every harness in the project's set. Nothing of it is in the
flow grammar: skills are a per-run selection, like the source branch.

**Ownership and shape.** User-owned, global across workshops (the System
hub, beside Integrations) — the per-user model has no organisation level,
and skills a repository wants for everyone already live in its own
`.agents/skills` / `.opencode/skills`, which every harness reads from the
checkout. Three tables (`0017_skills.sql`): `skills` (mutable rows: name,
frontmatter description, a `files` manifest of `{path, sha256, size, mode}`,
a content `hash`, an optional `source`), `skillsets` + `skillset_skills`
(ordered current membership; `ON DELETE RESTRICT` so a listed skill is not
deletable — 409 naming the sets), and `skillset_versions` (`seq`, `hash`,
an immutable `manifest` snapshot of every member's files). Bytes live in
the blob store; both jsonb paths are in `LiveBlobSHAs`, or the sweep would
reclaim them after the grace hour. Owner columns are the plain `user_id`
of 0013 (no FK to users).

**Version policy.** `skillstore.Store.bump`: a skillset's hash is
`SetHash` over its members' (name, content hash), order-independent; a new
`v{seq}` is appended only when that differs from the head's. Every content
change funnels through it — a member edit cuts one version in each set
listing the skill, a membership change or rename cuts one, a remote refresh
cuts one at the end (not one per touched child). Reverting content appends
a new seq rather than pointing back; latest is always max seq. Renaming a
listed skill is allowed: the snapshots keep the old name.

**Remote import.** `skillstore.Importer`: a public folder URL
(`github.com/{o}/{r}/tree/{ref}/{path}`, a GitLab `/-/tree/` URL on any
host, or a direct `.tar.gz`/`.zip` with an optional subfolder) is fetched
the way `curl` would — a plain unauthenticated GET of the host's public
archive (codeload tarball; GitLab `/-/archive/…?path=`), no tokens, no git
clone, private repositories out of scope. The commit comes from the git
archive's PAX `comment`. A folder holding `SKILL.md` is one skill (unique
per owner by name and by source URL — importing it again says "refresh
instead"); otherwise every child directory with a `SKILL.md` is a skill and
the folder a skillset named after it. Children that fail validation or
clash with an existing name are skipped and reported as warnings, not
failures. A ref containing `/` cannot be told from the path and is read as
its first segment; no ref means `HEAD` (GitLab also tries `main`, then
`master`). Caps: 64 MiB per archive, 1 MiB per file, 8 MiB and 200 files
per skill.

**The run.** `StartRunRequest.skillsets: [{id, seq}]` (0 = latest) →
`engine.RunInput.Skillsets` → `CreateRunWith` resolves them through the
`SkillStore` port (the project owner's only; anyone else's reads as not
found) and stamps `RunCreated.Skillsets` pins `{id, name, seq, hash}` →
`run.View.Skillsets` (in the view JSON, so `DeleteSkillset` can refuse
while a live run pins the set) → replay → bootstrap. A rerun carries the
original's skillsets re-pinned to their latest (as the flow is re-read by
name); an explicit empty list clears them.

**Install.** Bootstrap, after `agent-configs` and before `Prepare`: `rm -rf
/apothecary/skills && mkdir -p` (always — a re-bootstrap must not keep an
older selection), then one `EnvProvisioner.WriteTree` per skill
(`skills.WorkspaceDir/<name>`) and a `manifest.json` of the pins. `WriteTree`
is new on the port: a tar built engine-side rides stdin behind a one-line
script, as `WriteFile`'s base64 does — one exec per tree instead of one per
file, the same path on docker and Kubernetes. Then, after `Prepare`,
`Resolution.InstallSkills` fans `Harness.InstallSkills(names)` out over
every harness in the set; each adapter owns its location and calls
`acpagent.InstallSkills`, which COPIES (`cp -R`, one exec) into: codex
`$HOME/.agents/skills`, opencode `~/.config/opencode/skills`, omp
`~/.omp/agent/skills`. Copy, not symlink: omp's symlink following is
undocumented, and skills are kilobytes. opencode and omp also scan each
other's and codex's directories, so a project with several harnesses shows
each skill in more than one place — same name, same bytes: opencode warns
and keeps one, omp's native copy shadows the rest. Modes are normalised to
0755 (executable at the source, or under `scripts/`) and 0644. Retry and
the recovery path reuse the workspace and install nothing new; rebuild runs
the whole bootstrap again. A pinned version that no longer exists (the set
was deleted after the run finished, then the run rebuilt) is skipped with a
warning, as a vanished flow version falls back.

**Where things are.** Format rules: `internal/skills` (frontmatter, name
grammar, path rules, caps, hashes). Library: `internal/skillstore` (Store,
Importer, Mem, the `DB` port; `skilltest.RunDBContract` runs against Mem
and Postgres). Postgres: `internal/pgstore/skills.go`. Port: `ports.TreeFile`
/ `WriteTree` (workspace adapter `tree.go`, fake). Seam:
`kinds.Harness.InstallSkills`, `acpagent/skills.go`. API: `httpapi/skills.go`
under `/api/skills`, `/api/skillsets` (scopes `ScopeSkill`, `ScopeSkillset`;
skill bodies carry base64 file contents under their own size cap). UI:
System → Skills, the picker on the start-run pane and the rerun modal,
chips on the run header.

**Known limits.** Manual refresh only (no polling). Automations start runs
with no skillsets; a workshop-level default list is the natural follow-up.
`tar` and `base64` must exist in the repo's devcontainer image (busybox is
fine). A GitHub folder import downloads the whole repository tarball (there
is no server-side path filter), bounded by the 64 MiB cap. There is no
Claude Code harness in apothecary; when one exists its directory is
`~/.claude/skills`.
