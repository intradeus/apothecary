package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"path/filepath"

	"apothecary/internal/ports/envport"
)

// Definition is how a repo describes its workspace, and how to build and enter
// it inside a sandbox's docker daemon. The repo picks it (Detect, first match
// wins) — it is a property of the checkout, never of the provisioner.
type Definition interface {
	// Name is stable and recorded per run, so an env keeps its definition
	// across engine restarts without re-detecting.
	Name() string
	Detect(ctx context.Context, fsys sandboxFS, src string) bool
	// Up builds (or reuses) and starts the workspace container, WITHOUT the
	// repo's lifecycle hooks: RunUserCommands runs them once the adapter has
	// handed the checkout to the remote user (rootInit).
	Up(ctx context.Context, e defEnv, spec envport.EnvSpec, o upOptions) (Workspace, error)
	RunUserCommands(ctx context.Context, e defEnv, o upOptions) error
	// SandboxDaemonUse says what in the definition asks for the sandbox's own
	// docker daemon (devcontainer: the docker-outside-of-docker feature, or a
	// /var/run/docker.sock bind), "" when nothing does — the input to the
	// daemon-door refusal. Best-effort: unreadable or unparseable asks for
	// nothing.
	SandboxDaemonUse(ctx context.Context, fsys sandboxFS, src string) string
	// ExecArgs is the command prefix that runs a program inside the workspace
	// container as its remote user, with remoteEnv set; the caller appends the
	// program and its arguments.
	ExecArgs(name string, remoteEnv ...string) (bin string, args []string)
	// DeclaredPorts are the ports the definition asks to forward.
	DeclaredPorts(ctx context.Context, e defEnv) []int
	// IsCompose reports a multi-container workspace (rebuild = full recreate).
	IsCompose(ctx context.Context, e defEnv) bool
	// ConfigFiles are the files (relative to src, sorted) whose contents
	// decide what Up builds — the "config changed, rebuild?" digest inputs.
	ConfigFiles(ctx context.Context, fsys sandboxFS, src string) ([]string, error)
	// EditorExtensions are the VS Code extension ids the definition itself
	// declares (devcontainer: customizations.vscode.extensions). Best-effort,
	// nil on any miss; the adapter unions them with the project's list when
	// the browser editor starts.
	EditorExtensions(ctx context.Context, e defEnv) []string
}

// defEnv is what a definition gets from the adapter: the env, its checkout,
// the run folder's filesystem and a runner bound to the sandbox.
type defEnv struct {
	name, src string
	fs        sandboxFS
	run       func(ctx context.Context, bin string, args []string, stdin []byte, tee io.Writer, extraEnv ...string) ([]byte, []byte, int, error)
}

// upOptions are the adapter's demands on a build: mount the shim and the
// tools bundle, use the project's build cache behind the gateway, recreate,
// skip the cache.
type upOptions struct {
	shimDir        string
	toolsDir       string // mounted at /opt/apothecary/tools
	gwIP, project  string
	removeExisting bool
	noCache        bool
	// docker injects the docker-outside-of-docker feature into the build —
	// the project's "Docker in workspaces" toggle for a repo that does not
	// declare the feature itself (never both: the CLI would install it twice).
	docker bool
	log    io.Writer
	plog   func(string, ...any)
}

// Workspace is the started workspace container.
type Workspace struct {
	ContainerID     string
	RemoteUser      string
	WorkspaceFolder string
}

// definitionDigest hashes a definition's build inputs: each file's path and
// contents, in ConfigFiles order, so a rename counts as a change too.
func definitionDigest(ctx context.Context, def Definition, fsys sandboxFS, src string) (string, error) {
	files, err := def.ConfigFiles(ctx, fsys, src)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, rel := range files {
		data, err := fsys.ReadFile(ctx, filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			return "", err
		}
		h.Write([]byte(rel))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
