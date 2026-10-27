// Package workspace embeds the apothecary-owned workspace sources — single
// copies, consumed at runtime. Everything here is invariant material (shim,
// firewall, agent wiring); the workspace itself is the target repo's own
// devcontainer image, so apothecary builds no base image.
package workspace

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
)

// BaseContext holds the invariant material apothecary compiles/injects: the
// static shim's source tree (compiled by wsimage.EnsureShim, bind-mounted in)
// and the egress gateway daemon's source tree (compiled into the per-project
// egress-gw image by wsimage.EnsureGatewayImage). Egress is enforced by that
// external gateway now, so there is no in-container firewall feature.
//
//go:embed shim gateway
var BaseContext embed.FS

// HarnessFeature is the injected devcontainer feature that installs the
// project's coding harness into repo-authored dev containers, which run the
// REPO's image and so never get the §8 pipeline image that bakes it in. The
// per-project install commands are NOT here — the adapter generates them
// into the materialized feature dir (harness-binaries + harness-install.sh).
//
//go:embed harness-feature
var HarnessFeature embed.FS

// CodeServer is the injected devcontainer feature that installs code-server
// (VS Code in the browser) + the project's/repo's VS Code extensions at build
// time. The extension list is NOT here — the adapter generates the
// `extensions` file into the materialized feature dir. Always injected;
// code-server is started lazily on demand ("Open editor"), not at build.
//
//go:embed codeserver-feature
var CodeServer embed.FS

// Images holds the Dockerfiles of the images apothecary builds from the
// sources above (shim, gateway, browser), each with this directory as its
// build context. The engine builds them at runtime on a docker host
// (internal/wsimage); CI publishes the same files for Kubernetes, where the
// engine cannot build.
//
//go:embed images
var Images embed.FS

// Dockerfile returns images/<name>/Dockerfile.
func Dockerfile(name string) string {
	b, err := Images.ReadFile("images/" + name + "/Dockerfile")
	if err != nil {
		panic(err) // embedded at build time: a missing file is a build bug
	}
	return string(b)
}

// ShimSourceHash identifies the shim's source tree. It is baked into every
// shim build (main.sourceHash) and is what `shim version` prints, so a shim
// seeded from a published image can be checked against the engine that
// expects it.
func ShimSourceHash() string {
	h := sha256.New()
	sub, err := fs.Sub(BaseContext, "shim")
	if err != nil {
		return ""
	}
	_ = fs.WalkDir(sub, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := fs.ReadFile(sub, path)
		if rerr != nil {
			return rerr
		}
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(data))
		h.Write(data)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}
