# Forward is whole or nothing and offers free ports

Why: `--same` is asked for to get the run's own port numbers — to test
something that expects them. A forward that followed the run moved a taken
port to a free one by itself, said only in the daemon's log, and a command
naming several ports forwarded the ones it could and refused the rest: in
both cases the person had ports elsewhere than asked, or half of them,
without being told. Finding a free port for each refused one was then left
to them.

What now:

- **No following.** `apothecary forward <run>` forwards the ports the run
  listens on when asked, once; a port it opens later is forwarded by asking
  again (already forwarded ports are left as they are), and nothing closes
  by itself. A run listening on nothing is refused. The daemon's polling
  (`followRun`, `sync`) is gone; a `--follow` flag can bring it back. A
  forward still dials the run per connection, so a server restarting on the
  same port keeps working. This supersedes "a followed `--same` port moves
  to a free port" in `2026-11-17-forward-probes-loopback-before-binding.md`.
- **Whole or nothing.** Every port of a command is bound before any is
  forwarded (`bindAll`, then `commit`). One refused — its local port taken
  (`takenError`), or the port forwarded already on another local port —
  closes the ones bound, and the command forwards nothing and fails.
- **`port:local` with `--same`** is an exception to `--same`, not a pick:
  `--same 5000:5001` is every port on its own number, 5000 on 5001. A bare
  port picks (`3000 --same`: only 3000); without `--same` every port named
  picks, as before. A port named twice is refused.
- **The offer.** For each port refused as taken, the daemon probes a local
  port free now (`freeInstead`): the nearest of the next 20 above the one
  asked for, so 5432 offers 5433 and the number still reads as the
  service's, else any free one; never one offered twice, nor one the
  command's other ports would bind again. The command prints what was typed
  with only those ports changed (`retryCommand`): a typed port rewritten in
  place, one `--same` chose appended — `forward run --same` with 5000 taken
  offers `forward run --same 5000:5001`. An offered port taken in turn is
  refused the same way, with the next one offered. The offer is advisory:
  probed free, it may be taken before it is asked for.
- A refused forward answers 200 with its notes and offers, not an error, so
  the command can print them.

The web UI's Ports pane says the same: its two whole-workspace commands
forward the ports listening now (for a later one, run it again), `--same`
forwards nothing when a port is taken and prints the command with free
ports, and neither is offered while nothing listens, the CLI refusing that.

Tests: `TestForwardSameAndNamedPorts` (a taken `--same` port, a taken named
one, two taken among several, a port forwarded elsewhere, the whole run with
one taken and its other ports let go, the exception taken in turn, the
offered command run), `TestForwardEveryPortOnceRunsInParallel` (no
following, asking again adds, nothing listening refused, listing fails),
`TestFreeInstead` (nearest free, avoided, none twice, any past the span,
none at all; `retryCommand`'s rewrites), `TestForwardToARunGone`,
`TestParseForwardAndHelpers` (a port twice); in the UI, `PortsView.test.ts`
("every port at once").
