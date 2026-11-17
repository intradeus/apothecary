# CI must pass to merge, and pull request titles go unchecked

Why: a pull request could be merged with `ci` red, and the `pr-title`
workflow was asked to go.

What now:

- **`pr-title.yml` is removed.** A pull request's title is not checked any
  more; the commit-msg hook still checks every commit. A squash merge lands
  under the title, and the release reads that commit (`.cz.toml`), so a title
  outside the convention gives a commit the release does not count — the
  author's to get right (AGENTS.md, docs/operations.md say so).
- **`ci`'s `passed` job is the check a merge requires** (a ruleset on `main`,
  set up as docs/operations.md says). It used to be skipped when a job failed;
  GitHub counts a skipped required check as passed, so it now runs on every
  pull request run that was not cancelled and fails when any job failed or
  was cancelled. Jobs skipped by the change filter still count as passed.
- **The release pushes with a deploy key** (`RELEASE_DEPLOY_KEY`), the
  ruleset's one bypass: the release commit has no `ci` run, so the ruleset
  refuses it from the workflow's token. Chosen over a GitHub Actions bypass
  actor (not offered on every repository) and over releasing through a pull
  request (a step more per release). `manual-release` stops before building
  anything when the secret is missing.

Tests: actionlint (`task repo:lint`) over both workflows;
`internal/devtools/devcontainer` lists the workflows left.
