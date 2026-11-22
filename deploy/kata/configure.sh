#!/bin/sh
# Writes configuration-apothecary.toml beside the Kata release's Cloud
# Hypervisor configuration, edited for Apothecary's sandboxes, and checks
# every edit took. POSIX sh: the rig runs it in an alpine helper over the
# unpacked volume, install.sh on the box itself.
#
#   configure.sh <dir>   dir: .../share/defaults/kata-containers/runtime-rs
#
# The edits (docs/kubernetes.md, "Installing Kata"; the note
# 2026-11-22-kata-sandboxes-run-on-emptydir-disks says why each):
#   - each emptyDir an ext4 disk in the guest (block-plain);
#   - the VM is the pod's limits, no more (static_sandbox_resource_mgmt);
#   - freed guest memory goes back to the node, the mem-agent on.
set -eu

d=${1:?usage: configure.sh <runtime-rs defaults dir>}
src=$d/configuration-clh-runtime-rs.toml
dst=$d/configuration-apothecary.toml
[ -f "$src" ] || { echo "configure.sh: no $src" >&2; exit 1; }

sed -e 's/^reclaim_guest_freed_memory = .*/reclaim_guest_freed_memory = true/' \
  -e 's/^emptydir_mode = .*/emptydir_mode = "block-plain"/' \
  -e 's/^mem_agent_enable = .*/mem_agent_enable = true/' \
  -e 's/^memcg_period_secs = .*/memcg_period_secs = 30/' \
  -e 's/^compact_period_secs = .*/compact_period_secs = 30/' \
  -e 's/^static_sandbox_resource_mgmt = .*/static_sandbox_resource_mgmt = true/' \
  "$src" >"$dst"
for k in 'reclaim_guest_freed_memory = true' 'emptydir_mode = "block-plain"' 'mem_agent_enable = true' \
  'memcg_period_secs = 30' 'compact_period_secs = 30' 'static_sandbox_resource_mgmt = true'; do
  grep -qx "$k" "$dst" || { echo "configure.sh: $dst has no line $k" >&2; exit 1; }
done
