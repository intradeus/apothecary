# The run header and the alert banner on a phone

2026-10-24, the user, on a run created on someone's merge request: "very
ugly — the run, workspace and merge request cards are awkward, the egress
blocked warning is not mobile friendly."

- **The title is three lines at most**, at every width (`line-clamp`, the
  whole of it its tooltip): a merge request's title plus its author's handle
  (`(!21 by project_84607299_bot_…)`) ran to five lines of large italic.
  On a phone it is smaller (`--fs-section`, its accent `--fs-mark`) and the
  state sits under it.
- **Run, Workspace and Merge requests are one card on a phone**: the three
  glass groups stack edge to edge, a row each — what it is on the left,
  its buttons on the right — instead of three cards of three widths. The
  workspace's id gives its room to the buttons there.
- **`Banner` wraps on a phone**: the alert and its aside, then the items
  (the blocked hosts, which were cut off before), then the buttons, right
  aligned. Its buttons are one group pushed right at any width.
- The compact run page never scrolls sideways (`overflow-x: hidden`); the
  banner had pushed it 140px wide.
