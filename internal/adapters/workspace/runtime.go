package workspace

import (
	"fmt"

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
	//   - no volumeMount.subPath (Kata does not support it), so the run
	//     folder and the inner docker's data are a volume each, and on
	//     emptyDir storage only: a block emptyDir is a disk in the guest,
	//     while a claim from a directory-backed class arrives over virtio-fs,
	//     where docker cannot keep its layers;
	//   - a minimal /dev in a privileged container, which the sandbox
	//     completes before dockerd (APO_DEV_PRELUDE);
	//   - a size, required: the pod's limits size the VM.
	vm bool
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

// CheckStorage refuses a runtime the integration's workspace storage
// ("pvc" or "emptydir") cannot serve: a VM runtime's volumes must be
// emptyDirs (the vm trait says why). The kind checks it when an
// integration approves runtimes, before any workshop picks one.
func CheckStorage(name envport.Runtime, storage string) error {
	rt, err := lookupRuntime(name)
	if err != nil {
		return err
	}
	if rt.vm && storage != "emptydir" {
		return fmt.Errorf("the %s runtime needs the integration's storage set to emptydir, not %s (a claim reaches its VM over virtio-fs, where docker cannot keep its layers)", name, storage)
	}
	return nil
}

// CanStop says why sandboxes on this workspace storage cannot be stopped
// and kept (stop-on-idle): an emptyDir goes with its pod.
func CanStop(storage string) error {
	if storage != "pvc" {
		return fmt.Errorf("a sandbox on %s storage loses its checkout and containers with its pod: stopping idle sandboxes needs the Kubernetes integration's storage set to pvc", storage)
	}
	return nil
}

// DefaultStorage is the workspace storage an integration that sets none
// gets: emptydir when one of its runtimes is a VM (CheckStorage), else pvc.
func DefaultStorage(approved []envport.Runtime) string {
	for _, name := range approved {
		if rt, err := lookupRuntime(name); err == nil && rt.vm {
			return "emptydir"
		}
	}
	return "pvc"
}
