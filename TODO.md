- 3) merge conflicts
- 4) `channels` fold into `integrations`.
- 5) db migration + migration table checks and simplify
- 6) openapi + client gen for frontend.
- 7) redo everything in DDD
- 8) remove the flow's and project default model. Let each agentic stage define its own agent prompt + harness + model.
- 10) Devcontainer provisioner follow-ups. DONE: egress firewall (injected
      devcontainer feature + fallback-image path, see IMPLEMENTATION_NOTES
      "Devcontainer egress firewall"); dynamic port exposure (supersedes the
      old appPort/compose-collision item — no published host ports needed);
      node dropped from workspaces (static shim binary). STILL OPEN: harness
      binaries inside repo-authored devcontainers (InstallLayer only exists in
      the §8 image pipeline — the repo's own devcontainer must provide them);
      reap compose side services on Destroy (unlabeled by the CLI); firewall
      for compose primary services (feature works, but would break
      sibling-service networking — needs compose-subnet allowance); non-root
      container user makes the firewall entrypoint fail closed.
- 11) "relay", "reverse tunnel", "bridge": related, not identical.
      - coder REVERSE TUNNEL: `coder ssh --remote-forward` maps an
        engine-host loopback port onto the workspace's own 127.0.0.1
        (workspace→engine). One supervised ssh process.
      - devcontainer engine BRIDGE = two hops giving the same loopback parity
        without host↔container IP traffic (blocked on hardened hosts):
        engine-side unix listeners in the bind-mounted shim dir (Go, bridge.go)
        + an in-container RELAY (the shim binary) re-exposing them on the
        workspace's 127.0.0.1. "Relay" = the in-container shim process; "bridge"
        = the whole engine-side+in-container pair.
      - port exposure REVERSE RELAY (`shim relay --reverse`) is the same relay
        run the other direction (engine→workspace app) for feature A.
      Naming could be unified, but the directions/transports genuinely differ.
- 12) docker compose should link the docker socket ?