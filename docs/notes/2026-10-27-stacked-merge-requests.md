# Stacked merge requests (TODO 43 retired, 2026-10-27)

- **Stacked means it targets another open merge request's branch.**
  `ports.StackedOn(target, open)` finds the base among the repository's
  open merge requests (`ListMRs`). A fork is never a base, because its
  branch lives in another repository. Nothing is recorded; the hosts have
  no stacking concept of their own, so it is read off branch names on
  every listing.
- **Where it shows.** The workshop's merge-request rows (`MRRow.stacked_on`)
  carry a "stacked on !N" pill linking to the base. The flow picker warns
  that what a run merges goes into the base's branch, not the main line.
  The run page's live status (`MRLive.stacked_on`, open MRs only) says the
  same next to mergeability, for a run's own merge request too: a run
  forked off a branch that has a merge request opens a stacked one.
- **Cost.** `MRStatuses` lists open merge requests once per repository
  that has an open one. A list that fails leaves no notice; the status it
  already read is still returned.
