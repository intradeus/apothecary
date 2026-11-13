# A stage's checks see the repo through its symlink (2026-11-13)

- **The bug:** `task sandbox:test:harness` passed in a run's terminal and
  failed in its stage (`go: go.mod file not found`). A stage runs in
  `$HOME/repo`, a symlink to the bind-mounted checkout, so the Taskfile's
  `ROOT_DIR` is the link. `deploy/self-container.sh path` matched it against
  the mounts' destinations as text, found none, and returned it unchanged:
  the daemon then bound an empty directory of its own at `/src`. The terminal
  sat in the checkout's real path, which matched.
- **Now** `path` resolves the directory (`pwd -P`) before matching. On a host
  (no container) it still answers the directory as given.
- The same stage's output ended in `/proc/<pid>/environ: Permission denied`,
  once per process of another user: the marked-command sweep read each
  environ with `tr … < file 2>/dev/null`, and a failed `<` is reported
  before the later `2>/dev/null` applies. The read is now grouped and the
  group silenced (`TestSweepPreludeIsQuietAboutUnreadableProcesses`).
