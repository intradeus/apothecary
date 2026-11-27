package workspace

import (
	"fmt"

	"apothecary/internal/adapters/kube"
	"apothecary/internal/ports/envport"
)

// sandboxRuntime is how a runtime shapes a run's sandbox pod: everything
// podManifest and the daemon door decide per runtime, read from this table
// and never from a runtime's name. What the runtime can run (its
// capabilities) is the port's (envport.Runtime); this is what it takes to
// get there on Kubernetes.
type sandboxRuntime struct {
	name envport.Runtime
	// class is the RuntimeClass the pod names by default ("" = the node's
	// own runtime). The integration may name another (KubeConfig.RuntimeClass).
	class string
	// privileged gives the sandbox container, and the tunnel's wg container,
	// a privileged security context; off, wg gets NET_ADMIN alone.
	privileged bool
	// ownUserNamespace asks the kubelet for a user namespace of the pod's own
	// (hostUsers: false).
	ownUserNamespace bool
	// vm is a runtime whose pod is a virtual machine (Kata):
	//   - no volumeMount.subPath (Kata does not support it), and never a
	//     filesystem claim, whatever the integration's storage (which the
	//     other runtimes keep): one arrives over virtio-fs, where docker
	//     cannot keep its layers. Either the run's claim as a raw block
	//     device from the VM storage class (KubeConfig.VMStorageClass), one
	//     disk the sandbox formats and mounts, kept across a stop; or, with
	//     no such class, the run folder and the inner docker's data on an
	//     emptyDir each (a block emptyDir is a disk in the guest), gone with
	//     the pod, so the sandbox cannot be stopped and kept (CanStopUnder);
	//   - a minimal /dev in a privileged container, which the sandbox
	//     completes before dockerd (APO_DEV_PRELUDE);
	//   - a size, required: the pod's limits size the VM.
	vm bool
	// usernsNesting is a runtime that nests a docker daemon in the pod's own
	// user namespace with plain runc (the userns runtime): the sandbox
	// container's security context that takes (usernsSecurity), and the
	// image's switch (APO_USERNS_NESTING), which checks the cluster gave the
	// pod its user namespace and a writable cgroup and runs the daemon's
	// containers through apo-runc. Only with ownUserNamespace, never
	// privileged.
	usernsNesting bool
}

// sandboxRuntimes is the table, one row per runtime the adapter can build.
var sandboxRuntimes = map[envport.Runtime]sandboxRuntime{
	envport.RuntimePrivileged: {name: envport.RuntimePrivileged, privileged: true},
	// sysbox's documented pod shape: runtimeClassName + hostUsers: false
	// (sysbox ≥ 0.7 on containerd ≥ 2.0.5). Left on the host's user
	// namespace, sysbox still makes one for the container, but containerd
	// has set up the pod's network namespace before that, and the sandbox
	// sysctls it then writes (net.ipv4.ip_unprivileged_port_start) fail from
	// the user namespace that does not own it: "ip_unprivileged_port_start:
	// permission denied", and the pod never starts. With hostUsers false the
	// kubelet makes the user namespace first and sysbox adopts its mapping.
	// README, "Cluster requirements".
	envport.RuntimeSysbox: {name: envport.RuntimeSysbox, class: "sysbox-runc", ownUserNamespace: true},
	// Kata: privileged inside its VM (the VM is the boundary; containerd's
	// privileged_without_host_devices keeps the node's devices out), no
	// hostUsers (the guest kernel is the pod's own).
	envport.RuntimeKata: {name: envport.RuntimeKata, class: "kata", privileged: true, vm: true},
	// userns: plain runc in the pod's own user namespace (hostUsers: false),
	// under a handler that makes the pod's cgroup writable (containerd's
	// cgroup_writable). Nothing but that handler is installed on the node.
	// README, "Cluster requirements".
	envport.RuntimeUserns: {name: envport.RuntimeUserns, class: "runc-userns", ownUserNamespace: true, usernsNesting: true},
}

// lookupRuntime is the row for name; "" is privileged, the zero
// integration's sandbox.
func lookupRuntime(name envport.Runtime) (sandboxRuntime, error) {
	if name == "" {
		name = envport.RuntimePrivileged
	}
	rt, ok := sandboxRuntimes[name]
	if !ok {
		return sandboxRuntime{}, fmt.Errorf("kubernetes: the adapter cannot build a %q sandbox", name)
	}
	return rt, nil
}

// sandboxSecurity is the sandbox container's security context under rt (the
// runtime probe's too); nil leaves the cluster's default.
func (rt sandboxRuntime) sandboxSecurity() *kube.SecurityContext {
	switch {
	case rt.privileged:
		return &kube.SecurityContext{Privileged: kube.Ptr(true)}
	case rt.usernsNesting:
		return usernsSecurity()
	}
	return nil
}

