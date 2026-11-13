# No run reaches another through the gateway (2026-11-13)

- **The gap:** every run of a project is a WireGuard peer on the project's
  one gateway (`10.90.0.0/16`, a `/32` each), and that subnet was not a
  protected destination. Nothing in the sandbox filters what arrives on its
  tunnel, and the inner dockerd publishes ports on every pod address. So one
  run could reach another's published ports (a k3d API, its registry, a test
  postgres):
  - through the forward chain: any port in deny mode or during the run's
    provisioning window, the allowed ports and CIDRs in allow mode;
  - through the filter on :80/:443: it judges the SNI/Host, then dials the
    destination the client chose, so an allowlisted name sent to another
    run's address was dialed.
- **Now:** once wg0 is up, the gateway appends wg0's subnet to
  `--protect-cidrs` (`setupWG` parses it from `--wg-addr`). Protection
  comes first in the forward chain and in the filter's verdict, ahead of
  every mode, hatch and open window, so the one list closes both routes.
  The gateway's own address is not forwarded (DNS, the image and build
  caches, the gallery reach it through the input chain), so nothing a run
  needs is dropped.
- Runs of different projects were already apart: separate gateways, each
  admitting only its own project's sandboxes, and sandbox pods admit only
  the engine.
- **Tests:** the filter refuses another run's address for an allowlisted
  SNI, Host or IP, in deny mode and in a provisioning window
  (`TestHandleNeverDialsAnotherRun`), `main` renders the subnet into the
  protected drop, and the live Kubernetes test provisions a second run of
  the same project, listening on :443 and on the hatch port 9418, which the
  first run reaches by neither route (`liveNoCrossRun`).
