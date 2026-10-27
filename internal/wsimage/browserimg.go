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

// playwrightMCPVersion pins the MCP server the project browser runs (and,
// through its own dependency, the Playwright and Chromium build). In this
// text, so bumping it changes BrowserImageTag.
const playwrightMCPVersion = "0.0.83"

// browserDockerfile is the per-project browser (internal/browser): node,
// @playwright/mcp and the Chromium its Playwright drives, plus the shim —
// built from the same source as the workspace's — whose `browser-session`
// runs one MCP server per agent session. The container has no network, so
// everything is installed here, at build time; it idles on `sleep` until a
// session is exec'd into it. The browser comes from Playwright's own CLI —
// the copy @playwright/mcp depends on — so its build is the one the server
// expects.
var browserDockerfile = workspace.Dockerfile("browser")

// browserHash covers the shim source tree (the image carries a shim) and
// the Dockerfile.
func browserHash() string {
	h := sha256.New()
	if sub, err := fs.Sub(workspace.BaseContext, "shim"); err == nil {
		hashFS(h, sub)
	}
	h.Write([]byte(browserDockerfile))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// BrowserImageTag is the tag EnsureBrowserImage builds, without building.
func BrowserImageTag() string { return "apothecary/browser:" + browserHash() }

// EnsureBrowserImage builds (once, content-addressed) the project browser
// image on the host daemon and returns its tag.
func (b *Builder) EnsureBrowserImage(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tag := BrowserImageTag()
	if b.exists(ctx, tag) {
		return tag, nil
	}
	dir, err := os.MkdirTemp("", "apothecary-browser-*")
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
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(browserDockerfile), 0o644); err != nil {
		return "", err
	}
	if err := b.build(ctx, dir, tag, "SHIM_HASH="+workspace.ShimSourceHash()); err != nil {
		return "", err
	}
	return tag, nil
}
