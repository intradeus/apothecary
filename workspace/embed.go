// Package workspace embeds the apothecary-owned workspace sources — single
// copies, consumed at runtime. Everything here is invariant material (shim,
// firewall, agent wiring); the workspace itself is the target repo's own
// devcontainer image, so apothecary builds no base image.
package workspace

import "embed"

// BaseContext holds the invariant material injected into every workspace: the
// static shim's source tree (compiled by wsimage.EnsureShim, bind-mounted in)
// and init-firewall.sh (single-sourced into the firewall devcontainer feature).
//
//go:embed init-firewall.sh shim
var BaseContext embed.FS

// Feature is the injected devcontainer feature that brings the §11 egress
// firewall to repo-authored dev containers (the devcontainer provisioner):
// capAdd NET_ADMIN + a container-start entrypoint. init-firewall.sh is
// NOT here — it is single-sourced from BaseContext and copied into the
// materialized feature dir by the adapter.
//
//go:embed feature
var Feature embed.FS

// HarnessFeature is the injected devcontainer feature that installs the
// project's coding harness into repo-authored dev containers, which run the
// REPO's image and so never get the §8 pipeline image that bakes it in. The
// per-project install commands are NOT here — the adapter generates them
// into the materialized feature dir (harness-binaries + harness-install.sh).
//
//go:embed harness-feature
var HarnessFeature embed.FS
