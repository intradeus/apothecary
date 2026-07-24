// Package workspace embeds the apothecary-owned workspace sources — single
// copies, consumed at runtime. Everything here is invariant material (shim,
// firewall, agent wiring); the workspace itself is the target repo's own
// devcontainer image, so apothecary builds no base image.
package workspace

import "embed"

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