// usernsSecurity is what a docker daemon, and a Kubernetes node on it, need
// of an unprivileged container in a user namespace of the pod's own. Every
// capability is the namespace's, never the node's: the pod's root owns
// nothing of the node.
//   - procMount Unmasked: the kernel lets a nested container mount a fresh
//     /proc (and a sysfs) only where one is fully visible, and the runtime's
//     masked paths cover parts of it;
//   - SYS_ADMIN for mounts and namespaces (overlay layers, each nested
//     container's own), NET_ADMIN for docker's bridges and iptables, MKNOD
//     for nested containers' devices, SYS_RESOURCE for the limits dockerd and
//     a kubelet raise, SYS_PTRACE for runc and a kubelet reading other
//     processes' /proc;
//   - seccomp and AppArmor Unconfined: their runtime defaults refuse mount,
//     unshare and the rest of what nesting is made of. This is the
//     runtime's trade-off: the whole kernel API is reachable from the
//     sandbox, its isolation the user namespace alone.
func usernsSecurity() *kube.SecurityContext {
	return &kube.SecurityContext{
		ProcMount:       "Unmasked",
		Capabilities:    &kube.Capabilities{Add: []string{"SYS_ADMIN", "NET_ADMIN", "MKNOD", "SYS_RESOURCE", "SYS_PTRACE"}},
		SeccompProfile:  &kube.SeccompProfile{Type: "Unconfined"},
		AppArmorProfile: &kube.AppArmorProfile{Type: "Unconfined"},
	}
}

// NestedUserNamespaceAdds is what a cluster whose nodes give user-namespaced
// pods more than 65536 ids (KubeConfig.NestedUserNamespaces) adds to the
// runtimes the adapter builds: user namespaces inside the sandbox, for every
// runtime that nests in the pod's own user namespace with plain runc. Not
// sysbox, which maps its own 65536 whatever the kubelet gives
// (docs/notes/2026-11-26-sysbox-cannot-run-nested-user-namespaced-pods-even-with-a-wider-id-range.md).
// nil when off.
func NestedUserNamespaceAdds(on bool) map[envport.Runtime]envport.RuntimeCapabilities {
	if !on {
		return nil
	}
	out := map[envport.Runtime]envport.RuntimeCapabilities{}
	for name, rt := range sandboxRuntimes {
		if rt.usernsNesting {
			out[name] = envport.RuntimeCapabilities{UserNamespaces: true}
		}
	}
	return out
}

// caps is what the runtime gives the sandbox (the port's table).
func (rt sandboxRuntime) caps() envport.RuntimeCapabilities { return rt.name.Capabilities() }

// className is the RuntimeClass the pod names: the integration's override,
// else the runtime's own.
func (rt sandboxRuntime) className(override string) string {
	if override != "" {
		return override
	}
	return rt.class
}

// CanStop says why sandboxes on this workspace storage cannot be stopped
// and kept (stop-on-idle): an emptyDir goes with its pod.
func CanStop(storage string) error {
	if storage != "pvc" {
		return fmt.Errorf("a sandbox on %s storage loses its checkout and containers with its pod: stopping idle sandboxes needs the Kubernetes integration's storage set to pvc", storage)
	}
	return nil
}

// CanStopUnder says why a sandbox under runtime name cannot be stopped and
// kept on this integration's storage: a VM's disks are the VM storage
// class's raw-block claim, kept, or with no such class emptyDirs of its own
// (the vm trait), gone with its pod; every other runtime's are storage's.
func CanStopUnder(name envport.Runtime, storage, vmStorageClass string) error {
	rt, err := lookupRuntime(name)
	if err != nil {
		return err
	}
	if !rt.vm {
		return CanStop(storage)
	}
	if vmStorageClass == "" {
		return fmt.Errorf("a %s sandbox keeps its checkout and containers on disks inside its VM, which go with its pod: stopping idle %s sandboxes needs the Kubernetes integration's VM storage class (a class that provisions raw block volumes)", name, name)
	}
	return nil
}

// A VM sandbox on its raw-block claim (KubeConfig.VMStorageClass): the
// device's path in the sandbox container, where the init containers' run
// volume is mounted aside (apo-dockerd binds its shim and tools into the
// run folder on the disk), and that volume's bound: the shim and the tools
// bundle (about 2.3 GB), never kept.
const (
	vmDataDevice = "/dev/apo-data"
	vmSeedDir    = "/var/lib/apo-seed"
	vmSeedSize   = "4Gi"
)
