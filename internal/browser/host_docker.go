package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"apothecary/internal/ports"
)

// dockerHost runs each project's browser as a container on the engine's
// docker daemon, with NO network: a session's Chromium reaches the engine's
// routing proxy through a unix socket on a per-project bind mount (the
// sanctioned channel on docker hosts that drop container→host IP traffic).
type dockerHost struct {
	runsDir string
	run     HostRun
	stream  StreamRun
}

// dir is the project's bind mount: the sessions' proxy sockets, at the same
// path in the daemon and the container.
func (h *dockerHost) dir(project string) string {
	return filepath.Join(h.runsDir, "browser", ports.ProjectHostKey(project))
}

// maxSockPath is sun_path's limit, less the terminating NUL.
const maxSockPath = 107

func (h *dockerHost) Name(project string) string { return containerName(project) }

func (h *dockerHost) docker(ctx context.Context, args ...string) (string, error) {
	out, errb, code, err := h.run(ctx, args)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("docker %s: exit %d: %s", args[0], code, strings.TrimSpace(string(errb)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (h *dockerHost) Ensure(ctx context.Context, name, projectName, img string, replace bool) error {
	dir := h.dir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	c := containerName(name)
	cur, err := h.docker(ctx, "ps", "-a", "--filter", "name=^"+c+"$", "--format", "{{.Image}}")
	if err != nil {
		return err
	}
	if cur != "" {
		if cur == img || !replace {
			_, err := h.docker(ctx, "start", c) // a no-op when running
			return err
		}
		if _, err := h.docker(ctx, "rm", "-f", c); err != nil {
			return err
		}
	}
	args := []string{"run", "-d", "--name", c,
		"--label", "apothecary=true",
		"--label", "apothecary.project=" + name, // the id: what Running finds it by
		"--label", "apothecary.browser=true",
	}
	if projectName != "" {
		args = append(args, "--label", "apothecary.project.name="+projectName) // for a human's docker ps
	}
	_, err = h.docker(ctx, append(args,
		// No network at all: the routing proxy is the only way out.
		"--network", "none",
		"--init",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--shm-size", "1g",
		"-v", dir+":"+dir,
		img)...)
	return err
}

func (h *dockerHost) Stop(ctx context.Context, name string) error {
	_, err := h.docker(ctx, "stop", "-t", "5", containerName(name))
	return err
}

func (h *dockerHost) Running(ctx context.Context) ([]string, error) {
	out, err := h.docker(ctx, "ps", "--filter", "label=apothecary.browser=true",
		"--format", `{{.Label "apothecary.project"}}`)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func (h *dockerHost) Inspect(ctx context.Context, name string) (string, bool, error) {
	out, err := h.docker(ctx, "ps", "-a", "--filter", "name=^"+containerName(name)+"$", "--format", "{{.Image}}\t{{.State}}")
	if err != nil || out == "" {
		return "", false, err
	}
	img, state, _ := strings.Cut(out, "\t")
	return img, state == "running", nil
}

// Listen: a fresh unix socket on the project's bind mount.
func (h *dockerHost) Listen(_ context.Context, project, sid string) (net.Listener, []string, error) {
	sock := filepath.Join(h.dir(project), "s-"+sid+".sock")
	if len(sock) > maxSockPath {
		return nil, nil, fmt.Errorf("browser: socket path %s is longer than a unix socket allows (%d) — use a shorter runs dir", sock, maxSockPath)
	}
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, nil, fmt.Errorf("browser: proxy socket: %w", err)
	}
	// The container's user is whoever its image says; open the socket to it.
	_ = os.Chmod(sock, 0o666)
	return &removeOnClose{Listener: ln, path: sock}, []string{"--sock", sock}, nil
}

func (h *dockerHost) Exec(ctx context.Context, project string, argv []string, stdin io.Reader, stdout io.Writer) error {
	return h.stream(ctx, append([]string{"exec", "-i", containerName(project)}, argv...), stdin, stdout)
}

// removeOnClose deletes the socket file with its listener.
type removeOnClose struct {
	net.Listener
	path string
}

func (r *removeOnClose) Close() error {
	err := r.Listener.Close()
	_ = os.Remove(r.path)
	return err
}

func dockerRun(ctx context.Context, args []string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.Bytes(), errb.Bytes(), ee.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

func dockerStream(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin, cmd.Stdout = stdin, stdout
	errb := &tailBuffer{max: 4 << 10}
	cmd.Stderr = errb
	// The stdin copier blocks on the agent's side of the stream, which only
	// the caller closes: do not wait on it once the exec is gone.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

var _ Host = (*dockerHost)(nil)
