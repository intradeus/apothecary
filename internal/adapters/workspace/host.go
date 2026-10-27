package workspace

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"apothecary/internal/ports"
)

// A workspace has two independent axes:
//
//   - the HOST is where the run's sandbox lives and how the engine reaches it:
//     a Docker-in-Docker container on the engine's docker daemon (host_docker.go)
//     or a Pod on a Kubernetes cluster (host_k8s.go);
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
	// detect picks the definition from the checkout; prepare calls it as soon
	// as the checkout exists, so a repo without a definition fails before the
	// (expensive) sandbox comes up wherever the host allows it.
	prepare(ctx context.Context, name string, spec ports.EnvSpec, plog func(string, ...any), detect func(context.Context, string) (Definition, error)) (prep, error)

	// runInner runs one CLI invocation against the run's sandbox: its inner
	// docker daemon, its checkout, its devcontainer CLI.
	runInner(ctx context.Context, name, bin string, args []string, stdin []byte, tee io.Writer, extraEnv ...string) ([]byte, []byte, int, error)

	// fs is the run folder as the sandbox sees it (the checkout lives there).
	fs(name string) sandboxFS

	// sandboxState is the sandbox's state word ("running", "created",
	// "exited", ...) or "" when there is none. legacy reports a pre-sandbox
	// env whose workspace lives directly on the host daemon (docker only). An
	// unreachable host is an error, never "".
	sandboxState(ctx context.Context, name string) (state string, legacy bool, err error)
	destroySandbox(ctx context.Context, name string) error

	// The engine bridge: what makes engineURL reachable from inside the
	// workspace. bridgePorts are the workspace-loopback ports it occupies
	// (excluded from port discovery).
	ensureBridge(env ports.EnvID) error
	startBridgeRelay(ctx context.Context, name, cid string) error
	closeBridge(env ports.EnvID)
	bridgePorts() []int
	engineURL() string

	// dial reaches a unix socket the workspace serves in its shim dir
	// (/opt/apothecary/<sock>); waitSock blocks until it exists.
	dial(name, sock string) ports.PortConn
	waitSock(ctx context.Context, name, sock string) error

	// refreshShim makes the mounted shim current (Reattach).
	refreshShim(ctx context.Context, name string) error
	terminalCommand(name, container string) string
	sample(ctx context.Context, name string) (ports.ResourceSample, error)
	test(ctx context.Context) error
	close()
}

// prep is what Host.prepare hands the definition: the checkout (as the sandbox
// sees it), the shim dir to mount at /opt/apothecary, the egress gateway's IP
// on the run's link ("" when the firewall is off) and the detected definition.
type prep struct {
	src, shimDir, gwIP string
	def                Definition
}

// sandboxFS is the small file surface the definition needs on the run folder:
// the checkout's config files, the injected features, git's exclude file.
// Paths are absolute and identical on every host (path parity).
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
	// AppendLineOnce appends line to the file unless a line equal to it (after
	// trimming) is already there, creating the file and its parents.
	AppendLineOnce(ctx context.Context, path, line string) error
}

// osFS is sandboxFS on the engine's own filesystem — the docker host, whose
// run folder is bind-mounted into the sandbox at the same path.
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

func (osFS) AppendLineOnce(_ context.Context, path, line string) error {
	existing, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(l) == line {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

var _ sandboxFS = osFS{}
