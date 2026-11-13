# One session per stage (2026-10-31)

An attempt sent back to a stage continues that stage's session instead of
opening a new one (TODO 44). It never worked that way: a fresh attempt always
called `Run`, and `resume` was only for a reply within an attempt.

`runcore.State.Sessions` now survives `stage_started`, so a stage keeps its
session for the run, whatever chain runs it. The prompt is the attempt's whole
prompt, not a short note: the inputs may have changed and it carries the feed.
`AgentInput.Attempt` tells a runner a Resume starts an attempt, and gives it the
stage's spec. `Resume` now sets the harness's mode when a session is reloaded;
retries reach that path, and codex would otherwise come back asking for
approvals. A Resume that cannot load the session (`ErrSessionGone`) falls back to
`Run`.

A crash after a continuing attempt's prompt was logged is the one new window:
nothing says the agent read it, so recovery sends it again instead of telling
it to carry on (`Continuing`). A stage with a session is not proof that this
attempt reached its agent, so `AttemptBegun` reads `Prompted` alone.

`reuse_session: false` on an agent stage opts out, for a stage that should look
afresh: `builder`'s adversarial `review`, whose verdict the next round should not
inherit. The fold drops the session at `stage_started`, so nothing else changes,
crash recovery included. It is on unless said, so `Stage.FreshSession` carries
the exception and the editor writes the key only to turn it off.
