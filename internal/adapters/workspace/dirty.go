package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"apothecary/internal/ports"
)

// A "dirty" workspace: its container was built from a
// devcontainer configuration that has since changed — an agent edited the
// Dockerfile, a human added a feature — so a rebuild would produce something
// different. Provision records a digest of the build inputs next to the run
// (configSumFile); ConfigDirty recomputes it from the checkout and compares.
// The checkout is a bind mount, so an edit made inside the container is
// visible here the moment it lands.

const configSumFile = "devcontainer.sum"

// recordConfigDigest writes the digest of what was just built next to the run.
// Best-effort: a failure costs the dirty hint for this workspace, nothing else.
func (a *Adapter) recordConfigDigest(name, src string, plog func(string, ...any)) {
	sum, err := definitionDigest(context.Background(), a.defFor(name), a.host.fs(name), src)
	if err == nil {
		err = os.WriteFile(filepath.Join(a.folderFor(name), configSumFile), []byte(sum+"\n"), 0o644)
	}
	if err != nil && plog != nil {
		plog("→ could not record the devcontainer config digest (no rebuild hint for this workspace): %v\n", err)
	}
}

// ConfigDirty implements ports.WorkspaceDirtyChecker: the checkout's build
// inputs no longer match the digest recorded when the container was built.
// A workspace with no recorded digest (built before this existed, or the
// write failed) reports clean — unknown is not dirty.
func (a *Adapter) ConfigDirty(ctx context.Context, env ports.EnvID) (bool, error) {
	name := string(env)
	recorded, err := os.ReadFile(filepath.Join(a.folderFor(name), configSumFile))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current, err := definitionDigest(ctx, a.defFor(name), a.host.fs(name), a.srcDir(name))
	if err != nil {
		return false, fmt.Errorf("digest %s devcontainer config: %w", name, err)
	}
	return current != strings.TrimSpace(string(recorded)), nil
}

var _ ports.WorkspaceDirtyChecker = (*Adapter)(nil)
