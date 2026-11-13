# M18 fix 3 — egress broke after an engine restart (stale proxy.env + dead tunnel)

Live: git push "Failed to connect to 127.0.0.1:<port>" after rebuilding
the engine and hitting Try again. Root cause was a latent restart bug the
proxy exposed: the reverse tunnel is a subprocess that dies with the
engine, and the ensureEnv REUSE path (recovery/resume of a running
workspace) refreshed run.env but never re-established the tunnel — so the
workspace's engine/proxy loopback forwards were gone. The proxy also binds
a random port with a process-random cred secret, so a restart invalidated
the workspace's proxy.env. Fixes: (1) ports.WorkspaceReattacher optional
capability — the provisioner's Reattach re-establishes the tunnel
(idempotent); ensureEnv reuse path calls it. (2) reuse path
also rewrites proxy.env (current port + creds) via a shared
encodeEnvFile. This also fixes ask_human across restarts (same dead-tunnel
cause). Fake provisioner doesn't implement Reattach (no tunnel) — no-op.
