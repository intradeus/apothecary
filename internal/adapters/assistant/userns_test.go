package assistant

import (
	"strings"
	"testing"
)

// userNamespaceRefused tells a runtime that cannot make a pod's user
// namespace from the other ways a sandbox fails (a network plugin not ready
// yet, on a fresh cluster), which pass. The live test's probe asks it.
func userNamespaceRefused(warning string) bool {
	if !strings.HasPrefix(warning, "FailedCreatePodSandBox:") {
		return false
	}
	for _, s := range []string{"unshare", "user namespace", "uid_map", "gid_map", "idmap", "set effective UID"} {
		if strings.Contains(warning, s) {
			return true
		}
	}
	return false
}

func TestUserNamespaceRefused(t *testing.T) {
	for warning, want := range map[string]bool{
		// A k3d node on a docker daemon in a sysbox workspace (65536 ids).
		"FailedCreatePodSandBox: Failed to create pod sandbox: rpc error: code = Unknown desc = failed to create network namespace for sandbox \"0f3\": failed to start noop process for unshare: fork/exec /proc/self/exe: operation not permitted": true,
		"FailedCreatePodSandBox: failed to write /proc/11/uid_map: operation not permitted": true,
		// A k3d node in a userns workspace whose node's kubelet gave it 65536
		// ids (the provisioner's nested_user_namespaces off).
		"FailedCreatePodSandBox: Failed to create pod sandbox: rpc error: code = Unknown desc = failed to create network namespace for sandbox \"50e\": failed to set effective UID: invalid argument": true,
		// A fresh cluster's network plugin: it passes, the probe waits.
		"FailedCreatePodSandBox: plugin type=\"flannel\" failed (add): open /run/flannel/subnet.env: no such file or directory": false,
		"FailedScheduling: 0/1 nodes are available: 1 node(s) had untolerated taint":                                            false,
		"BackOff: unshare": false,
		"":                 false,
	} {
		if got := userNamespaceRefused(warning); got != want {
			t.Errorf("userNamespaceRefused(%q) = %v, want %v", warning, got, want)
		}
	}
}
