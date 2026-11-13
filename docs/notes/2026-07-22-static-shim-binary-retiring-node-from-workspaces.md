# Static shim binary (retiring node from workspaces)

The two in-workspace node helpers (`ask-human.mjs` MCP server, the
devcontainer `relay.mjs`) are replaced by ONE static Go binary,
`workspace/shim/` (`shim ask-human` | `shim relay`), so repo-authored dev
containers no longer need node (the earlier hard "workspace has no node"
failure is gone). Decisions:

- **Built by docker, not the host toolchain.** `wsimage.EnsureShim`
  (content-addressed `apothecary/shim:<sha16>`, `docker build` →
  `create`/`cp`/`rm` extract, cached under `<RunsDir>/shim-cache/`) compiles
  the embedded source in a `golang:alpine` stage. Same daemon that runs workspaces ⇒ arch always matches.
  `go:embed` of a prebuilt binary would be a chicken-and-egg; a nested
  `go.mod` would break embed, so the shim is a normal `package main` in the
  main module (`go mod init` happens inside the builder image).
- **wsimage now walks subdirectories** (`baseContextHash`/`ensureBase` used
  `fs.ReadDir(".")` and silently skipped dirs) — required to hash + ship the
  embedded `shim/` tree.
- The shim keeps `ask-human.mjs`'s protocol byte-for-byte (204 re-poll via
  `X-Ping-Id`, 401 → re-read run.env, 60 s backoff cap, 10 min unreachable
  limit, the `other —` rendering) and gains real unit tests the `.mjs` never
  had (httptest for the MCP loop, real sockets for the relay). MCP
  registration moved from `["node","…ask-human.mjs"]` to
  `["/opt/apothecary/shim","ask-human"]` in the opencode/codex ADAPTERS
  (`internal/adapters/{opencode,codex}`), not the kinds. **node stays in the
  pipeline image** — the npm-distributed harnesses still need it; only the
  shim's own runtime dependency died.
