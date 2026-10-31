package wsimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"apothecary/internal/platform/shell"
	"apothecary/workspace"
)

// EnsureShim builds (once, content-addressed) the static in-workspace shim
// binary and returns its host path. The devcontainer path runs arbitrary
// repo images, so the binary is built standalone by docker — same
// compiler, same daemon, matching arch — and bind-mounted in. Cached at
// <runsDir>/shim-cache/<sha16>/apothecary-shim; the hash covers the shim
// sources and this builder Dockerfile, so upgrades produce a new path and
// stale caches are simply abandoned.

// gojqVersion and xcryptoVersion pin the shim's two non-stdlib
// dependencies: gojq (the `jq` subcommand) and x/crypto (the sidecar's SSH
// server). The shim source can't carry its own go.mod (go:embed won't take
// a nested module), so the builder `go get`s these exact versions — their
// own deps then resolve from their go.mod files alone, checksum-verified by
// the default GOSUMDB. Each must equal the root go.mod's (the shim also
// compiles in the root module); TestShimDependencyPinsMatchGoMod holds them
// together.
const (
	gojqVersion    = "v0.12.19"
	xcryptoVersion = "v0.54.0"
)

// shimBuilderDockerfile compiles the embedded source and parks the binary
// in a scratch image for `docker create`+`cp` extraction (create never
// runs it, so scratch is fine). The pin is in this text, so bumping it
// changes shimHash.
var shimBuilderDockerfile = workspace.Dockerfile("shim")

// shimHash covers the shim source tree + the builder Dockerfile.
func shimHash() string {
	h := sha256.New()
	sub, err := fs.Sub(workspace.BaseContext, "shim")
	if err == nil {
		hashFS(h, sub)
	}
	h.Write([]byte(shimBuilderDockerfile))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (b *Builder) EnsureShim(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	hash := shimHash()
	base := b.runsDir
	if base == "" {
		base = filepath.Join(os.TempDir(), "apothecary-shim")
	}
	dest := filepath.Join(base, "shim-cache", hash, "apothecary-shim")
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}

	tag := "apothecary/shim:" + hash
	if !b.exists(ctx, tag) {
		dir, err := os.MkdirTemp("", "apothecary-shim-*")
		if err != nil {
			return "", err
		}
		defer os.RemoveAll(dir)
		sub, err := fs.Sub(workspace.BaseContext, "shim")
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Join(dir, "shim"), 0o755); err != nil {
			return "", err
		}
		if err := materializeFS(sub, filepath.Join(dir, "shim")); err != nil {
			return "", err
		}
		if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(shimBuilderDockerfile), 0o644); err != nil {
			return "", err
		}
		if err := b.build(ctx, dir, tag, "SHIM_HASH="+workspace.ShimSourceHash()); err != nil {
			return "", err
		}
	}

	// Extract: create (never runs — scratch image), cp, rm.
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	// scratch images have no CMD and `docker create` insists on one; any
	// word works — the container is never started.
	out, code, err := b.docker.Run(ctx, "", "create", tag, "noop")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("docker create %s: exit %d: %s", tag, code, shell.Tail(out, 1024))
	}
	cid := strings.TrimSpace(out)
	defer func() { _, _, _ = b.docker.Run(ctx, "", "rm", cid) }()
	tmp := dest + ".tmp"
	if out, code, err := b.docker.Run(ctx, "", "cp", cid+":/apothecary-shim", tmp); err != nil || code != 0 {
		return "", fmt.Errorf("docker cp shim: exit %d: %v: %s", code, err, shell.Tail(out, 1024))
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	// Atomic publish: a concurrent EnsureShim (another engine process on
	// the same RunsDir) sees either nothing or the complete binary.
	if err := os.Rename(tmp, dest); err != nil {
		return "", err
	}
	return dest, nil
}
