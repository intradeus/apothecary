# CI's unit job enforces both coverage floors (2026-11-09)

- The CPUI floor (`cpui/vite.config.ts` thresholds, lines 99%) was enforced
  only by a local `task cover` / `task verify`; CI's unit job ran
  `task test:unit`, which has no coverage. Main sat at 98.96% lines without
  anyone seeing it (found while fixing the Notices page, TODO 51). The unit
  job now runs `task cover`: `cover:go` (every package, Postgres tests
  included, `GO_COVER_MIN`) then `npm run test:coverage`, and uploads both
  reports as a workflow artifact (`coverage`, 14 days). A floor only rises;
  a change that drops under one fails the pull request.
- Not `task verify`: lint and build have their own jobs, and the pre-commit
  gate repeating them in CI would only lengthen it (TODO 52). The Postgres
  tests now run twice in CI: `cover:go` runs every package with
  `TEST_POSTGRES=1`, so the `pg` integration suite (the pgtest packages,
  same flag) is a subset of it. It stays for now as the fast, isolated
  signal on the Postgres adapter; dropping it to shorten CI is TODO 52's
  call.
- Getting back over the CPUI floor took tests, not a lower number: the run
  header's Stop and Archive (each asks first, naming the workspace it keeps
  or destroys; archive's toast offers the ticket) and the retry label read
  off the log were never exercised, and some forty one-line calls in
  `api/index.ts` (the thin reads: run ports/ssh/services, the Ask pane, a
  workspace file/changes/diff, a ping's payload and curation, skills and
  skillsets by id, the account calls) had no test saying which endpoint they
  reach with which arguments. They do now, as one table.
