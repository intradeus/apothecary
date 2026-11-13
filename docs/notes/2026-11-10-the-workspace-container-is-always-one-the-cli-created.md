# The workspace container is always one the CLI created (2026-11-10)

- A repository whose `initializeCommand` runs `docker compose … up -d` with
  the devcontainer's own service (so VS Code reuses it) failed to provision:
  `workspace init: … chgrp: cannot access '/opt/apothecary'`, then "Dev
  container not found" on every exec. For a compose configuration the
  devcontainer CLI looks its container up by compose project and service,
  not by our id-labels, and reuses whatever it finds as it is: no
  `/opt/apothecary`, no `/apothecary`, none of our labels. Not specific to
  Kubernetes, and not a regression.
- `initializeCommand` runs on every `up`, a recovery's too, and the CLI's
  compose override changes the service's config, so compose replaces our own
  container with the repository's. A check for our container before `up`
  would therefore only have covered the first provision.
- `Adapter.up` checks after `up` instead: the returned container must carry
  the workspace's id-label (`docker ps -aq --filter id=… --filter label=…`
  on the run's daemon). When it does not, one more `up` with
  `--remove-existing-container` replaces it: the CLI removes the container it
  found and creates the service's container with our mounts and labels, the
  sibling services left running. If that still hands back a foreign
  container (or the call was already a recreate), the provision fails saying
  the CLI reused a container it did not create and pointing at
  `initializeCommand`, before anything runs in it. The exec log says when a
  container was replaced.
- Checked against the real CLI (0.86.0) and docker with a compose
  devcontainer whose `initializeCommand` starts the service: a plain `up`
  returns a foreign container on the first provision and again on a later
  one; the retry returns ours with the shim mount each time, and the sibling
  service keeps running. Unit tests pin the retry, the refusal, no second
  recreate after a recreate, and the lookup's failures.
