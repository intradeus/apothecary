# Four goldens froze a race; the engine's test log is silent (2026-11-09)

- The first CI run of `task cover` failed on
  `TestRerunTicketedRunKeepsBaseNewTail`: its golden ended run 2 at
  `phase_started`, the state the loop was in when the test returned and the
  harness cancelled it. Under `-covermode=atomic -coverpkg=./...` on a shared
  runner the loop reached `env_requested` first. Four goldens had the same
  shape (the two rerun tests, the ticket rerun, the title-derived branch): a
  `CreateRun`/`Rerun` read through `h.view` and returned without
  `h.e.Wait`, while their sibling in the same file carried the rule in a
  comment ("to its end: the golden log must not depend on when the test
  stopped"). Each now waits; the goldens were re-recorded with the second
  run complete. Five local passes with CI's flags never showed it — the
  stress task is the stand-in, as before.
- An audit of every golden's last event found 57 that end short of a
  terminal or parked state; all but these four are at rest by design (a run
  idle after `phase_completed` waiting on its watchers, a bootstrap that
  failed, a watcher's confirmation ping, a deliberate mid-flight shutdown).
  A mechanical rule ("every run ends terminal or parked") would be wrong for
  them, so this stays a review item: a test that reads a run it created or
  rerun waits for it first.
- The job log carried 1,600 lines of INFO/WARN/ERROR from the engine: it
  logs through `slog.Default()` when `Deps.Log` is unset, the harness leaves
  it unset, and `go test` prints a failing package's whole stderr. Credential
  scrubs, flow warnings and the crash tests' "the process died" are all
  expected there and say nothing about the failure. The engine's `TestMain`
  now installs a discarding default handler; `APOTHECARY_TEST_LOGS=1` brings
  the lines back when they are what one is after.
