#!/bin/sh
# Container-START entrypoint (devcontainer feature `entrypoint`, runs as the
# container user before the workspace's real command). Sources the per-run
# firewall config the engine wrote into the bind-mounted shim dir, brings up
# the egress firewall, then execs the original command so the container
# keeps running.
#
# init-firewall.sh owns fail-closed: on any error its own trap slams egress
# shut and writes NO lock file, so the engine's waitReady probe of
# /run/apothecary-firewall.done times out into an honest env_failed while
# the container stays up for inspection. We therefore ignore its exit and
# ALWAYS exec — a firewall init that exited the entrypoint would kill the
# container and lose that inspectability. A non-root container user makes
# iptables fail here (fail-closed, same path); the fix is a root container
# user or the firewall-off escape hatch.
if [ -f /opt/apothecary/firewall.env ]; then
  set -a
  . /opt/apothecary/firewall.env
  set +a
fi

if [ -x /usr/local/bin/init-firewall.sh ]; then
  /usr/local/bin/init-firewall.sh || echo "apothecary-entrypoint: firewall init failed (fail-closed); container stays up for inspection" >&2
fi

exec "$@"
