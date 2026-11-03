# The tools bundle: the harness CLIs (opencode, codex + codex-acp, omp) and
# code-server as standalone builds for ONE architecture and BOTH libcs, so a
# repo-authored workspace image needs no Node, npm, apt or egress of its own
# to run an agent or the browser editor. Build context: this directory.
#
# One file, two consumers: the engine builds it at runtime on a docker host
# (internal/workspace/wsimage EnsureTools, then `docker create` + `cp` /tools out to
# <RunsDir>/tools-cache/<hash>, bind-mounted into every workspace at
# /opt/apothecary/tools), and CI publishes it as the apothecary/tools image a
# Kubernetes sandbox pod's tools-seed init container copies onto the run
# volume (seed.sh). Layout under /tools:
#
#   gnu/bin/   opencode codex codex-acp omp code-server   (glibc images)
#   musl/bin/  opencode codex codex-acp omp               (Alpine images)
#   musl/lib/  libstdc++.so.6 libgcc_s.so.1  (the bun-built binaries need them;
#              the musl wrappers add them only when the image lacks its own)
#   code-server/  the standalone tree (bundles its own node; glibc only)
#   TOOLS_HASH, seed.sh
#
# The version ARG defaults ARE the release pins: TestToolsDockerfilePinsMatchKinds
# (internal/workspace/wsimage) holds internal/project/kinds' constants to them. TOOLS_HASH is the
# hash of this directory (`apothecaryd tools-hash`), baked in so a seeded
# bundle can be compared with the one the engine expects. Keep it the LAST
# build arg so a hash-only change reuses the download layers.
ARG BUN_VERSION=1.4.4

# --- downloads --------------------------------------------------------------
FROM alpine:3.21 AS fetch
ARG TARGETARCH
ARG OPENCODE_VERSION=1.17.8
ARG CODEX_VERSION=0.160.1
ARG OMP_VERSION=18.13.1
ARG CODE_SERVER_VERSION=4.143.1
RUN apk add --no-cache curl tar
COPY fetch.sh /fetch.sh
# Readable by any user, set here: a chmod in a later layer would store every
# file it touches again (see the bundle below).
RUN sh /fetch.sh /out && chmod -R a+rX /out

# --- codex-acp ----------------------------------------------------------------
# The ACP bridge for codex is plain JS on npm with no prebuilt binaries; its
# own package.json compiles it with `bun build --compile`, so this does the
# same for both libcs — each in a bun image of THAT libc: bun on a musl host
# builds musl for an unsuffixed target (both outputs came out identical,
# requesting ld-musl). It finds codex through $CODEX_PATH, which the wrappers
# set to the bundled static binary — the npm-nested codex is never installed
# or resolved.
FROM alpine:3.21 AS acpsrc
ARG CODEX_ACP_VERSION=2.2.2
RUN apk add --no-cache curl tar && mkdir -p /acp \
 && curl -fsSL --retry 3 -o /acp/pkg.tgz "https://registry.npmjs.org/@agentclientprotocol/codex-acp/-/codex-acp-${CODEX_ACP_VERSION}.tgz" \
 && tar -xzf /acp/pkg.tgz -C /acp && test -f /acp/package/dist/index.js

FROM oven/bun:${BUN_VERSION} AS acp-gnu
ARG TARGETARCH
COPY --from=acpsrc /acp/package/dist/index.js /acp/index.js
RUN case "${TARGETARCH:-$(uname -m)}" in \
      amd64|x86_64) t=bun-linux-x64-baseline ;; \
      arm64|aarch64) t=bun-linux-arm64 ;; \
      *) echo "unsupported architecture: ${TARGETARCH:-$(uname -m)}" >&2; exit 1 ;; \
    esac \
 && bun build /acp/index.js --compile --minify --target="$t" --outfile /out/codex-acp \
 && chmod 0755 /out/codex-acp

FROM oven/bun:${BUN_VERSION}-alpine AS acp-musl
ARG TARGETARCH
COPY --from=acpsrc /acp/package/dist/index.js /acp/index.js
RUN case "${TARGETARCH:-$(uname -m)}" in \
      amd64|x86_64) t=bun-linux-x64-baseline-musl ;; \
      arm64|aarch64) t=bun-linux-arm64-musl ;; \
      *) echo "unsupported architecture: ${TARGETARCH:-$(uname -m)}" >&2; exit 1 ;; \
    esac \
 && bun build /acp/index.js --compile --minify --target="$t" --outfile /out/codex-acp \
 && chmod 0755 /out/codex-acp

# --- musl runtime libs --------------------------------------------------------
FROM alpine:3.21 AS musllibs
RUN apk add --no-cache libstdc++ libgcc && mkdir /lib-out \
 && cp -L /usr/lib/libstdc++.so.6 /usr/lib/libgcc_s.so.1 /lib-out/ \
 && chmod a+r /lib-out/*

# --- the bundle ---------------------------------------------------------------
# alpine, not scratch: seed.sh needs sh and cp. Every file arrives with its
# final mode, and assemble.sh only adds the wrappers: a chmod here of what an
# earlier layer holds would store it twice (4.3 GB instead of 2.3), in the
# image and in the build.
FROM alpine:3.21
COPY --from=fetch /out /tools
COPY --from=acp-gnu /out/codex-acp /tools/gnu/libexec/codex-acp
COPY --from=acp-musl /out/codex-acp /tools/musl/libexec/codex-acp
COPY --from=musllibs /lib-out/ /tools/musl/lib/
COPY assemble.sh seed.sh /tools/
RUN sh /tools/assemble.sh && rm /tools/assemble.sh
ARG TOOLS_HASH=dev
RUN printf '%s\n' "$TOOLS_HASH" > /tools/TOOLS_HASH
ENTRYPOINT ["/tools/seed.sh"]
