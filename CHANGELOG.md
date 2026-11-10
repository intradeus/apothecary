# Changelog

Generated on each release by commitizen (`deploy/release.sh cut`, rules in `.cz.toml`). Do not edit released sections.

## v3.2.5 (2026-11-10)

### Fixes

- **workspace**: sandbox permission issue, reaper not working for failed provisions

## v3.2.4 (2026-11-09)

### Fixes

- **engine**: put the checkout on the run's branch before the devcontainer build

## v3.2.3 (2026-11-09)

### Fixes

- **tests**: update golden logs and test timing issues
- **engine**: run the devcontainer CLI on the scoped branch, not main

## v3.2.2 (2026-11-09)

### Fixes

- **deployment**: wireguard deploment issue

## v3.2.1 (2026-11-09)

### Fixes

- **sandbox**: sysbox host user improvements

## v3.2.0 (2026-11-09)

### Features

- **deployment**: add docker outside of docker support to launch containers dynamically in sandbox

### Fixes

- **ci**: fix flaky unit tests
- **ci**: fix CI for k8s
- **analytics**: fix analytics dashboards

## v3.1.0 (2026-11-04)

### Features

- **ci**: add manual release

### Fixes

- **ci**: add github release on release
- **ci**: fixing CI failed timing tests
- **ci**: Commit convention
- **ci**: fixing tests
- **infra**: read keys from /etc/wireguard (AppArmor on Ubuntu 26.04)

## v3.0.0 (2026-11-03)

### Features

- **tools**: reduce tools image size by using multi-stage
- **browser**: use microsoft mcp browser image instead of building custom one, allow overrides
- **ci**: update release mechanism
- add new built-in schema file viewers
- add new manual stage
- add system notifications
- add per-harness options
- full run on observability
- setup CI for test suite + upgrade to test structure
- ticket source syncing, ticket type/status mapping
- add window file pane tree layout
- frontend normalization + test stack
- new theme
- **flow**: reuse_session opts a stage out of keeping its session
- **engine**: a stage keeps its session across attempts
- **BREAKING** **run**: nobody talks to an agent without taking it over
- **engine**: outcomes override everything but a take-over
- **BREAKING** **flow**: a run has one cursor
- add ssh connection through gateway, allows connect vscode

### Fixes

- **ci**: fix CI missing disk space.
- fix built-in schema viewers
- make sure secrets are stripped from logs and spans and other outputs
- fix tests for macos
- refresh client token on pw change, reject nul pwd
- **sshdoor**: an account disabled during its handshake is out
- **wsname**: a run's tail is four characters
- **engine**: an answer the queue holds is accepted
- **engine**: a ping rebuilt from the log keeps its file types
- **engine**: a verb landing while a run parks acts on the parked run
- **engine**: a crash before an attempt's first prompt starts that attempt
- **engine**: recovery after a completion routes it the way the loop would (D1)
- **engine**: a recovered wait keeps its timeout from the first park (D9)
- **engine**: a run parked on its holder stays parked across restarts (D10)
- **engine**: a gate answered while its phase was parked survives a crash (D8)
- **engine**: a harness session is recorded whichever path meets it
- **httpapi**: shutting down ends event streams and ping holds
- **ask**: a pane whose session is gone answers on a fresh one
- **sshdoor**: deliver a request's reply before closing its channel
- add vscode desktop setting's name

### Refactors

- **project**: resolve is integrations
- **internal**: engine/ holds the services around a live run
- **internal**: project/ groups what a project is configured with
- **internal**: workspace/ groups what concerns a run's container
- **internal**: run/ holds what a run is
- **internal**: flow/ holds the language
- **internal**: adapters/ gains what drives the outside
- **internal**: skills/ holds the format and its library
- **internal**: account/ is who a person is
- **internal**: platform/ holds the building blocks
- **BREAKING** **flow**: gates are answered by people
- **adapters**: one shell.Quote and one shell.Tail
- **runinput**: what starts a run leaves the engine
- **engine**: runloop.go splits by stage type
- **runcore**: an attempt's end and a take-over's tables are pure
- **runcore**: the idle tick is planned, the engine carries it out
- **runcore**: the interpreter's functional core leaves the engine
- **engine**: the interpreter's routing decisions are pure functions
- **engine**: the live loop stops repeating what the fold already did
- **engine**: the live loop applies the fold to everything it appends
- **engine**: runState holds the fold and nothing else
- **engine**: the fold is (*runState).apply
- **ports**: the notification port moves to ports/notifyport; ports is a directory of ports
- **ports**: the project-browser port moves to ports/browserport
- **ports**: the egress-gateway port moves to ports/egressport
- **ports**: the issue-tracker port moves to ports/trackerport
- **ports**: the repository-host port moves to ports/repoport
- **ports**: the harness port moves to ports/agentport
- **ports**: the workspace port moves to ports/envport
- **engine**: Verdict and VerdictComment live in the engine
- **httpapi**: split the server and its route table by area
- **apothecaryd**: split serve into wiring, background loops and serving
- **engine**: engine.New(Deps) replaces every setter
- **services**: service shim protocol and run-page control leave the engine
- **browsermcp**: agents' browser sessions leave the engine
- **runports**: port exposure, discovery and the editor leave the engine
- **authsink**: login push and the auth broker leave the engine
- **ask**: the Ask pane service joins its store in internal/ask
- **egress**: live egress controls and the audit trail leave the engine
- **envcheck**: the compliance gate and its job registry leave the engine
- **wsinfo**: workspace reads leave the engine
- **envstate**: workspace op gate and reconciled statuses leave the engine
- **resources**: the resource sampler leaves the engine
- **mrs**: the merge-request read side leaves the engine
- **ports**: review-thread helpers beside IsFactoryReply
- **run**: run.Reader is the shared read side for run views
- **redact**: the secret redactor in its own package
- **modelcheck**: model and credential checks leave the engine
- **bootstrap**: workspace bootstrap in its own package
- **resolution**: Resolution and its checks in their own package
- **agentspec**: Ask agent spec and workspace agent list leave the engine
- **wsname**: workspace naming in its own package
- **shell**: move shellQuote and tail out of the engine

### Chores

- squash migrations before 1.0.0 release
- upgrades to test speeds + new tests
- add test coverage + increase test coverage

### Docs

- add new UI design documents
- the README maps internal/
- **refactor**: the follow-ups to the clean architecture refactor
- **refactor**: the clean architecture refactor is done
- **refactor**: record Phase 4
- **refactor**: record Phase 3 deviations and the post-phase fixes
- **refactor**: record Phase 2 deviations and the phase-end check
- **refactor**: record Phase 1 deviations
- **refactor**: clean architecture plan with baselines

## v2.0.78 (2026-10-30)

Last release of the legacy `v2.0.<ci run>` series (no changelog). Semver releases start after it.
