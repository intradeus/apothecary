# The build cache only goes on an image that is built (2026-11-10)

- Found live, right after the container-replacement fix: the CLI replaced
  the foreign container, then failed to create its own. With a gateway,
  `up` always carried `--cache-from` (twice) and `--cache-to`. The CLI
  writes `cache_from` into its compose override under the service's
  `build:`, and the repo's prebuilt compose file had reset that service's
  build (`build: !reset null`). So `cache_from` landed directly on the
  service, and compose refused the override
  (`services.devcontainer.cache_from false schema`). It had never shown up
  before because the CLI skips the build when it reuses a container, and
  the repo's `initializeCommand` always left one to reuse.
- `devcontainerDef.cacheBuilds`: a single-container devcontainer keeps the
  flags. A compose one keeps them only when its service has a `build:` in
  the merged configuration (`docker compose -f … config --format json`,
  which resolves overrides and `!reset` the way the build will). If the
  configuration cannot be read, or names no such service, the cache stays
  off: the flags only ever buy speed, and a wrong guess fails the build.
  The provisioning log says when and why the cache is off. An image-only
  service has nothing to cache anyway.
