package wsimage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"

	"apothecary/workspace"
)

// gatewayDockerfile compiles the self-contained gateway daemon
// (workspace/gateway, pure stdlib) and lands it in a minimal alpine image with
// the userland the daemon shells out to (nft, ip). Unlike the shim (extracted as
// a bare binary and bind-mounted), the gateway is a RUNNABLE image: it needs
// nftables/iproute2, and it IS the egress path so it cannot apk-add at start.
const gatewayDockerfile = `FROM golang:1.26-alpine AS build
COPY gateway/ /src/
RUN cd /src && go mod init apothecary-gateway >/dev/null \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /apothecary-gateway .
FROM alpine:3.21
RUN apk add --no-cache nftables iproute2 ip6tables
COPY --from=build /apothecary-gateway /usr/local/bin/apothecary-gateway
ENTRYPOINT ["/usr/local/bin/apothecary-gateway"]
`

// gatewayHash covers the gateway source tree + the builder Dockerfile, so an
// upgrade produces a new tag and stale images are simply abandoned.
func gatewayHash() string {
	h := sha256.New()
	if sub, err := fs.Sub(workspace.BaseContext, "gateway"); err == nil {
		hashFS(h, sub)
	}
	h.Write([]byte(gatewayDockerfile))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// EnsureGatewayImage builds (once, content-addressed) the per-project egress
// gateway image on the host daemon and returns its tag. Idempotent: a cache hit
// (image already present) skips the build.
func (b *Builder) EnsureGatewayImage(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tag := "apothecary/egress-gw:" + gatewayHash()
	if b.exists(ctx, tag) {
		return tag, nil
	}
	dir, err := os.MkdirTemp("", "apothecary-gw-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	sub, err := fs.Sub(workspace.BaseContext, "gateway")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "gateway"), 0o755); err != nil {
		return "", err
	}
	if err := materializeFS(sub, filepath.Join(dir, "gateway")); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(gatewayDockerfile), 0o644); err != nil {
		return "", err
	}
	if err := b.build(ctx, dir, tag); err != nil {
		return "", err
	}
	return tag, nil
}
