# File lists as a tree (TODO 47, 2026-11-01)

A run's Changes and a review's files (`review/FileList`: DocReview,
ReviewHistory, CurationPage) can show their files as a tree, from a List /
Tree switch (`app/FileLayoutSwitch`) in the pane's bar. The run's Files pane
was a tree already and is unchanged. The choice is one per-browser preference
(`prefs.fileLayout`, `apothecary.files.layout`) that every such list follows,
so a pane remounted on a repo switch keeps it. Tree is the default: only a
saved `list` shows the flat list.

`core/pathTree` turns paths into the tree: folders first, then files, in
code-unit order (git's, not the locale's); a folder holding only one folder
merges into one row, since a change list is sparse. Each pane keeps its folded
folders by path, so a Changes refresh keeps them. In tree mode Changes opens on
the first file the tree shows; a review still opens on the gate's first file
(`primary`, which old drafts hang off), and j/k (`ReviewFiles.step`) walk the
order shown (`ReviewFiles.order`), folded folders included. The narrow-width
picker (`FileList variant="select"`) keeps the gate's order.
