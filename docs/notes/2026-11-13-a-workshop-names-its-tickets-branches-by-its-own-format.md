# A workshop names its tickets' branches by its own format (2026-11-13)

- **The setting:** `repo.branch_format` on a workshop (the settings page's
  Repository section, with a live preview). Free text with three
  placeholders: `{ticket_type}` (the change type), `{ticket_number}` (the
  tracker key: `PROJ-123` → `proj-123`, `lu/greeter#5` → `5`, empty by hand)
  and `{ticket_title}`. There are no short aliases. The default,
  `{ticket_type}/{ticket_number}-{ticket_title}`, gives exactly the names
  the hardcoded `tickets.BranchName` gave, so existing workshops are
  unchanged.
- **One spec, two renderers:** `internal/project/branchname` names a ticket
  created without a branch (by hand, the import form, an automation), and
  `cpui/src/lib/core/branchname.ts` prefills the ticket form. Both test
  suites read `internal/project/branchname/testdata/cases.json`, so they
  cannot drift. Values are slugged; literal text is kept verbatim.
- **Empty placeholder rule:** a placeholder that renders empty takes one
  adjacent literal separator (`-`, `_`, `.`) with it: the next one when it
  starts its `/` segment, else the one before. A segment it leaves empty is
  dropped, so a hand-written ticket is still `feat/add-x`. With neither
  number nor title the title is `ticket`. The title is cut to keep the name
  within 200 bytes.
- **Validated at save:** an unknown placeholder or brace, a format with
  neither `{ticket_number}` nor `{ticket_title}`, more than 100 characters,
  or a sample name `checkout.CheckBranch` refuses. Deviation from the plan:
  a format that renders an *empty* name for a ticket without a key
  (`{ticket_number}` alone) is refused too, since CheckBranch accepts `""`.
  An empty format is saved as the default.
- **Storage:** the field lives in the `projects.repo` JSONB, so there is no
  migration.
- **`SourceTicket.branch` is gone** from the import preview
  (`GET /api/integrations/{id}/tickets/{key}`). The preview belongs to an
  integration, not a workshop, so its name was wrong for any custom format;
  the form renders the name with the selected workshop's format instead.
- **Unchanged on purpose:** the engine's fallback for a run whose ticket has
  no base branch (`apothecary/<run>-<title>`, `engine.go`). Every ticket
  created now has a base branch.
