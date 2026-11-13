# Ticket sources: import from Jira (new port `ticketsource`)

*Superseded by "Tickets follow their tracker (TODO 49)" below: a sourced
ticket now mirrors its issue (title, body, status, type, labels, comments)
and is re-read by a sync; the "copy taken once" below no longer holds.
Deleting the integration unlinks its tickets onto the internal tracker.*

A ticket can now be **copied from an external tracker** instead of typed in.
The shape is the usual one — port (`ports.TicketSource`), adapter
(`internal/adapters/jira`), kind (`kinds.Jira`, registered under the new
`ticketsource` port) — plus a **ticket factory** (`internal/tickets`) that
both creation paths go through, so an imported ticket is indistinguishable
downstream from a hand-written one.

Decisions worth recording:

- **The port is read-only.** It has `Test/Search/Fetch/OpenAttachment` and no
  write surface: apothecary never transitions an issue, comments on it, or
  syncs back. The import is a *copy taken once* — title, body and attachment
  bytes land in apothecary's own storage, and `tickets.source_{id,kind,key,url}`
  record where they came from. `source_kind` is stored next to `source_id`
  (and there is no FK) precisely so a ticket survives deleting the
  integration. *(Amended: the read port stays as it is; write-back arrived
  later as an optional capability a flow opts into stage by stage — see
  "Tracker actions" below.)*
- **Not a project reference.** Unlike repository host/provisioner/harness, a ticket
  source is picked *per import* on the ticket form, never bound to a project.
  A tracker outage can therefore only fail an import — it can never stall a
  run, and `confstore.validateProject` gained nothing.
- **Attachments go through the ordinary artifact store** (`Save`, so the MIME
  allowlist and size cap apply), landing as `ticket_goal` scope — the same
  thing an upload becomes, hence delivered into every run of the ticket. A
  file that is too large or disallowed is *skipped with a named warning*
  rather than failing the import; the create response carries `warnings[]`
  and the form makes the operator acknowledge them before navigating on.
- **Duplicate imports are refused** (409 naming the existing ticket): two
  tickets tracking one issue diverge silently, and it is nearly always a
  mis-click.
- **Jira's two shapes are absorbed in the adapter.** Descriptions arrive as
  ADF (JSON node tree) on API v3 and as wiki markup on v2 — `adf.go` renders
  both to markdown so nothing past the adapter sees vendor markup, degrading
  unknown node types to their children rather than dropping them. Search tries
  Cloud's current `POST /search/jql` and falls back to the classic
  `GET /search` on 404/410/405 (Data Center, older Cloud). Auth is Basic
  email+token, or Bearer when the email is left empty (DC PAT).
- **Attachment URLs are re-checked against the configured site** before the
  credential is attached: the download URL comes from the tracker's own
  response, and a tracker must not be able to aim an authenticated engine
  request at a host of its choosing.
- Free text becomes JQL in one place (`Client.JQL`): an issue key looks the
  issue up, a `jql:` prefix passes through verbatim, empty means "assigned to
  me and unresolved", anything else is a text search — all ANDed with the
  integration's optional scope filter.

`serve -dev` seeds a fake tracker (`kinds.FakeTicketSource`, two issues and an
attachment) so the whole flow is exercisable with no Jira.
- **Setup prose is descriptor data, not UI copy.** `kinds.Descriptor.Help` and
  `kinds.Field.Help` were added so a kind can carry its own "where do I get
  this credential" instructions; `IntegrationForm` renders the descriptor help
  in a `<details>` (open when adding) and linkifies bare URLs. The Jira help
  names the *exact* least-privilege token scopes — classic `read:jira-user` +
  `read:jira-work`, or the granular equivalents — precisely because
  Atlassian's scoped-token picker offers dozens and the four calls this
  adapter makes (`/myself`, `/search/jql`, `/issue/{key}`, attachment content)
  are all reads. Data Center PATs have no scope picker at all (they inherit
  the account), which is worth saying out loud rather than letting an
  operator hunt for it. The JQL field is labelled "search filter", not
  "scope", so the word only ever means the token's permissions.
