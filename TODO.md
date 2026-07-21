- 3) merge conflicts
- 4) `channels` fold into `integrations`.
- 5) db migration + migration table checks and simplify
- 6) openapi + client gen for frontend.
- 7) redo everything in DDD
- 8) remove the flow's and project default model. Let each agent stage define its own agent + harness + model.
- 9) ~~Implement devpod provisionner adapter~~ → done as the `devcontainer` provisioner
     (@devcontainers/cli; DevPod upstream is abandoned — see IMPLEMENTATION_NOTES
     "Devcontainer provisioner"). Still open: OMP harness adapter.
- 10) Devcontainer provisioner follow-ups: harness binaries inside repo-authored
      devcontainers (InstallLayer only exists in the image pipeline); egress
      enforcement for EgressOpen provisioners (injected firewall feature or
      internal-network sidecar); reap compose side services on Destroy; remap
      fixed appPort/compose `ports:` collisions across concurrent runs.