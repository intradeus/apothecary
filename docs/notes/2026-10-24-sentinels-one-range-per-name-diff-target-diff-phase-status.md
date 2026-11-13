# Sentinels: one range per name — $DIFF_TARGET, $DIFF_PHASE, $STATUS

2026-10-24, with the user, ahead of runs on others' merge requests.
`$CHANGED` meant two ranges by where it was bound: the branch against its
target in an entry phase, what *this round* changed (from the phase's
starting head) in a subject phase. A reviewer re-reviewing after the
author's push would have read an empty list — the round itself changed
nothing. The user asked for names in git terms that say exactly the range:

- `$DIFF_TARGET` = `git diff --name-status <target>...HEAD`, everywhere;
- `$DIFF_PHASE` = `git diff --name-status <phase start>...HEAD` (from the
  target when the phase began on no recorded head — the delivery);
- `$STATUS` = `git status --short` (was `$UNCOMMITTED`).

Not `$DIFF_HEAD` (git's `diff HEAD` is the uncommitted changes) nor
`$UNSTAGED` (it would exclude staged and untracked files). The prompt's
INPUT header prints the git range, so an agent reads what it was given. The
old names are refused with their replacement (`retiredSentinel`) — an error
message, not compatibility: saved flows using them must be edited, and an
in-flight run whose pinned flow binds one fails with the parse error. The
default flow's review-round `reply` stage reads `$DIFF_PHASE`; its reviewer
and redactor `$DIFF_TARGET`.
