# The agent guide is optional, with no default (2026-11-13)

- **Optional, no default:** a project's guide path may be empty, and
  `confstore.validateProject` no longer fills in `AGENTS.md` (it trims, so
  whitespace is empty). Empty means none:
  - preflight (`CreateRun` and `PreflightProject`) checks no file and lists
    no `repo_file:` step;
  - bootstrap links nothing (an empty path used to emit
    `ln -sf "" AGENTS.md`, a dangling link);
  - the prompt says nothing about it.
  A path that is set and missing on the repository host still fails
  preflight, as before.
- **Renamed in the UI only:** the form and the overview call it the
  *Agent guide*. The wire, storage and Go names stay `repo.agents_md_path`,
  `RepoConfig.AgentsMDPath`, `agentsMdPath`: no migration, no API or
  OpenAPI change.
- **Stored values are kept:** existing projects (usually `AGENTS.md`) still
  require the file until someone clears the field. The form's help text
  says to leave it empty for none.
- **The prompt names it, not the flows:** the built-in flows (builder,
  hands-on, reviewer) and the v5 reference flow no longer mention
  `AGENTS.md`. Instead `assemblePrompt` adds a `# REPOSITORY GUIDE` section
  naming the configured path, after REPOSITORIES and before the OUTPUT
  CONTRACT, only when the path is set and the file reads from the run's
  checkout (the run branch, not the repository host's default branch).
  envport has no stat call, so the check reads the file; guide-sized files
  make that acceptable. The harnesses that read a root `AGENTS.md` on their
  own still do with the setting empty: optional means Apothecary neither
  requires nor injects it.
- **Pinned hashes:** editing the built-in YAML changes each flow's SHA-256.
  A run paused across the upgrade and pinned to an old built-in hash no
  longer resolves through the built-in layer of `GetVersion`, which knows
  only the compiled-in hash (unless the store holds that version) — the cost
  of any built-in edit.
