# Forward probes loopback before binding

Why: `apothecary forward` tells a taken local port by failing to bind
`127.0.0.1:<port>`. Go sets `SO_REUSEADDR` on listeners, and on macOS (and
Windows) that bind succeeds beside another process's wildcard listener on the
same port — a Postgres container published on `0.0.0.0:5432` and `[::]:5432`,
say. Both then serve localhost: `127.0.0.1:5432` reaches the run (the more
specific bind wins), `[::1]:5432` the container, and `localhost:5432` either,
depending on the client's resolution order. Linux refuses the bind, so the
tests and CI never saw it.

What now: before binding a named port (`--same`, `port:local`, a remembered
assignment), `listenLocal` dials `127.0.0.1` and `[::1]` on it
(`localAnswers`, 200 ms each); one that answers makes the port taken, with
the existing outcomes: a named port is refused, a followed `--same` port and
a remembered one move to a free port. A dial rather than a wildcard bind
probe because `EADDRINUSE` semantics differ across the three systems the
client ships for, while "something answers localhost here" is the question
on all of them. Port 0 is not probed.

Tests: `TestListenLocalRefusesAnsweredPort` (a wildcard listener and a
loopback one each make the port taken; port 0 still binds), with
`TestForwardSameAndNamedPorts` for the outcomes.
