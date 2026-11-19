# The UI gates controls on the workshop role

Follows `2026-11-18-permissions-cedar-roles-sharing-and-scoped-tokens.md`:
the engine refuses what a role does not hold (403); the UI stops offering
it, so a viewer or operator is not handed buttons that only fail.

Why: a shared workshop puts runs, tickets and settings in front of people
who may only read them. A button that answers "this needs run:operate" on
click is a trap.

What now:

- **Where the access comes from.** Every project the engine lists or
  returns carries the caller's `access` (role + actions) and, when shared,
  the owner's handle. A component holding the `Project` asks
  `projectCan(project, action)`; anything keyed by a run, ping or ticket
  asks `fleet.can(projectId, action)`, which reads the shell's project list.
  The run page reads `RunStore.canOperate` / `canShell`.
- **What an unknown workshop grants.** No workshop (a personal run, only
  ever its owner's) grants everything; a workshop not (yet) in the list
  grants nothing. A control can be disabled for the instant before the
  list loads; it is never enabled for someone who lacks the action.
- **Disabled, or hidden.** A control is disabled with a title saying the
  role does not allow it (`common.needsPermission`); where the component
  already hides things (menus, the composer, the Ask box, the ticket reply
  box, the workshop's Settings tab, the members' add form) it is absent.
  Reads stay: a viewer still sees the runs, their logs, the tickets and
  the workshop's overview.
- **Members.** Only the owner is offered *maintainer*, and only the owner
  re-roles or removes one, matching the engine.
- **Tokens.** The Account page mints full or limited tokens; the action
  list comes from `GET /api/permissions`, minus `account:manage`, which a
  token can never hold.

Tests: `src/lib/domain/project.test.ts`, `src/lib/app/fleet.svelte.test.ts`
(`fleet.can`), a viewer case in each gated component's test,
`src/lib/workshop/MembersTab.test.ts`, `src/lib/system/TokenGrantForm.test.ts`.
