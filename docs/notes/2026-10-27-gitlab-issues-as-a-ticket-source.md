# GitLab Issues as a ticket source (2026-10-27, TODO 355)

A third `ticketsource` kind, `gitlab` (`kinds.GitLabIssues`,
`internal/adapters/gitlab/issues.go`), with the read port, `TicketWriter` and
`TicketAttacher` — the GitHub Issues shape, on the repository adapter's
REST v4 client. Decisions worth recording:

- **REST issues, not the work items GraphQL API.** GitLab is moving issues,
  tasks, incidents and epics onto one "work items" framework whose native API
  is GraphQL. The REST issues API still serves every issue-type work item and
  is what the rest of the package speaks, so the adapter stays on it; epics
  (group-level) are out. What only GraphQL reaches — the configurable work
  item *Status* (in progress, in review…) — was why `TransitionTo` knew only
  todo/done. *(Superseded by TODO 49, below: steps resolve through the
  integration's status list, opened/closed, and GitLab types are its
  `issue_type` list.)*
- **The key is GitLab's own full reference**, `group/project#123`
  (`references.full`, falling back to the `web_url` path): an iid is unique
  only within its project, and nested groups make the path any depth. It is
  sent URL-encoded as `:id`, so no numeric-id lookup precedes a call.
- **Search scope is projects and groups.** Each configured scope is one list
  call, merged by key and re-sorted by `updated_at`; none configured falls
  back to `/issues`. There is no query language on the REST side, so the
  search box lifts GitLab's filter-bar words (`~label`, `is:`, `assignee:`,
  `author:`, `milestone:`, `type:`) into parameters and searches the rest.
- **Description uploads are attachments.** Unlike GitHub, GitLab hosts the
  files pasted into an issue (`/uploads/<secret>/<file>`, project-relative).
  Fetch lists them and rewrites the links absolute (the body is read outside
  GitLab once imported); `OpenAttachment` downloads through
  `GET /projects/:id/uploads/:secret/:filename` (GitLab 17.4+), host-checked
  before the token goes along. A file that fails is the factory's usual
  named warning.
- **Write-back** is `state_event` close/reopen, notes for comments and links
  (system notes are left out of `Comments`, so the link marker scan sees only
  real notes), and labels / assignees (username → id via `/users`) /
  milestone (title → id, ancestors included) for `ticket_set_field`.
  `Attach` reads the issue's `project_id` and reuses `UploadAttachment`.
