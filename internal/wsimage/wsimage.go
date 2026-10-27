// Package wsimage builds and caches, on a docker host, the images apothecary
// owns: the static in-workspace shim (EnsureShim), the per-project egress
// gateway (EnsureGatewayImage), the project browser (EnsureBrowserImage) and
// the tools bundle (EnsureTools) — every workspace runs the target repo's OWN
// devcontainer image, so these are the only builds. On Kubernetes CI
// publishes the same Dockerfiles (deploy/images.sh) and the engine builds
// nothing.
package wsimage

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Runner executes one docker CLI invocation in dir; swapped in tests.
type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (output string, exitCode int, err error)
}

type cliRunner struct{}

func (cliRunner) Run(ctx context.Context, dir string, args ...string) (string, int, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); ok {
		return string(out), ee.ExitCode(), nil
	}
	if err != nil {
		return string(out), 0, err
	}
	return string(out), 0, nil
}

type Builder struct {
	docker  Runner
	runsDir string

	mu sync.Mutex // one build at a time; docker layer-caches the rest
}

func New(runsDir string) *Builder {
	return &Builder{docker: cliRunner{}, runsDir: runsDir}
}

// NewWithRunner is the test constructor.
func NewWithRunner(r Runner, runsDir string) *Builder {
	return &Builder{docker: r, runsDir: runsDir}
}

// hashFS feeds every file of fsys (sorted walk order) into h.
func hashFS(h io.Writer, fsys fs.FS) {
	_ = fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(data))
		h.Write(data)
		return nil
	})
}

// materializeFS writes every file of fsys under dir, creating subdirs.
func materializeFS(fsys fs.FS, dir string) error {
	return fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == "." {
				return nil
			}
			return os.MkdirAll(filepath.Join(dir, path), 0o755)
		}
		data, rerr := fs.ReadFile(fsys, path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(filepath.Join(dir, path), data, 0o644)
	})
}

func (b *Builder) exists(ctx context.Context, ref string) bool {
	_, code, err := b.docker.Run(ctx, "", "image", "inspect", ref)
	return err == nil && code == 0
}

// build runs `docker build` and persists the full log under
// runs_dir/builds/<tag-suffix>.log.
func (b *Builder) build(ctx context.Context, dir, tag string, buildArgs ...string) error {
	args := []string{"build", "-t", tag}
	for _, a := range buildArgs {
		args = append(args, "--build-arg", a)
	}
	out, code, err := b.docker.Run(ctx, dir, append(args, ".")...)
	b.writeLog(tag, out)
	if err != nil {
		return fmt.Errorf("docker build %s: %w", tag, err)
	}
	if code != 0 {
		return fmt.Errorf("docker build %s: exit %d: %s", tag, code, tail(out, 4096))
	}
	return nil
}

func (b *Builder) writeLog(tag, out string) {
	if b.runsDir == "" {
		return
	}
	dir := filepath.Join(b.runsDir, "builds")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := strings.ReplaceAll(tag, "/", "-")
	name = strings.ReplaceAll(name, ":", "-")
	_ = os.WriteFile(filepath.Join(dir, name+".log"), []byte(out), 0o644)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
