package wsimage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"apothecary/internal/platform/shell"
	"apothecary/workspace"
)

// ToolsImageTag is the tag EnsureTools builds, without building: the tools
// bundle (every harness CLI + code-server as standalone builds, see
// workspace/images/tools/Dockerfile), content-addressed by that directory.
func ToolsImageTag() string { return "apothecary/tools:" + workspace.ToolsHash() }

// toolsHashFile is the marker the bundle carries (written by the Dockerfile)
// and the engine's cache-hit test: a dir with it is a complete extraction.
const toolsHashFile = "TOOLS_HASH"

// EnsureTools builds (once, content-addressed) the tools bundle image on the
// host daemon, extracts its /tools onto the engine host and returns that
// directory: <runsDir>/tools-cache/<hash>. The docker host bind-mounts the
// PARENT (tools-cache) into every sandbox at the identical path, and each
// workspace mounts its hash dir at /opt/apothecary/tools — so an engine
// upgrade's new bundle is visible to a Rebuild without recreating the
// sandbox, and an old hash dir is NEVER removed here: a running workspace may
// still be executing from it (a sweep must first check `docker ps` mounts).
//
// Same shape as EnsureShim, a thousand times the size (~1.2 GB): the first
// call after an upgrade downloads every release artifact and compiles
// codex-acp, inside Builder.mu — cmdServe warms it at boot so a provision
// rarely pays for it.
func (b *Builder) EnsureTools(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	hash := workspace.ToolsHash()
	base := b.runsDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "apothecary-tools")
	}
	dest := filepath.Join(base, "tools-cache", hash)
	if _, err := os.Stat(filepath.Join(dest, toolsHashFile)); err == nil {
		return dest, nil
	}

	tag := ToolsImageTag()
	if !b.exists(ctx, tag) {
		dir, err := os.MkdirTemp("", "apothecary-tools-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		sub, err := fs.Sub(workspace.Images, "images/tools")
		if err != nil {
			return "", err
		}
		// The Dockerfile is part of the tree: the build context IS the dir.
		if err := materializeFS(sub, dir); err != nil {
			return "", err
		}
		if err := b.build(ctx, dir, tag, "TOOLS_HASH="+hash); err != nil {
			return "", err
		}
	}

	// Extract: create (never started — the entrypoint is the k8s seeder),
	// cp, rm. A stale .tmp from an interrupted extraction must go first:
	// `docker cp` into an existing dir nests /tools inside it.
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	tmp := dest + ".tmp"
	if err := os.RemoveAll(tmp); err != nil {
		return "", err
	}
	out, code, err := b.docker.Run(ctx, "", "create", tag)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("docker create %s: exit %d: %s", tag, code, shell.Tail(out, 1024))
	}
	cid := strings.TrimSpace(out)
	defer func() { _, _, _ = b.docker.Run(ctx, "", "rm", cid) }()
	if out, code, err := b.docker.Run(ctx, "", "cp", cid+":/tools", tmp); err != nil || code != 0 {
		return "", fmt.Errorf("docker cp tools bundle: exit %d: %v: %s", code, err, shell.Tail(out, 1024))
	}
	// Atomic publish; a concurrent engine on the same RunsDir may have won
	// the race, in which case its complete extraction is the one to use.
	if err := os.Rename(tmp, dest); err != nil {
		if _, serr := os.Stat(filepath.Join(dest, toolsHashFile)); serr == nil {
			_ = os.RemoveAll(tmp)
			return dest, nil
		}
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dest, toolsHashFile)); err != nil {
		return "", errors.New("tools bundle extraction is incomplete: no " + toolsHashFile + " in " + dest)
	}
	return dest, nil
}
