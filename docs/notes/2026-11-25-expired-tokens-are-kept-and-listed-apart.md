# Expired tokens are kept and listed apart

## Why

The Account page's personal tokens grew without end. The assistant mints a
token per session, good for an hour
(`2026-11-22-the-assistant-token-lives-with-its-harness-session.md`), and
revokes it only when the same session mints its next one. A session nobody
returns to (each "New conversation", each page the assistant was opened
about) leaves its last token behind: refused by the engine from its expiry
on (`auth.AuthenticateToken`), its row never deleted. The list showed those
beside the ones that work, and the expiry was an absolute date at the end
of a truncated line, so a dead token looked like a live one.

## What now

- **Nothing is deleted.** A token past its expiry is of no use to anyone,
  and the owner chose to leave it where it is: no sweeper, no revoke when
  the assistant's pod stops, no engine change.
- **The list is the tokens that still work.** A switch above it, Active /
  Expired with a count each, opens on Active. Minting a token brings the
  list back to Active. An expired token can still be revoked from its
  side, which is what removes it.
- **A row says how long its token has left**, on a line of its own:
  "expires in 41m" (rounded down: what is left is never overstated),
  "expired 2h ago", "never expires", a CLI token's "signs out after 7 days
  unused" or "signed out 1d ago". The full date is the line's tooltip.
- **Expired is read in the browser** (`tokenLife`), by the engine's two
  rules: at or past `expiresAt`, or an inactivity window run out since the
  last use (the creation, never used). The list's clock ticks every 30 s,
  so a token whose time comes with the page open leaves Active by itself.
  A browser clock that is off moves that moment by as much; the engine's
  answer to the token itself is unchanged.

*Rejected for now:* deleting expired tokens on a schedule, and revoking a
session's token when the pod stops: both shorten the list for good, and
both are engine work the list did not need. Telling who minted a token
(person, CLI, assistant) apart: the assistant's still read as "Assistant
session …" by name.

## Tests

`cpui/src/lib/system/tokenLife.test.ts` (each wording, the boundary at the
expiry, the window sliding with a use and counted from the creation),
`TokenSection.test.ts` ("a token past its expiry": left out by default
with the counts, shown and revoked on its side, the two empty sides, a
mint bringing Active back, a token leaving Active while the page is open),
`cpui/src/lib/core/format.test.ts` (`fromNow`).
