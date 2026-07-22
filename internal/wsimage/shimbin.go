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

	"apothecary/workspace"
)

// EnsureShim builds (once, content-addressed) the static in-workspace shim
// binary and returns its host path. The coder path gets the shim via the
// base image's multi-stage build; the devcontainer path runs arbitrary
// repo images, so the binary is built standalone by docker — same
// compiler, same daemon, matching arch — and bind-mounted in. Cached at
// <runsDir>/shim-cache/<sha16>/apothecary-shim; the hash covers the shim
// sources and this builder Dockerfile, so upgrades produce a new path and
// stale caches are simply abandoned.

// shimBuilderDockerfile compiles the embedded source and parks the binary
// in a scratch image for `docker create`+`cp` extraction (create never
// runs it, so scratch is fine).
const shimBuilderDockerfile = `FROM golang:1.26-alpine AS build
COPY shim/ /src/
RUN cd /src && go mod init apothecary-shim >/dev/null \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /apothecary-shim .
FROM scratch
COPY --from=build /apothecary-shim /apothecary-shim
`

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
		if err := b.build(ctx, dir, tag); err != nil {
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
		return "", fmt.Errorf("docker create %s: exit %d: %s", tag, code, tail(out, 1024))
	}
	cid := strings.TrimSpace(out)
	defer func() { _, _, _ = b.docker.Run(ctx, "", "rm", cid) }()
	tmp := dest + ".tmp"
	if out, code, err := b.docker.Run(ctx, "", "cp", cid+":/apothecary-shim", tmp); err != nil || code != 0 {
		return "", fmt.Errorf("docker cp shim: exit %d: %v: %s", code, err, tail(out, 1024))
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
