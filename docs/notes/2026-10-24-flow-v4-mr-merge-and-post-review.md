# Flow v4: mr_merge and post_review

Second pass, step 4 (2026-10-24): the two host-side acts the modeled flows
need beyond opening a merge request — auto-merge (a reaction on
`mr_mergeable`) and the reviewer flow.

- **Port.** `MergeMR(mr, {Method, Head, DeleteBranch})` and `PostReview(mr,
  {Head, Summary, Notes, ID})`. A merge the repository host declines is a typed
  `*ErrMergeRefused` (GitHub 405/409/422, GitLab 405/406/409/422) that the
  engine turns into a routable `input_invalid` failure; anything else is the
  repository host not answering (infra, terminal). GitLab refuses `rebase` outright:
  it merges by the project's merge method, and quietly merging another way
  would be worse than saying so.
- **Pinned head.** `mr_merge` passes the phase's starting tip (`phaseTip`) as
  the head to merge — the head the repository host called mergeable, or the head a
  person started the phase on. A push after the decision makes the repository host
  refuse; `mr_mergeable` fires again for the new head. Templates should
  route that `on_fail: end` rather than to a person.
- **Reconciling.** Both record `action_started` first. `mr_merge` re-entered
  reads the state: already merged = this merge. `post_review` marks every
  part (`ReviewMarker(id, part)`, the id = phase run + stage): GitHub posts
  one review, all or nothing, after checking no review carries the marker;
  GitLab posts each note as a discussion and the summary as a note, each
  skipped when its marker is already there. `IsFactoryReply` recognises the
  marker, so a review's own notes never read as a reviewer's comments.
- **Lines.** A note goes on its line only when the line is in the diff
  (`ports.DiffLines` reads the unified patch; GitLab needs the old line for
  an unchanged one); otherwise it is folded into the summary with its place
  (`FoldedNotes`) — GitHub would refuse the whole review for one bad line,
  and an agent's line numbers are not always right. GitLab notes it will not
  anchor (400/422) are folded too.
- **Output type `review_threads`**: `{summary, threads: [{path, line,
  body}]}`, ≤ 50 threads, checked when the agent's stage ends
  (`checkReviewThreads`, next to `checkReviewReplies`) so a bad review is
  that stage's failure with `$FAILURE`, not post_review's.
- Events `review_posted` (inline, folded, already) and `mr_merged` (method,
  head, branch deleted) — the latter only for the run's own merge; a
  person's is seen by observation. `mr_merge` is `Remote`: no workspace.
