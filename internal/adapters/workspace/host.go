package workspace

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"apothecary/internal/ports/envport"
)

// A workspace has two independent axes:
//
//   - the HOST is where the run's sandbox lives and how the engine reaches it:
//     a Pod on a Kubernetes cluster (host_k8s.go; the package's tests run on a
//     host of their own);
//   - the DEFINITION is what gets built inside that sandbox, read from the
//     repo: today a devcontainer.json (def_devcontainer.go).
//
// Both hosts give the definition the same thing — a docker daemon to build and
// run in, and the run folder at the same path the engine computes — so a
// definition never knows which host it runs on, and everything the adapter does
// once the workspace container exists (exec, files, sidecar, ports, editor,
// services) is definition- and host-blind.

// Host is where a run's sandbox runs. Unexported methods: the set is the
// adapter's own seam, not a public extension point.
type Host interface {
	// prepare is everything before the definition builds, in this host's
	// order: the checkout, the shim, the egress attachment and the sandbox.
	// detect picks the definition from the checkout and refuses one this
	// deployment cannot honor (the daemon door); prepare calls it as soon as
	// the checkout exists, so such a repo fails before the (expensive) sandbox
	// comes up wherever the host allows it.
	prepare(ctx context.Context, name string, spec envport.EnvSpec, plog func(string, ...any), detect func(context.Context, string) (Definition, error)) (prep, error)

	// unprivileged reports that this host's sandboxes run without
	// --privileged (a user-namespace runtime): sandbox root is not host root.
	// It decides the daemon door under APOTHECARY_WORKSPACE_DOCKER=auto;
	// unprivilegedHint is how an operator gets there on this host.
	unprivileged() bool
	unprivilegedHint() string

	// runInner runs one CLI invocation against the run's sandbox: its inner
	// docker daemon, its checkout, its devcontainer CLI.
	runInner(ctx context.Context, name, bin string, args []string, stdin []byte, tee io.Writer, extraEnv ...string) ([]byte, []byte, int, error)

	// fs is the run folder as the sandbox sees it (the checkout lives there).
	fs(name string) sandboxFS

	// sandboxState is the sandbox's state word ("running", "created",
	// "exited", ...) or "" when there is none. An unreachable host is an
	// error, never "".
	sandboxState(ctx context.Context, name string) (state string, err error)
	destroySandbox(ctx context.Context, name string) error
	// resetSandbox empties the sandbox for a compose rebuild: every workspace
	// and side container goes, and with them what they hold in the daemon.
	// The checkout stays: agent commits live there and nowhere else yet.
	resetSandbox(ctx context.Context, name string) error

	// The engine bridge: what makes engineURL reachable from inside the
	// workspace. bridgePorts are the workspace-loopback ports it occupies
	// (excluded from port discovery).
	ensureBridge(env envport.EnvID) error
	startBridgeRelay(ctx context.Context, name, cid string) error
	closeBridge(env envport.EnvID)
	bridgePorts() []int
	engineURL() string

	// dial reaches a unix socket the workspace serves in its shim dir
	// (/opt/apothecary/<sock>); waitSock blocks until it exists.
	dial(name, sock string) envport.PortConn
	waitSock(ctx context.Context, name, sock string) error

	// refreshShim makes the mounted shim current (Reattach).
	refreshShim(ctx context.Context, name string) error
	sample(ctx context.Context, name string) (envport.ResourceSample, error)
	test(ctx context.Context) error
	close()
}

// daemonDoorSocket is dockerd's default socket inside the sandbox: what the
// daemon door hands a docker-outside-of-docker devcontainer.
const daemonDoorSocket = "/var/run/docker.sock"

// prep is what Host.prepare hands the definition: the checkout (as the sandbox
// sees it), the shim dir to mount at /opt/apothecary, the tools bundle to
// mount at /opt/apothecary/tools, the egress gateway's IP on the run's link
// ("" when the firewall is off) and the detected definition.
type prep struct {
	src, shimDir, toolsDir, gwIP string
	def                          Definition
}

// sandboxFS is the small file surface the definition needs on the run folder:
// the checkout's config files. Paths are absolute and identical on every host
// (path parity).
type sandboxFS interface {
	ReadFile(ctx context.Context, path string) ([]byte, error)
	WriteFile(ctx context.Context, path string, data []byte, mode fs.FileMode) error
	MkdirAll(ctx context.Context, path string) error
	// Exists reports whether anything is at path; IsRegular whether a regular
	// file is (never following a final symlink).
	Exists(ctx context.Context, path string) bool
	IsRegular(ctx context.Context, path string) bool
	// Walk lists the regular files under dir, recursively (absolute paths);
	// a missing dir is no files, not an error.
	Walk(ctx context.Context, dir string) ([]string, error)
}

// osFS is sandboxFS on the engine's own filesystem: the run folder as the
// engine sees it (the tests' host; the Kubernetes host reads the pod's
// volume through exec instead).
type osFS struct{}

func (osFS) ReadFile(_ context.Context, path string) ([]byte, error) { return os.ReadFile(path) }

func (osFS) WriteFile(_ context.Context, path string, data []byte, mode fs.FileMode) error {
	return os.WriteFile(path, data, mode)
}

func (osFS) MkdirAll(_ context.Context, path string) error { return os.MkdirAll(path, 0o755) }

func (osFS) Exists(_ context.Context, path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (osFS) IsRegular(_ context.Context, path string) bool {
	st, err := os.Lstat(path)
	return err == nil && st.Mode().IsRegular()
}

func (osFS) Walk(_ context.Context, dir string) ([]string, error) {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

var _ sandboxFS = osFS{}
