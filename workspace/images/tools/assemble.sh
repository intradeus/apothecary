#!/bin/sh
# Final-stage assembly: the wrapper scripts. Every path a wrapper names is
# the IN-CONTAINER mount point (/opt/apothecary/tools) — the same on both
# hosts and independent of the bundle's hash — so the bundle can be extracted
# anywhere engine-side and still run once mounted.
#
# Everything else arrives with its final mode (the Dockerfile's stages set
# it): a chmod here of a file an earlier layer holds would store it twice.
set -eu
T=/opt/apothecary/tools
root=/tools

# musl images: the bun-built binaries (opencode, omp, codex-acp) need
# libstdc++/libgcc, which a minimal Alpine image may not carry. Add ours only
# when the image lacks its own, so children never load our copy over the
# image's.
musl_libs="[ -e /usr/lib/libstdc++.so.6 ] || export LD_LIBRARY_PATH=$T/musl/lib\${LD_LIBRARY_PATH:+:\$LD_LIBRARY_PATH}"

wrap() { # wrap <path> <script body>
  printf '#!/bin/sh\n%s\n' "$2" > "$1"
  chmod 0755 "$1"
}

wrap "$root/gnu/bin/code-server" "exec $T/code-server/bin/code-server \"\$@\""
wrap "$root/gnu/bin/codex-acp" "export CODEX_PATH=$T/gnu/bin/codex
exec $T/gnu/libexec/codex-acp \"\$@\""
for b in opencode omp; do
  wrap "$root/musl/bin/$b" "$musl_libs
exec $T/musl/libexec/$b \"\$@\""
done
wrap "$root/musl/bin/codex-acp" "$musl_libs
export CODEX_PATH=$T/musl/bin/codex
exec $T/musl/libexec/codex-acp \"\$@\""

# seed.sh comes from the build context, which a docker host writes 0644.
chmod 0755 "$root/seed.sh"
