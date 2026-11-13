# Flows belong to a project

The global flow was a fiction. A command stage runs `make verify`; an agent's
prompt knows where this repository keeps its migrations; an egress rule names
this project's registry. "The flow called default", shared by every project,
could only ever hold what is true everywhere — which is almost nothing. So
identity moved from `name` to `(project_id, name)`.

Two things survive the move deliberately, and they are what keep it from being
a downgrade:

- **The built-ins stay global templates.** `internal/builtin` documents belong
  to no project and resolve by name for anyone who has not saved their own.
  Saving one creates that project's editable copy — the shadowing that already
  existed, now per project. A fresh project still runs without authoring a flow
  first.
- **Copying replaces sharing.** `POST /api/projects/{id}/flows/{name}/copy`
  clones a flow into another project (optionally renaming it), which is how a
  good flow spreads when it can no longer be shared by reference. The copy is a
  new document with its own history: editing it never reaches back, which is
  the point of ownership and what the dialog says out loud. A template is
  copied through `POST /api/flow-templates/{name}/copy` instead: it belongs to
  no project, and a path cannot carry an empty `{id}` — the mux collapses the
  empty segment and the request lands on the UI's catch-all as a 405.

### The seam

`flow.Source.Get(ctx, name)` became `Get(ctx, projectID, name)` (and
`PinnedSource.GetVersion` likewise). Every engine call site already knew its
project — from a `Resolution`, a `run.View`, or `CreateRun`'s own argument — so
the change is threading, not restructuring. `MapSource` ignores the project on
purpose: ownership is a property of the STORE, not of the flow language.

### What the ordering change forces

A flow cannot exist before the project that owns it, so **a project is always
created on a built-in template** and authors or copies its own afterwards. That
inverted every fixture that used to create a flow and then a project pointing
at it, and it is why `seedDevProject` now creates the project, saves the dev
flow into it, and then re-points `default_flow`.

`flow.Rename` exists for the same reason: a document names itself and that name
is its id, so copying under a new name is a document edit. It rewrites through
the YAML node tree, so comments, key order and formatting survive and only the
top-level `name` is touched.

### Deletion, and what "immutable forever" now means

Deleting a FLOW still keeps its versions (§7). Deleting a PROJECT now takes its
flows and their history with it (`ON DELETE CASCADE`): a document naming a gone
project's commands has no reader, and `DeleteProject` already refuses while any
non-archived run exists. The run events — which carry `flow_name` and
`flow_hash` — are untouched, and they remain the audit trail.

The delete guards narrowed to match: only THIS project's `default_flow` and
THIS project's live runs block a flow's deletion. Another project's flow of the
same name is a different document and never interferes.

### Migration (0023)

Existing rows are copied into EVERY project, because that is what they already
meant: a flow named X resolved for every project, so every project keeps
resolving the same document. Duplicate rows are the honest cost of the model —
attaching each flow to one project instead would silently change what the
others run. `flow_versions` gets `project_id` in its primary key: the same
content saved in two projects is two rows with one hash, because they are two
histories.

The old primary keys are dropped BEFORE the fan-out insert, not after: `name`
(resp. `hash`) is still the key while the copies are being written, so the
second copy of any flow collides with the first. Found by running the migration
against the real local database, which had two flows and one project.

### The API and the UI

Flow routes live under their project (`/api/projects/{id}/flows/...`), with two
that do not belong to one: `/api/flows` (the cross-project overview) and
`/api/flow-templates` (the compiled-in library, whose `{name}/copy` adopts a
template into a project). `POST /api/flows/validate` stays global because it
is pure parsing.

The UI keeps its Flows page, grouped by project, with the templates in their own
section and a copy action on every row — cross-project cloning has one obvious
home, which it would not have inside a single project's page. The editor moved
to `/flows/[project]/[name]`.
