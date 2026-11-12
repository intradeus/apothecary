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

// BaseContext holds the invariant material apothecary ships: the static
// shim's source tree and the egress gateway daemon's. deploy/images.sh builds
// them into the release's shim and egress-gw images; the engine embeds them
// only to hash them, so it can tell whether a seeded shim matches it.
//
//go:embed shim gateway
var BaseContext embed.FS

// Images holds the Dockerfiles of the images built from the sources above
// (shim, gateway), each with this directory as its build context, and the
// tools bundle (images/tools: its Dockerfile plus the scripts it copies,
// built with that directory as the context). CI publishes them
// (deploy/images.sh); the engine never builds an image.
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
func ShimSourceHash() string { return hashTree(BaseContext, "shim") }

// ToolsHash identifies the tools bundle: the hash of images/tools (its
// Dockerfile — whose ARG defaults are the version pins — and the scripts it
// copies). Baked into every bundle as /tools/TOOLS_HASH (`apothecaryd
// tools-hash` prints it for CI), so a bundle seeded from a published image
// can be compared with the one the engine expects; a bumped pin moves it.
func ToolsHash() string { return hashTree(Images, "images/tools") }

// hashTree is the first 16 hex chars of a sha256 over every file of dir in
// fsys (path, length and contents, in walk order).
func hashTree(fsys fs.FS, dir string) string {
	h := sha256.New()
	sub, err := fs.Sub(fsys, dir)
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
