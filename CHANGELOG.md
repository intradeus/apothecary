# Changelog

Generated on each release by commitizen (`deploy/release.sh cut`, rules in `.cz.toml`). Do not edit released sections.

## v4.0.1 (2026-11-18)

### Fixes

- **builtin-flow**: update built-in reviewer to be less picky
- **builtin-flow**: update built-in reviewer to be less picky
- **flow**: update flow validation and add flow checks in CI

## v4.0.0 (2026-11-17)

### BREAKING CHANGE

- ticket_transition takes free-text `to:` (a tracker
status name or a bound string parameter); `status:` and the step names
are gone. The built-in builder and hands-on flows refuse to run until a
workshop binds todo_status, in_progress_status, in_review_status and
done_status. Migration 0004 is one-way: it drops remote comments,
remote status rows and the sync columns, and disables tracker_query
automations whose source disagrees with their workshop's. The sync and
unlink routes are removed.

### Features

- **records**: add a download all record button
- **run**: let take-over and Ask chat messages carry files
- **flow**: add start_held so an agent stage opens in its owner's hands
- **run**: make archived a run flag so archiving keeps the outcome
- **run**: give each chain its own card in the Watchers pane
- **kinds**: add the Claude Code harness
- **cli**: make ssh-config create, register and use its own SSH key
- **BREAKING** **tickets**: read one ticket source per workshop live, drop sync

### Fixes

- **ui**: move skills to sidebar instead of settings
- **ui**: update the settings UI and reviewer UI for some QoL upgrades
- **ask**: give files back and open no thread when a question fails
- **run**: lock the chat composers while their files upload
- **run**: name a chat file when it is claimed and keep that path
- **ask**: leave no files or thread behind when a question is refused
- **cli**: let the forward daemon answer the request that stops it
- **engine**: write a start hold's opening prompt and its messages at once
- **engine**: hold a start hold's sends until its stage prompt is written
- **cli**: stop the forward daemon deleting a successor's socket
- **engine**: send a start hold's early messages with its stage prompt
- **engine**: let the owner message or release a start hold before its prompt
- **engine**: record a start hold's opening turn so restarts keep it
- **flow**: count start_held among the fields a parameter is read in
- **flow**: add a new operates_on 'nothing' - to start run without any workitem
- **pgstore**: keep a migration's rebuild pending until it commits
- **run**: stop a manual outcome's card pointing at a Run now
- **run**: show a chain's quiet time to the second and fix outcome hint
- **cli**: let a stopping forward daemon finish its answers before exit
- **engine**: hold a release while the agent's question is open
- **engine**: keep the stage clock stopped on a question under a hold
- **engine**: count queued messages when releasing a question-parked hold
- **engine**: park on a live question after a restart past a release
- **engine**: keep a holder's question asked before its turn start is reported
- **engine**: let the holder's turn ask while the loop trails a stop
- **engine**: keep a turn whose question precedes the runner's stop
- **engine**: keep a release made before a question parks the run
- **engine**: hold a release until a delivered message's turn starts
- **engine**: hand a release back only after the holder's messages
- **engine**: decide a take-over's stop under the question lock
- **engine**: leave a turn alone whose question is queued at take-over
- **acpagent**: answer a take-over's stop before the holder's prompt
- **engine**: keep a pending ping's choices when read from the projection
- **cli**: let the forward daemon answer its stop request before closing
- **acpagent**: bound a take-over's stop request by the cancel grace
- **httpapi**: reject trailing data after a take-over body
- **engine**: stop a reattached turn on a forced take-over
- **httpapi**: read a take-over body of unknown length
- **engine**: stop the agent's turn in flight on a take-over
- **records**: remove journal naming attempt number
- **ci**: force ci pass before merge main
- **services**: update how services ports are passed to stage
- **cli**: forward port only if no other port conflict with it
- **cli**: create the ssh-config key owner-only and survive a 409 race
- **cli**: shut the forward daemon down gracefully and keep its socket
- **db**: add migration sum
- **tickets**: retry a moved issue by its stable id on Jira only
- **tickets**: let a cron automation pick its ticket from the live list
- **tickets**: migrate a user's hand-written tickets sharing one key

### Performance

- **session**: improve loading performance of session tab

## v3.6.0 (2026-11-16)

### Features

- **engine**: add port type to outputs
- **engine**: agent notes, the debug summary and the files a person changed
- **engine**: add new journal tool, replace run.ping_answers with run.journal
- **engine**: support for 'impasses' after budgetted verdict rejection
- **engine**: support for 'impasses' after budgetted verdict rejection

### Fixes

- **engine**: reset budget on debug continuation
- **cpui**: close log find on Esc from the log and end URLs at quotes
- **cli**: close the forward daemon's socket as soon as it exits
- **cli**: let the forward daemon answer its last request on exit
- **cli**: leave socket removal to the forward daemon's listener
- **cpui**: end a linkified URL at a quote
- **cpui**: cap log find marks by match count and reset copy tick
- **cpui**: ease copy/paste, links, find and scrolling in terminal and logs

### Refactors

- **artifacts**: refactor of the artifact's name standard

### Chores

- **global**: cleanup plan files

### Docs

- **design**: say log find highlights 300 matches either side

## v3.5.1 (2026-11-15)

### Fixes

- **workspace**: archived runs should be properly destroyed
- **workspace**: report a sandbox that refuses exec as broken and replace it
- **cli**: remove the forward daemon's socket before it exits
- **cpui**: darken the Dark theme and its ping card

### Performance

- **run**: performance upgrades for session events loading in UI

### Reverts

- **cli**: remove the forward daemon's socket before it exits

## v3.5.0 (2026-11-13)

### Features

- **tickets**: make the branch name format a per-workshop setting
- **swarm**: add war room live view of the full agent swarm
- **engine**: make the agent guide optional with no AGENTS.md default
- **ask**: support multiple sessions per run in the Ask pane

### Fixes

- **devcontainer**: add failing test sweep fix
- **devcontainer**: add sensible defaults
- **devcontainer**: updates and testing the CI tests in sandbox
- **devcontainer**: add missing k3d dependencies for integration tests in devcontainer

## v3.4.0 (2026-11-13)

### Features

- **workitems**: add workitem kinds for tickets and merge request, MRs are now first-class citizens
- **deployment**: full k8s native deployment, removed docker-compose version

### Fixes

- **environment**: pin tools version
- **ci**: fix tests and ci
- **ci**: read the pinned helm version past its comment
- **ui**: new merge request in command palette
- **ui**: ui fixes on vitals, logs, egress and mrs
- **ci**: upgrade CI speed and cache
- **ci**: speed upgrades for k8s integration tests

## v3.3.0 (2026-11-10)

### Features

- **flow**: pass ping answers as input

### Fixes

- **devcontainer**: pass the build-cache flags only when there is an image to build
- **tools**: upgrades to tools and usability : retries, provision log, default egress

## v3.2.6 (2026-11-10)

### Fixes

- **ui**: keep the focus on a select closed within a frame
- **workspace**: never keep a container the devcontainer CLI did not create

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
