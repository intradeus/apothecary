# The client signs in through the browser, forwards in the background, and updates itself (2026-11-11)

- **Browser login, the device authorization grant (RFC 8628, as
  `gh auth login` does).**
  - `apothecary login <url>` opens a sign-in (`POST /api/cli/logins`, public)
    and opens the browser on `/cli-login?code=XXXX-XXXX`.
  - The person approves it there, signed in as usual. The code shown on the
    page and in the terminal is what they compare.
  - The client polls `POST /api/cli/logins/token` with its secret device
    code until it gets a token.
  - One flow for a desktop and for a machine without a browser
    (`--no-browser` prints the address): no localhost callback, no PKCE.
  - Pending sign-ins live in the engine's memory (one replica), last 10
    minutes, and number at most 1000. A token is minted under the lock, so
    two racing approvals cannot mint two.
  - `--token <personal token>` stays, for scripts and CI. The password
    login is gone from the client.
- **The client's token expires after a stretch of disuse, not on a date.**
  - Migration 0002 adds `api_tokens.idle_seconds`. A token with one is
    refused once its last use (or its creation) is older than the window,
    and every use slides it (touched at most once a minute).
  - The window is `APOTHECARY_CLI_TOKEN_IDLE` (default `168h`). Tokens made
    in the UI never get one.
  - Account → API tokens shows such a token as the CLI's, "signs out after
    N days unused".
  - A token approved but never collected (the client quit) idles out by
    itself.
- **Local ports are random by default.**
  - A run's port lands on a free local port, and the client prints the
    mapping.
  - `--same` keeps the numbers, for a frontend that calls its API at
    `localhost:<port>`; `port:local` names one.
  - The local port a run's port got is remembered (`ports.json`, the last
    200 runs), so forwarding it again lands on the same one while it is
    free.
- **Forwards live in a per-user background daemon** (`__forward-daemon`).
  - It is started by the first `forward`, detached (its own session on Unix,
    `DETACHED_PROCESS` on Windows), so closing or killing the terminal
    leaves it running.
  - Commands reach it over a unix socket in `<config>/apothecary/forwards/`
    (0700, the socket 0600), with HTTP+JSON on it. Go speaks AF_UNIX on
    Windows 10 and later too.
  - It reads the sign-in again on every new forward and connection, so a new
    login reaches running forwards.
  - It drops a run whose workspace or sign-in is gone, and exits once nothing
    is forwarded, or 30 s after starting with nothing asked.
  - It writes its own log (`forward logs`). `forward status` names each run
    by its ticket key and title and its workspace. A run is named by its id
    or its workspace (the env id the SSH door also uses).
- **`apothecary update` installs the release of the engine's version, not
  the newest.**
  - The engine says its version on the public `/api/auth/status`; it is
    `main.version`, stamped at build time.
  - The client downloads `apothecary-<os>-<arch>` and `SHA256SUMS` from that
    GitHub Release, refuses a checksum mismatch, and replaces itself with
    one rename. Windows moves the running `.exe` aside first.
  - An engine built from source (`dev`) has no release to match. Once a day a
    command checks the engine's version and says when the client differs.
- **The UI copies the commands.**
  - The Ports view shows `apothecary forward <ws>` and `--same`, and a copy
    button per port (`apothecary forward <ws> <port>`).
  - The Services view has the same per-port copy button.
  - The commands name the workspace, not the run id.
