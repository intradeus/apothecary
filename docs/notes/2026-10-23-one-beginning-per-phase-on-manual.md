# One beginning per phase: `on: manual`

*Superseded by flow v4 (next entry): the entrypoint is `on: run_start`,
`confirm: true` became `start: confirm`, `ticket` is no longer read, and the
entry is no longer the first phase by position.*

A phase is one beginning and the stages it runs, and the three places that
said so disagreed. The YAML had `on: ticket` on the entry — a noun, a magic
string outside the signal registry — and a trigger signal on each reaction;
the canvas drew a solid "start" pill for the entry that named neither its
phase nor what begins it, and violet "phase: on signal" markers for reactions;
the inspector told the entry it "has no trigger to set". A flow without phases
had no beginning at all.

Now every beginning is a registry signal. `manual` (`signalspec.Manual`,
flagged `Trigger` and `Entry`) is the one a person gives: it never holds on
its own. The entry is `on: manual` — someone starts a run for a ticket — and a
reaction phase may be `on: manual` too: a phase only the run page starts
(`max` counts those starts; `confirm` is refused, nothing fires to confirm).
The repository host triggers keep `Trigger` only; `Entry` is the seam for a tracker
event (*a ticket assigned*) to begin a run, as FLOWLANG §10 foresaw. The
entry is the first phase by position (`Phase.entry`, set by the parser), no
longer "the one on ticket", since manual begins reactions too. `ticket` is
still read, as `manual`, and the editor writes `manual` back: no version
bump, every saved flow parses.

The canvas draws one marker per phase, the entry included — "deliver: on
manual", "conflicts: on mr_conflicts (confirm)" — solid for the entry and
dashed for reactions, each pointing at its phase's first stage; a flow
without phases shows "on manual". The inspector's phase panel is one form:
*Begins on* offers the entry the signals flagged `Entry` and a reaction those
flagged `Trigger`, and shows `quiet_for`, `confirm` and `max` only where the
chosen signal takes them. `GET /api/signals` carries `entry`; the run page's
review card names a manual phase "only when you start it". The entry's
`phase_started` records `by: human` (it was `ticket`); the timeline reads
`by` only for reaction starts.
