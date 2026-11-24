#!/bin/sh
# The sysbox rig node's entrypoint: sysbox's daemons first (the manager,
# then the filesystem), each waited for by its socket, then k3s as the
# container's main process. sysbox must never restart under live pods: the
# rig is restarted only with none (rig.sh down, then up).
set -eu
mkdir -p /var/lib/sysbox /var/log /run/sysbox
sysbox-mgr --log /var/log/sysbox-mgr.log >/var/log/sysbox-mgr.out 2>&1 &
for _ in $(seq 60); do [ -S /run/sysbox/sysmgr.sock ] && break; sleep 1; done
[ -S /run/sysbox/sysmgr.sock ] || { cat /var/log/sysbox-mgr.out /var/log/sysbox-mgr.log >&2; exit 1; }
sysbox-fs --log /var/log/sysbox-fs.log >/var/log/sysbox-fs.out 2>&1 &
for _ in $(seq 30); do [ -S /run/sysbox/sysfs.sock ] && break; sleep 1; done
[ -S /run/sysbox/sysfs.sock ] || { cat /var/log/sysbox-fs.out /var/log/sysbox-fs.log >&2; exit 1; }
exec /bin/k3s "$@"
